package control

// claudescan.go — 扫描本地 Claude 会话 jsonl 派生进度快照,以及被 transcript / info /
// subagents / workflow 共用的底层解析(rawEvent / contentKindPreview / scanOneSession 等)。
//
// 纯本地实现:不向任何云端上报,只做本地扫描 + 共享解析原语。
// claudeProjectsDir 在 config.go(可被 --claude-dir / CCFLY_CLAUDE_DIR 覆盖)。

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// claudeSnapshot 是单个 Claude 会话的进度快照(本地扫描用)。
type claudeSnapshot struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	GitBranch string `json:"git_branch,omitempty"`
	Title     string `json:"title,omitempty"` // aiTitle
	MsgCount  int    `json:"msg_count"`
	Turns     int    `json:"turns"`           // 用户提问轮数(排除工具结果回传)
	Tokens    int    `json:"tokens"`          // 当前上下文占用(最后一条 assistant 的 input+cache)
	Model     string `json:"model,omitempty"` // opus / sonnet / haiku
	LastTs    string `json:"last_ts"`
	LastRole  string `json:"last_role"` // user / assistant
	LastKind  string `json:"last_kind"` // text / tool_use / tool_result
	Preview   string `json:"preview"`
	State     string `json:"state"`   // working / awaiting_input / idle / error / closed / unknown
	AgeSec    int64  `json:"age_sec"` // 距最后活动秒数
}

// scanClaudeSessions 扫描 <claudeProjectsDir>/**/*.jsonl,每个会话出一个快照。
func scanClaudeSessions() ([]claudeSnapshot, error) {
	root := claudeProjectsDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := []claudeSnapshot{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pdir := filepath.Join(root, e.Name())
		files, _ := os.ReadDir(pdir)
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			if snap, ok := scanOneSession(filepath.Join(pdir, f.Name())); ok {
				out = append(out, snap)
			}
		}
	}
	return out, nil
}

type rawEvent struct {
	Type      string `json:"type"`
	UUID      string `json:"uuid"`
	SessionID string `json:"sessionId"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
	GitBranch string `json:"gitBranch"`
	AiTitle   string `json:"aiTitle"`
	// Edit/MultiEdit 的 tool_result 行在**顶层**带 toolUseResult(非 message 内),其中
	// structuredPatch 是含上下文行的标准 hunk 数组(与 TUI 同源)。transcript 透传给前端画 diff。
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	Message       *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Model   string          `json:"model"`
		Usage   *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func scanOneSession(path string) (claudeSnapshot, bool) {
	fh, err := os.Open(path)
	if err != nil {
		return claudeSnapshot{}, false
	}
	defer fh.Close()

	snap := claudeSnapshot{SessionID: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024) // 行可能很大(工具结果/快照)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev rawEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.SessionID != "" {
			snap.SessionID = ev.SessionID
		}
		// resume 按「第一个(原始)cwd」作用域:jsonl 落在 encode(初始 cwd) 的 project 目录下,
		// cwd 漂移过的会话若用「最后的 cwd」去 claude --resume 会报 "No conversation found"。故取第一个。
		if ev.Cwd != "" && snap.Cwd == "" {
			snap.Cwd = ev.Cwd
		}
		if ev.GitBranch != "" {
			snap.GitBranch = ev.GitBranch
		}
		if ev.Type == "ai-title" && ev.AiTitle != "" {
			snap.Title = ev.AiTitle
		}
		if ev.Message != nil && (ev.Type == "user" || ev.Type == "assistant") {
			snap.MsgCount++
			if ev.Timestamp != "" {
				snap.LastTs = ev.Timestamp
			}
			snap.LastRole = ev.Message.Role
			snap.LastKind, snap.Preview = contentKindPreview(ev.Message.Content)
			// 用户真·提问轮数:排除工具结果回传(role=user 但 content 是 tool_result)
			if ev.Type == "user" && snap.LastKind != "tool_result" {
				snap.Turns++
			}
			if ev.Type == "assistant" {
				if ev.Message.Model != "" {
					snap.Model = shortModel(ev.Message.Model)
				}
				if u := ev.Message.Usage; u != nil {
					// 当前上下文占用 ≈ 最后一条 assistant 的 input + 两类 cache(随会话推进取最新)
					snap.Tokens = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
				}
			}
		}
	}
	if snap.MsgCount == 0 {
		return snap, false
	}
	snap.AgeSec, snap.State = classify(snap.LastRole, snap.LastKind, snap.Preview, snap.LastTs)
	return snap, true
}

// contentKindPreview 解析 message.content(string 或 block 数组),返回 kind + 短预览。
func contentKindPreview(raw json.RawMessage) (kind, preview string) {
	if len(raw) == 0 {
		return "", ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return "text", trunc(s, 160)
	}
	var blocks []map[string]any
	if json.Unmarshal(raw, &blocks) == nil {
		kind = "text"
		var parts []string
		for _, b := range blocks {
			switch bt, _ := b["type"].(string); bt {
			case "tool_use":
				kind = "tool_use"
				if n, ok := b["name"].(string); ok {
					parts = append(parts, "tool:"+n)
				}
			case "tool_result":
				if kind != "tool_use" {
					kind = "tool_result"
				}
				parts = append(parts, "result")
			case "text":
				if t, ok := b["text"].(string); ok {
					parts = append(parts, t)
				}
			case "thinking":
				parts = append(parts, "(thinking)")
			}
		}
		return kind, trunc(strings.Join(parts, " | "), 160)
	}
	return "", ""
}

// classify 由"最后消息的角色/类型/内容 + 距今时长"推断会话状态。
// 状态:error / closed / awaiting_input / working / idle / unknown
func classify(role, kind, preview, lastTs string) (int64, string) {
	age := ageSeconds(lastTs)
	switch {
	case role == "assistant" && containsAny(preview, "API Error", "hit your limit", "Please run /login", "overloaded", "rate limit", "Request was aborted"):
		return age, "error"
	case role == "user" && containsAny(preview, "Bye!", "Goodbye!", "See ya!", "Catch you later!", "Until next time"):
		return age, "closed" // 用户 /bye 等主动退出(Claude Code 的告别语)
	case role == "assistant" && kind == "text":
		return age, "awaiting_input" // 助手给了文字回复 = 一轮结束,该你了
	case age >= 0 && age < 120:
		return age, "working"
	default:
		return age, "idle"
	}
}

// shortModel 把完整模型名简化成 opus/sonnet/haiku(展示用)。
func shortModel(m string) string {
	switch {
	case strings.Contains(m, "opus"):
		return "opus"
	case strings.Contains(m, "sonnet"):
		return "sonnet"
	case strings.Contains(m, "haiku"):
		return "haiku"
	}
	return m
}

func ageSeconds(lastTs string) int64 {
	t, err := time.Parse(time.RFC3339, lastTs)
	if err != nil {
		return -1
	}
	return int64(time.Since(t).Seconds())
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}
