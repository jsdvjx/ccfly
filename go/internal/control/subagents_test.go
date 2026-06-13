package control

import (
	"os"
	"path/filepath"
	"testing"
)

// fixtureLines 构造一个小内联 jsonl 夹具,覆盖三类 agent:
//   - A1(后台):tool_use + async_launched tool_result + completed task-notification → 已完成
//   - A2(后台):tool_use + async_launched tool_result,无 completed 通知 → 运行中
//   - A3(同步):tool_use,无任何 result → 运行中
func fixtureLines() []string {
	return []string{
		// A1 启动(后台)
		`{"type":"assistant","timestamp":"2026-01-01T00:00:01.000Z","message":{"content":[{"type":"tool_use","id":"tA1","name":"Task","input":{"subagent_type":"general-purpose","description":"bg done","run_in_background":true}}]}}`,
		// A1 的 async_launched tool_result(不算完成)
		`{"type":"user","timestamp":"2026-01-01T00:00:01.003Z","toolUseResult":{"status":"async_launched","taskId":"x1"},"message":{"content":[{"type":"tool_result","tool_use_id":"tA1"}]}}`,
		// A2 启动(后台)
		`{"type":"assistant","timestamp":"2026-01-01T00:00:02.000Z","message":{"content":[{"type":"tool_use","id":"tA2","name":"Agent","input":{"subagent_type":"explorer","description":"bg still running","run_in_background":true}}]}}`,
		// A2 的 async_launched tool_result(不算完成)
		`{"type":"user","timestamp":"2026-01-01T00:00:02.003Z","toolUseResult":{"status":"async_launched","taskId":"x2"},"message":{"content":[{"type":"tool_result","tool_use_id":"tA2"}]}}`,
		// A3 启动(同步,无 run_in_background)
		`{"type":"assistant","timestamp":"2026-01-01T00:00:03.000Z","message":{"content":[{"type":"tool_use","id":"tA3","name":"Task","input":{"subagent_type":"general-purpose","description":"sync running"}}]}}`,
		// A1 的 completed task-notification(顶层 content,queue-operation)→ 标记 A1 完成
		`{"type":"queue-operation","operation":"task-notification","timestamp":"2026-01-01T00:00:05.000Z","content":"<task-notification>\n<task-id>q1</task-id>\n<tool-use-id>tA1</tool-use-id>\n<status>completed</status>\n<summary>done</summary>\n</task-notification>"}`,
		// 一条坏行(json 解析失败)应被跳过
		`{not valid json at all`,
	}
}

func TestScanRunningAgentsFromLines(t *testing.T) {
	got := scanRunningAgentsFromLines(fixtureLines())

	running := map[string]runningAgent{}
	for _, a := range got {
		running[a.ToolUseID] = a
	}

	if _, ok := running["tA1"]; ok {
		t.Errorf("tA1 应已完成(有 completed task-notification),却出现在运行中集合")
	}
	if _, ok := running["tA2"]; !ok {
		t.Errorf("tA2 应运行中(后台,无 completed 通知),却不在集合")
	}
	if _, ok := running["tA3"]; !ok {
		t.Errorf("tA3 应运行中(同步,无 result),却不在集合")
	}
	if len(got) != 2 {
		t.Fatalf("期望运行中 2 个,实际 %d: %+v", len(got), got)
	}

	// 字段透传校验。
	if a := running["tA2"]; a.AgentType != "explorer" || a.Description != "bg still running" || a.StartedAt != "2026-01-01T00:00:02.000Z" {
		t.Errorf("tA2 字段不符: %+v", a)
	}
	if a := running["tA3"]; a.AgentType != "general-purpose" || a.Description != "sync running" {
		t.Errorf("tA3 字段不符: %+v", a)
	}

	// 升序排序:tA2(00:02)应在 tA3(00:03)前。
	if got[0].ToolUseID != "tA2" || got[1].ToolUseID != "tA3" {
		t.Errorf("StartedAt 升序排序错误: %+v", got)
	}
}

// TestScanRunningAgentsSyncCompleted:同步 agent 出现真实 tool_result(status!=async_launched)→ 完成。
func TestScanRunningAgentsSyncCompleted(t *testing.T) {
	lines := []string{
		`{"type":"assistant","timestamp":"2026-01-01T00:00:01.000Z","message":{"content":[{"type":"tool_use","id":"tS","name":"Task","input":{"subagent_type":"general-purpose","description":"sync"}}]}}`,
		`{"type":"user","timestamp":"2026-01-01T00:00:09.000Z","toolUseResult":{"status":"completed","agentId":"a1"},"message":{"content":[{"type":"tool_result","tool_use_id":"tS"}]}}`,
	}
	got := scanRunningAgentsFromLines(lines)
	if len(got) != 0 {
		t.Fatalf("同步 agent 有真实 tool_result 应判定完成,期望 0,实际 %d: %+v", len(got), got)
	}
}

