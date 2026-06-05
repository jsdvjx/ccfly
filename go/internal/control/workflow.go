package control

// workflow.go — 「一次 Workflow 执行」的只读渲染后端。
//
// 磁盘真相(以真样确认,projectDir = <claudeProjectsDir>/<projectSlug>):
//   - 摘要文件(完整真相):<projectDir>/<sid>/workflows/wf_<runId>.json
//     顶层字段:runId/workflowName/summary/status/startTime/durationMs/phases[]/
//     defaultModel/agentCount/totalTokens/totalToolCalls/workflowProgress[]
//     (workflowProgress 里 type==workflow_agent 的条目带 agentId/label/phaseIndex/
//      phaseTitle/model/state/startedAt/tokens/toolCalls/durationMs/lastToolSummary…)
//   - 各 agent transcript:<projectDir>/<sid>/subagents/workflows/wf_<runId>/agent-<agentId>.jsonl
//     + 同目录 journal.jsonl / agent-<agentId>.meta.json
//
// 设计:/workflow 做「薄聚合」——剥掉 script/scriptPath/logs/result、以及各 agent 的
// promptPreview/resultPreview/queuedAt/attempt 等大/无关字段,只回卡片所需。
// agent transcript 复用 readTranscriptSteps(同 /subtranscript 的字节游标语义)。
// 防穿越:runId/agentId 校验无斜杠 + filepath.Clean + 强制前缀落在该 sid 目录下。

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// wfSummary 是 /workflow 的薄聚合返回体(只含卡片所需,剥掉所有大字段)。
type wfSummary struct {
	Name           string    `json:"name"`
	Summary        string    `json:"summary"`
	Status         string    `json:"status"`
	DurationMs     int64     `json:"durationMs"`
	StartTime      int64     `json:"startTime"`
	DefaultModel   string    `json:"defaultModel"`
	AgentCount     int       `json:"agentCount"`
	TotalTokens    int64     `json:"totalTokens"`
	TotalToolCalls int64     `json:"totalToolCalls"`
	Phases         []wfPhase `json:"phases"`
	Agents         []wfAgent `json:"agents"`
}

type wfPhase struct {
	Index int    `json:"index"`
	Title string `json:"title"`
}

type wfAgent struct {
	AgentID         string `json:"agentId"`
	Label           string `json:"label"`
	PhaseIndex      int    `json:"phaseIndex"`
	PhaseTitle      string `json:"phaseTitle"`
	Model           string `json:"model"`
	State           string `json:"state"`
	Tokens          int64  `json:"tokens"`
	ToolCalls       int64  `json:"toolCalls"`
	DurationMs      int64  `json:"durationMs"`
	LastToolSummary string `json:"lastToolSummary"`
	StartedAt       int64  `json:"startedAt"`
}

// safeWorkflowDir 返回该 sid 的会话目录(<projectDir>/<sid>)并保证它在 projectDir 之下。
// 返回 "" 表示 sid 非法或会话不存在。
func sessionSubDir(sid string) string {
	if sid == "" || strings.ContainsAny(sid, "/\\") {
		return ""
	}
	main := transcriptPath(sid) // <projectDir>/<sid>.jsonl
	if main == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(main), sid)
}

// workflowSummaryPath 由 sid + runId 定位 wf_<runId>.json。
// runId 形如 "wf_0d0eaf7f-158"(= 文件名去 .json),校验无斜杠;filepath.Clean + 强制
// 落在 <projectDir>/<sid>/workflows/ 之下防穿越。找不到/越界返回 ""。
func workflowSummaryPath(sid, runId string) string {
	if runId == "" || strings.ContainsAny(runId, "/\\") {
		return ""
	}
	subDir := sessionSubDir(sid)
	if subDir == "" {
		return ""
	}
	base := filepath.Clean(filepath.Join(subDir, "workflows")) + string(os.PathSeparator)
	p := filepath.Clean(filepath.Join(subDir, "workflows", runId+".json"))
	if !strings.HasPrefix(p, base) { // 防穿越:必须严格落在 workflows/ 之下
		return ""
	}
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return ""
	}
	return p
}

// workflowAgentPath 由 sid + runId + agentId 定位 agent transcript。
// 路径:<projectDir>/<sid>/subagents/workflows/wf_<runId>/agent-<agentId>.jsonl。
// runId、agentId 均校验无斜杠;filepath.Clean + 强制前缀防穿越。找不到/越界返回 ""。
func workflowAgentPath(sid, runId, agentId string) string {
	if runId == "" || strings.ContainsAny(runId, "/\\") {
		return ""
	}
	if agentId == "" || strings.ContainsAny(agentId, "/\\") {
		return ""
	}
	subDir := sessionSubDir(sid)
	if subDir == "" {
		return ""
	}
	wfDir := filepath.Clean(filepath.Join(subDir, "subagents", "workflows", runId))
	base := filepath.Clean(filepath.Join(subDir, "subagents", "workflows")) + string(os.PathSeparator)
	if !strings.HasPrefix(wfDir+string(os.PathSeparator), base) {
		return ""
	}
	p := filepath.Clean(filepath.Join(wfDir, "agent-"+agentId+".jsonl"))
	if !strings.HasPrefix(p, wfDir+string(os.PathSeparator)) { // 防穿越
		return ""
	}
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return ""
	}
	return p
}

