package control

// realfixture_test.go — 真样发现助手(脱敏)。
//
// 早期版本把某台开发机的绝对路径 + 真实 session id 写死在测试里。公开仓不允许残留
// 任何 PII / 本机路径,所以改成:从 os.UserHomeDir() 派生通用的 ~/.claude/projects,
// 在其中发现「任意一个本地 claude 会话」。若机器上没有任何本地会话(CI / 公开环境),
// 相关「真样」用例一律 t.Skip,而不是 Fail。
//
// 也支持用 CCFLY_TEST_CLAUDE_DIR 覆盖根目录(便于在自定义目录里放夹具跑回归)。

import (
	"os"
	"path/filepath"
	"strings"
)

// realClaudeProjectsRoot 返回本地 claude 会话根目录(~/.claude/projects 或 env 覆盖)。
func realClaudeProjectsRoot() string {
	if d := os.Getenv("CCFLY_TEST_CLAUDE_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// discoverRealSession 在本地 claude projects 里找任意一个会话,返回
//   projectDir = <root>/<projectSlug>(会话目录的父目录)
//   sid        = 该会话的文件名(去 .jsonl)
// 若没有任何本地会话,返回 ("", "")——调用方据此 t.Skip。
func discoverRealSession() (projectDir, sid string) {
	root := realClaudeProjectsRoot()
	if root == "" {
		return "", ""
	}
	projects, err := os.ReadDir(root)
	if err != nil {
		return "", ""
	}
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		pdir := filepath.Join(root, p.Name())
		files, err := os.ReadDir(pdir)
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if f.IsDir() || !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			return pdir, strings.TrimSuffix(name, ".jsonl")
		}
	}
	return "", ""
}

// discoverRealWorkflow 在已发现的会话里找任意一个 workflow 摘要,返回其 runId(去 .json)。
// 找不到返回 ""(无 workflow 的会话不能跑 workflow 真样用例)。
func discoverRealWorkflow(projectDir, sid string) (runID string) {
	dir := filepath.Join(projectDir, sid, "workflows")
	files, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || !strings.HasPrefix(name, "wf_") || !strings.HasSuffix(name, ".json") {
			continue
		}
		return strings.TrimSuffix(name, ".json")
	}
	return ""
}

// discoverWorkflowAgent 在某 workflow 的 subagents 目录里找任意一个 agent transcript,返回 agentId。
// 文件名形如 agent-<agentId>.jsonl。找不到返回 ""。
func discoverWorkflowAgent(projectDir, sid, runID string) (agentID string) {
	dir := filepath.Join(projectDir, sid, "subagents", "workflows", runID)
	files, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".jsonl")
		if id != "" {
			return id
		}
	}
	return ""
}
