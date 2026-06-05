package control

// config.go — 包级配置:Claude projects 目录的可覆盖来源。
//
// 来源解析策略(默认 ~/.claude/projects,可显式覆盖):
//   1. SetClaudeDir(...) 显式注入(--claude-dir / CCFLY_CLAUDE_DIR 在 main 里解析后调用);
//   2. 未显式注入时,运行期兜底读 env CCFLY_CLAUDE_DIR;
//   3. 仍未指定 → ~/.claude/projects。
//
// 安全模型:本服务自身不做鉴权,默认只绑回环(见 Serve / cmd/ccfly)。任何对外暴露
// 都应交由上游反向代理 / 消费方在前面统一把关(对齐 ttyd 的「绑回环 + 反代鉴权」)。

import (
	"os"
	"path/filepath"
)

// claudeDirOverride 由 SetClaudeDir 设置(空 = 未显式注入,运行期回落 env/默认)。
var claudeDirOverride string

// SetClaudeDir 显式注入 Claude projects 目录(main 解析 --claude-dir/env 后调用)。
// 传空串表示「清除注入,回落 env/默认」。
func SetClaudeDir(dir string) { claudeDirOverride = dir }

// claudeProjectsDir 返回 Claude 会话 jsonl 的根目录 <claudeDir>/...(每会话一文件)。
// 优先级:显式注入 > env CCFLY_CLAUDE_DIR > ~/.claude/projects。
func claudeProjectsDir() string {
	if claudeDirOverride != "" {
		return claudeDirOverride
	}
	if d := os.Getenv("CCFLY_CLAUDE_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}
