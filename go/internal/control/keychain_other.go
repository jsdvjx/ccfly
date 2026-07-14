//go:build !darwin

package control

// 非 macOS 平台:claude-code 直接以 ~/.claude/.credentials.json 文件为凭据主源(无登录钥匙串遮蔽),
// ccfly 下发写文件即生效,无需 seed。wrapClaudeCmd 原样返回 claude 启动命令。
func wrapClaudeCmd(cmd string) string { return cmd }
