package control

// subagents.go — /subagents 端点:返回某 Claude 会话「当前正在运行的子代理」列表。
//
// 判定靠「启动事件 ↔ 完成事件按 tool_use_id 配对」(不是 tool_use/tool_result 配对,
// 因为后台 agent 启动 3ms 内就写了一条 async_launched 的 tool_result,那不是完成):
//
//  1. 登记:type=assistant 行,message.content[] 里 type=tool_use 且 name∈{Agent,Task} 的块
//     → toolUseId=block.id, agentType=input.subagent_type, description=input.description,
//     startedAt=该行 timestamp, bg=input.run_in_background。
//     name==Workflow 的块也登记(kind=workflow):它无 subagent_type/description,runId/描述从
//     该 tool_use 对应的 async_launched 行(顶层 toolUseResult.runId/summary)补全。
//  2. 后台 agent 完成:某行(queue-operation / user)的字符串内容含
//     <task-notification>…<tool-use-id>TUID</tool-use-id>…<status>S</status>…,
//     S 属 terminalStatuses(completed/failed/stopped/killed/canceled/cancelled/timed_out/
//     timeout/error/aborted/interrupted)→ 该 agent 已结束。
//  3. 同步 agent 完成:一条 tool_result(type=user 行,content[] 里 tool_use_id==toolUseId)
//     且其顶层 toolUseResult.status != "async_launched"(真实结果)。后台那条 async_launched
//     的 tool_result 要排除。
//  4. 运行中 = 所有登记的 - 已出现终止信号的。

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
)

// runningAgent 是一条「运行中子代理 / 工作流」(发给 UI 的最小单元)。
// Kind 区分种类:""/"agent" 普通子代理,"workflow" 编排运行。
// RunID 仅 workflow 有(从 async_launched 行的顶层 toolUseResult.runId 取),供前端钻入 WorkflowCard。
type runningAgent struct {
	ToolUseID   string `json:"toolUseId"`
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	StartedAt   string `json:"startedAt"`
	Kind        string `json:"kind,omitempty"`  // ""/"agent" | "workflow"
	RunID       string `json:"runId,omitempty"` // workflow 的 wf_<id>(从 async_launched 取)
}

var (
	// 整条 task-notification 块:一行可能含多条,逐块取出后在块内配对 id+status(避免跨通报错配)。
	tnBlockRe   = regexp.MustCompile(`<task-notification>(.*?)</task-notification>`)
	tnToolUseRe = regexp.MustCompile(`<tool-use-id>(.*?)</tool-use-id>`)
	tnStatusRe  = regexp.MustCompile(`<status>(.*?)</status>`)
)

// 终止类 task-notification 状态:出现任一即视为「该 agent 已不再运行」。
// 兼顾两种拼写(canceled / cancelled —— claude code 多用 JS 系的双 l "cancelled")与其它明确的终止态
// (timed_out / timeout / error / aborted / interrupted)。漏一个终止态 = 该后台 agent 永远卡在
// 「运行中」、AgentDock 一直显示「N 个运行中」+ 脉动点(= 用户反馈「busy 误报」的一种:已完成却仍显运行)。
// 刻意只收【明确表示已结束】的状态;非终止/中间态(如 running/in_progress/async_launched)绝不进此集。
var terminalStatuses = map[string]bool{
	"completed":   true,
	"failed":      true,
	"stopped":     true,
	"killed":      true,
	"canceled":    true,
	"cancelled":   true,
	"timed_out":   true,
	"timeout":     true,
	"error":       true,
	"aborted":     true,
	"interrupted": true,
}