// workflowRunIdByToolUse 兜底:仅给 toolUseId 时,扫主 jsonl 找该 tool_use 的
// async_launched 行(顶层 toolUseResult.status=="async_launched"),取 toolUseResult.runId。
// runId == 文件名(wf_<id>)。找不到返回 ""。
func workflowRunIdByToolUse(sid, toolUseId string) string {
	if toolUseId == "" || strings.ContainsAny(toolUseId, "/\\") {
		return ""
	}
	path := transcriptPath(sid)
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		chunk, err := r.ReadBytes('\n')
		if len(chunk) > 0 {
			if rid := workflowRunIdFromLine(chunk, toolUseId); rid != "" {
				return rid
			}
		}
		if err != nil {
			break
		}
	}
	return ""
}

// workflowRunIdFromLine 判定一行是否为某 toolUseId 的 workflow async_launched 结果,是则返回 runId。
func workflowRunIdFromLine(line []byte, toolUseId string) string {
	var ev struct {
		ToolUseResult *struct {
			Status string `json:"status"`
			RunID  string `json:"runId"`
		} `json:"toolUseResult"`
		Message *struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.ToolUseResult == nil || ev.Message == nil {
		return ""
	}
	if ev.ToolUseResult.Status != "async_launched" || ev.ToolUseResult.RunID == "" {
		return ""
	}
	// 校验该 tool_result 块的 tool_use_id 匹配。
	var arr []struct {
		Type      string `json:"type"`
		ToolUseID string `json:"tool_use_id"`
	}
	if json.Unmarshal(ev.Message.Content, &arr) != nil {
		return ""
	}
	for _, b := range arr {
		if b.Type == "tool_result" && b.ToolUseID == toolUseId {
			return ev.ToolUseResult.RunID
		}
	}
	return ""
}

// readWorkflowSummary 读 wf_<runId>.json 并薄聚合成 wfSummary(剥掉所有大字段)。
func readWorkflowSummary(path string) (wfSummary, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return wfSummary{}, false
	}
	// 只解析需要的字段;workflowProgress 用最小结构,丢弃 promptPreview/resultPreview 等。
	var doc struct {
		WorkflowName   string `json:"workflowName"`
		Summary        string `json:"summary"`
		Status         string `json:"status"`
		DurationMs     int64  `json:"durationMs"`
		StartTime      int64  `json:"startTime"`
		DefaultModel   string `json:"defaultModel"`
		AgentCount     int    `json:"agentCount"`
		TotalTokens    int64  `json:"totalTokens"`
		TotalToolCalls int64  `json:"totalToolCalls"`
		Phases         []struct {
			Title string `json:"title"`
		} `json:"phases"`
		WorkflowProgress []struct {
			Type            string `json:"type"`
			AgentID         string `json:"agentId"`
			Label           string `json:"label"`
			PhaseIndex      int    `json:"phaseIndex"`
			PhaseTitle      string `json:"phaseTitle"`
			Model           string `json:"model"`
			State           string `json:"state"`
			Tokens          int64  `json:"tokens"`
			ToolCalls       int64  `json:"toolCalls"`
			DurationMs      int64  `json:"durationMs"`
			LastToolSummary string `json:"lastToolSummary"`
			StartedAt       int64  `json:"startedAt"`
		} `json:"workflowProgress"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return wfSummary{}, false
	}
	out := wfSummary{
		Name:           doc.WorkflowName,
		Summary:        doc.Summary,
		Status:         doc.Status,
		DurationMs:     doc.DurationMs,
		StartTime:      doc.StartTime,
		DefaultModel:   doc.DefaultModel,
		AgentCount:     doc.AgentCount,
		TotalTokens:    doc.TotalTokens,
		TotalToolCalls: doc.TotalToolCalls,
		Phases:         make([]wfPhase, 0, len(doc.Phases)),
		Agents:         make([]wfAgent, 0),
	}
	// phases:index 从 1 起(与 workflow_agent.phaseIndex 对齐,真样里 phaseIndex 从 1)。
	for i, p := range doc.Phases {
		out.Phases = append(out.Phases, wfPhase{Index: i + 1, Title: p.Title})
	}
	for _, e := range doc.WorkflowProgress {
		if e.Type != "workflow_agent" {
			continue
		}
		out.Agents = append(out.Agents, wfAgent{
			AgentID:         e.AgentID,
			Label:           e.Label,
			PhaseIndex:      e.PhaseIndex,
			PhaseTitle:      e.PhaseTitle,
			Model:           e.Model,
			State:           e.State,
			Tokens:          e.Tokens,
			ToolCalls:       e.ToolCalls,
			DurationMs:      e.DurationMs,
			LastToolSummary: e.LastToolSummary,
			StartedAt:       e.StartedAt,
		})
	}
	return out, true
}