// TestScanRunningAgentsTerminalStatuses:后台 agent 的 task-notification 用各种终止态拼写
// (尤其 JS 系双 l 的 "cancelled"、timed_out、error 等)都应判定完成,不再卡在「运行中」。
// 这是「已完成却仍显运行」(AgentDock 一直「N 个运行中」)误报的回归护栏。
func TestScanRunningAgentsTerminalStatuses(t *testing.T) {
	for _, status := range []string{"cancelled", "canceled", "timed_out", "timeout", "error", "aborted", "interrupted", "failed", "stopped", "killed"} {
		t.Run(status, func(t *testing.T) {
			lines := []string{
				`{"type":"assistant","timestamp":"2026-01-01T00:00:01.000Z","message":{"content":[{"type":"tool_use","id":"tB","name":"Task","input":{"subagent_type":"explorer","description":"bg","run_in_background":true}}]}}`,
				`{"type":"user","timestamp":"2026-01-01T00:00:01.003Z","toolUseResult":{"status":"async_launched","taskId":"x"},"message":{"content":[{"type":"tool_result","tool_use_id":"tB"}]}}`,
				`{"type":"queue-operation","operation":"task-notification","timestamp":"2026-01-01T00:00:05.000Z","content":"<task-notification>\n<tool-use-id>tB</tool-use-id>\n<status>` + status + `</status>\n</task-notification>"}`,
			}
			got := scanRunningAgentsFromLines(lines)
			if len(got) != 0 {
				t.Fatalf("status=%q 应判完成,期望运行中 0,实际 %d: %+v", status, len(got), got)
			}
		})
	}
}

// TestScanRunningWorkflow:Workflow 的 tool_use 登记为 kind=workflow,从 async_launched 行补 runId/summary。
//   - W1:tool_use + async_launched(补 runId),无 completed → 运行中,kind=workflow,runId 填好。
//   - W2:tool_use + async_launched + completed task-notification → 已完成,不出现在运行中。
func TestScanRunningWorkflow(t *testing.T) {
	lines := []string{
		// W1 启动(Workflow tool_use,input 只有 script)
		`{"type":"assistant","timestamp":"2026-01-01T00:00:01.000Z","message":{"content":[{"type":"tool_use","id":"tW1","name":"Workflow","input":{"script":"export const x=1"}}]}}`,
		// W1 的 async_launched(顶层 toolUseResult 带 runId/summary)→ 补全,不算完成
		`{"type":"user","timestamp":"2026-01-01T00:00:01.003Z","toolUseResult":{"status":"async_launched","runId":"wf_abc-123","summary":"build the thing"},"message":{"content":[{"type":"tool_result","tool_use_id":"tW1"}]}}`,
		// W2 启动
		`{"type":"assistant","timestamp":"2026-01-01T00:00:02.000Z","message":{"content":[{"type":"tool_use","id":"tW2","name":"Workflow","input":{"script":"export const y=2"}}]}}`,
		// W2 的 async_launched
		`{"type":"user","timestamp":"2026-01-01T00:00:02.003Z","toolUseResult":{"status":"async_launched","runId":"wf_def-456","summary":"verify the thing"},"message":{"content":[{"type":"tool_result","tool_use_id":"tW2"}]}}`,
		// W2 的 completed task-notification → 标记完成
		`{"type":"queue-operation","operation":"task-notification","timestamp":"2026-01-01T00:00:05.000Z","content":"<task-notification>\n<tool-use-id>tW2</tool-use-id>\n<status>completed</status>\n</task-notification>"}`,
	}
	got := scanRunningAgentsFromLines(lines)
	if len(got) != 1 {
		t.Fatalf("期望运行中 1 个 workflow,实际 %d: %+v", len(got), got)
	}
	w := got[0]
	if w.ToolUseID != "tW1" {
		t.Errorf("期望运行中是 tW1,实际 %q", w.ToolUseID)
	}
	if w.Kind != "workflow" {
		t.Errorf("W1 kind 应为 workflow,实际 %q", w.Kind)
	}
	if w.RunID != "wf_abc-123" {
		t.Errorf("W1 runId 应从 async_launched 补 wf_abc-123,实际 %q", w.RunID)
	}
	if w.Description != "build the thing" {
		t.Errorf("W1 description 应取 async_launched summary,实际 %q", w.Description)
	}
}

// TestScanRunningAgentsRealFile:对本地任意 claude 会话 jsonl 跑(若存在),只断言不报错
// 并打印运行中数量。无本地会话则 Skip(不写死任何机器路径 / session id)。
func TestScanRunningAgentsRealFile(t *testing.T) {
	projectDir, sid := discoverRealSession()
	if projectDir == "" {
		t.Skip("requires a local claude session")
	}
	path := filepath.Join(projectDir, sid+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("会话文件不可读,跳过: %v", err)
	}
	defer f.Close()
	got, err := scanRunningAgentsFrom(f)
	if err != nil {
		t.Fatalf("扫真实文件报错: %v", err)
	}
	t.Logf("真实文件运行中子代理: %d 个", len(got))
	for _, a := range got {
		t.Logf("  RUNNING %s type=%q desc=%q at=%s", a.ToolUseID, a.AgentType, a.Description, a.StartedAt)
	}
}