// scanRunningAgents 按 sid 定位主 jsonl(复用 transcriptPath),扫出运行中子代理,
// 按 StartedAt 升序排序。坏行跳过。
func scanRunningAgents(sid string) ([]runningAgent, error) {
	path := transcriptPath(sid)
	if path == "" {
		return nil, os.ErrNotExist
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return scanRunningAgentsFrom(f)
}

// scanRunningAgentsFrom 从 reader 逐行扫描(便于夹具测试)。
// 无上限逐行(jsonlLines):超大行(工具结果/快照)不再被 bufio.Scanner 静默截断、吞掉其后所有行。
func scanRunningAgentsFrom(r io.Reader) ([]runningAgent, error) {
	var lines []string
	for line := range jsonlLines(r) {
		lines = append(lines, string(line))
	}
	return scanRunningAgentsFromLines(lines), nil
}

// subAgentReg 是一条登记的 Agent/Task/Workflow 启动记录。
type subAgentReg struct {
	agentType   string
	description string
	startedAt   string
	kind        string // ""/"agent" | "workflow"
	runID       string // 仅 workflow:从 async_launched 行补全
}

// scanRunningAgentsFromLines 是扫描核心(纯逻辑,可测)。坏行(json 解析失败)continue。
func scanRunningAgentsFromLines(lines []string) []runningAgent {
	agents := map[string]subAgentReg{} // toolUseId -> 登记信息(保留登记顺序无关,排序在最后)
	order := []string{}                // 登记顺序(用于稳定输出)
	done := map[string]bool{}          // 已出现终止信号的 toolUseId

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev subAgentEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue // 坏行跳过
		}

		// 1. 登记 Agent/Task/Workflow 启动。Workflow 标 kind=workflow(其 runId/描述靠 async_launched 行补)。
		if ev.Type == "assistant" && ev.Message != nil {
			for _, b := range parseContentBlocks(ev.Message.Content) {
				if b.Type != "tool_use" {
					continue
				}
				isWF := b.Name == "Workflow"
				if !isWF && b.Name != "Agent" && b.Name != "Task" {
					continue
				}
				if b.ID == "" {
					continue
				}
				if _, seen := agents[b.ID]; !seen {
					order = append(order, b.ID)
				}
				reg := subAgentReg{
					agentType:   subagentTypeOf(b.Input),
					description: stringField(b.Input, "description"),
					startedAt:   ev.Timestamp,
				}
				if isWF {
					reg.kind = "workflow"
				}
				agents[b.ID] = reg
			}
		}

		// 2. 后台 agent 完成信号:task-notification。它可能出现在多个通道(queue-operation 顶层 content、
		//    user 的 message.content 字符串、attachment.prompt、甚至内容块字段),且一行可能含多条。
		//    故直接对整行原文逐「块」扫描,块内配对 tool-use-id + status(避免跨通报错配)。
		//    只扫非 assistant 行:assistant 是代理自身输出,避免其正文里引用的通报文本造成误判。
		if ev.Type != "assistant" {
			for _, blk := range tnBlockRe.FindAllStringSubmatch(line, -1) {
				inner := blk[1]
				tid := firstSubmatch(tnToolUseRe, inner)
				st := firstSubmatch(tnStatusRe, inner)
				if tid != "" && terminalStatuses[st] {
					done[tid] = true
				}
			}
		}

		// 3. tool_result 行(type=user):
		//   - status==async_launched:不是完成;但若是已登记的 workflow,从顶层 toolUseResult 补 runId/summary。
		//   - status!=async_launched:真实结果 → 该 tool_use 完成。
		if ev.Type == "user" && ev.Message != nil {
			res := ev.toolUseResult()
			if res.Status == "async_launched" {
				if res.RunID != "" {
					for _, b := range parseContentBlocks(ev.Message.Content) {
						if b.Type != "tool_result" || b.ToolUseID == "" {
							continue
						}
						if reg, ok := agents[b.ToolUseID]; ok && reg.kind == "workflow" {
							reg.runID = res.RunID
							if reg.description == "" {
								reg.description = res.Summary
							}
							agents[b.ToolUseID] = reg
						}
					}
				}
			} else {
				for _, b := range parseContentBlocks(ev.Message.Content) {
					if b.Type == "tool_result" && b.ToolUseID != "" {
						done[b.ToolUseID] = true
					}
				}
			}
		}
	}

	// 4. 运行中 = 登记 - 终止。
	out := make([]runningAgent, 0, len(order))
	for _, id := range order {
		if done[id] {
			continue
		}
		reg := agents[id]
		out = append(out, runningAgent{
			ToolUseID:   id,
			AgentType:   reg.agentType,
			Description: reg.description,
			StartedAt:   reg.startedAt,
			Kind:        reg.kind,
			RunID:       reg.runID,
		})
	}
	// 按 StartedAt 升序(空串排在前;同值保持登记顺序稳定)。
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out
}

// subAgentEvent 是 /subagents 扫描需要的字段子集。
// 注意:user 行的 toolUseResult 在顶层(非 message 内)。完成信号(task-notification)不在此解析——
// 它通道多(顶层 content / message.content / attachment / 内容块),改由扫描器对整行原文直接块扫描,
// 故这里不再声明顶层 content 字段(避免它在某些行是数组/对象时让整行解析失败而丢行)。
type subAgentEvent struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

// tuResult 是顶层 toolUseResult 里我们关心的字段(status 判完成;runId/summary 供 workflow 补全)。
type tuResult struct {
	Status  string `json:"status"`
	RunID   string `json:"runId"`
	Summary string `json:"summary"`
}

// toolUseResult 解析顶层 toolUseResult(无/非对象时返回零值)。
func (e subAgentEvent) toolUseResult() tuResult {
	var m tuResult
	if len(e.ToolUseResult) == 0 {
		return m
	}
	_ = json.Unmarshal(e.ToolUseResult, &m)
	return m
}

// contentBlock 是 message.content[] 里我们关心的块字段。
type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`          // tool_use
	Name      string          `json:"name"`        // tool_use
	Input     json.RawMessage `json:"input"`       // tool_use
	ToolUseID string          `json:"tool_use_id"` // tool_result
}

// parseContentBlocks 把 message.content 解析为块数组(字符串型 content 返回 nil)。
func parseContentBlocks(raw json.RawMessage) []contentBlock {
	if len(raw) == 0 {
		return nil
	}
	var arr []contentBlock
	if json.Unmarshal(raw, &arr) != nil {
		return nil
	}
	return arr
}

// subagentTypeOf 从 tool_use.input 取 subagent_type(缺则空)。
func subagentTypeOf(input json.RawMessage) string {
	return stringField(input, "subagent_type")
}

// stringField 从原始 JSON 对象里取某字符串字段(缺/类型不符则空)。
func stringField(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var s string
	if v, ok := m[key]; ok && json.Unmarshal(v, &s) == nil {
		return s
	}
	return ""
}

// firstSubmatch 返回正则第一个捕获组(无匹配返回空串)。
func firstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// handleSubagents — GET /subagents?sid=<sid> → 运行中子代理列表。
func handleSubagents(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	if strings.TrimSpace(sid) == "" {
		ctrlErr(w, 400, "sid required")
		return
	}
	list, err := scanRunningAgents(sid)
	if err != nil {
		if os.IsNotExist(err) {
			ctrlErr(w, 404, "session not found")
			return
		}
		ctrlErr(w, 500, err.Error())
		return
	}
	ctrlJSON(w, 200, list)
}
