package control

import (
	"os"
	"path/filepath"
	"testing"
)

// 这些「真样」用例对任意一个本地 claude 会话生效:发现不到会话 / workflow 时 t.Skip。
// 不写死任何机器路径或 session id(见 realfixture_test.go 的发现助手)。

// TestReadWorkflowSummaryReal:对本地任意 wf_*.json 跑薄聚合解析,断言结构完整性
// (字段非空、phases/agents 自洽),不假设具体 workflow 名/阶段数。
func TestReadWorkflowSummaryReal(t *testing.T) {
	projectDir, sid := discoverRealSession()
	if projectDir == "" {
		t.Skip("requires a local claude session")
	}
	runID := discoverRealWorkflow(projectDir, sid)
	if runID == "" {
		t.Skip("local claude session has no workflow run")
	}
	path := filepath.Join(projectDir, sid, "workflows", runID+".json")
	sum, ok := readWorkflowSummary(path)
	if !ok {
		t.Fatalf("readWorkflowSummary 失败: %s", path)
	}
	if sum.Name == "" || sum.Status == "" {
		t.Errorf("基础字段缺失: name=%q status=%q", sum.Name, sum.Status)
	}
	if sum.StartTime == 0 {
		t.Errorf("startTime 缺失")
	}
	// phases:若存在,index 从 1 递增、title 非空。
	for i, p := range sum.Phases {
		if p.Index != i+1 {
			t.Errorf("phase[%d] index 应为 %d,实际 %d", i, i+1, p.Index)
		}
		if p.Title == "" {
			t.Errorf("phase[%d] title 为空", i)
		}
	}
	// agents:若存在,关键字段非空。
	for _, a := range sum.Agents {
		if a.AgentID == "" || a.Label == "" || a.PhaseIndex == 0 || a.State == "" {
			t.Errorf("agent 关键字段缺失: %+v", a)
		}
	}
	t.Logf("workflow %q: status=%q phases=%d agents=%d", sum.Name, sum.Status, len(sum.Phases), len(sum.Agents))
}

// TestWorkflowAgentPathReal:对本地任意 workflow 的某 agentId 跑路径定位 + 复用
// readTranscriptSteps 读出 items。
func TestWorkflowAgentPathReal(t *testing.T) {
	projectDir, sid := discoverRealSession()
	if projectDir == "" {
		t.Skip("requires a local claude session")
	}
	runID := discoverRealWorkflow(projectDir, sid)
	if runID == "" {
		t.Skip("local claude session has no workflow run")
	}
	agentID := discoverWorkflowAgent(projectDir, sid, runID)
	if agentID == "" {
		t.Skip("workflow run has no agent transcript")
	}
	t.Setenv("CCFLY_CLAUDE_DIR", filepath.Dir(projectDir))
	path := workflowAgentPath(sid, runID, agentID)
	if path == "" {
		t.Fatalf("workflowAgentPath 未定位到 agent transcript")
	}
	if filepath.Base(path) != "agent-"+agentID+".jsonl" {
		t.Errorf("路径文件名不符: %s", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("定位路径不可读: %s (%v)", path, err)
	}
	steps, cursor, err := readTranscriptSteps(path, 0)
	if err != nil {
		t.Fatalf("读 agent transcript 报错: %v", err)
	}
	if len(steps) == 0 || cursor == 0 {
		t.Fatalf("agent transcript 应非空,steps=%d cursor=%d", len(steps), cursor)
	}
	t.Logf("agent %s: %d items, cursor=%d", agentID, len(steps), cursor)
}

// TestWorkflowPathTraversal:runId/agentId 含斜杠或 .. 必须被拒(防穿越)。
// 这是纯逻辑用例;为让 sessionSubDir 能解析,指向本地会话(无则 Skip)。
func TestWorkflowPathTraversal(t *testing.T) {
	projectDir, sid := discoverRealSession()
	if projectDir == "" {
		t.Skip("requires a local claude session")
	}
	t.Setenv("CCFLY_CLAUDE_DIR", filepath.Dir(projectDir))
	const goodRun = "wf_0d0eaf7f-158" // 仅作「合法形态」对照,不要求其存在
	bad := []struct{ runId, agentId string }{
		{"../../etc/passwd", "x"},
		{"wf_x/..", "x"},
		{goodRun, "../../../etc/passwd"},
		{goodRun, "a/b"},
		{"", "x"},
		{goodRun, ""},
	}
	for _, c := range bad {
		if p := workflowAgentPath(sid, c.runId, c.agentId); p != "" {
			t.Errorf("应拒绝穿越 runId=%q agentId=%q,却返回 %q", c.runId, c.agentId, p)
		}
	}
	// summary 端单独验斜杠 runId 被拒。
	for _, rid := range []string{"../foo", "wf/..", "a/b", ""} {
		if p := workflowSummaryPath(sid, rid); p != "" {
			t.Errorf("summary 应拒绝穿越 runId=%q,却返回 %q", rid, p)
		}
	}
}
