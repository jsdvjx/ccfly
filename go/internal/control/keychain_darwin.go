//go:build darwin

package control

import (
	"os"
	"path/filepath"
)

// keychain_darwin.go — macOS 上把 ccfly 下发的订阅凭据「播种」进登录钥匙串。
//
// 背景:claude-code 在 macOS 用**登录钥匙串**(generic-password,service "Claude Code-credentials")
// 作为 OAuth 凭据主源;`~/.claude/.credentials.json` 只是次要/回退路径,一旦钥匙串里已有条目(哪怕是
// 失效的旧 token)就把文件遮蔽掉。ccfly 的凭据下发(claude_login.go writeCredentials)只写文件,于是在
// 「钥匙串里残留旧凭据」的 mac 上,claude 一直用旧 token → 401,反复下发也进不去。
//
// 修法:起 claude 前,在**同一个 tmux 上下文**(登录钥匙串已解锁、与 claude 读取上下文一致)执行 `security`
// 把订阅凭据写进钥匙串,claude 遂以原生订阅登录——零迂回、零 env 覆盖。要点:
//   - 门控:仅当 ~/.ccfly/keychain-seed-pending 存在(ccfly 刚下发过凭据的一次性标记,见 writeCredentials)
//     才 seed,seed 成功即删标记。之后 claude 自己用 refreshToken 续期钥匙串,ccfly 不再覆盖 —— 既不踩
//     claude 的 refresh,也绝不动用户自登录(无标记)的机器。
//   - -A:让写入的条目对所有程序免授权窗读取。否则 claude(非条目创建者)读会触发 macOS SecurityAgent 的
//     GUI 授权窗,无人值守设备无人可点 → 卡死。凭据本就在 0600 明文文件里,-A 不额外降低安全面。
//   - -X:以 hex 传 JSON,回避引号/转义;凭据明文不出现在 tmux/ccfly 的命令行(hex 在 shell 变量里)。
//   - delete + add(而非 -U):新建条目不涉及「修改已存在条目」的授权判定,最大化无弹窗成功率。
//
// 全程无 GUI 交互;失败均静默(seed 失败不该挡住会话启动,claude 照常起、退回原有行为)。

// wrapClaudeCmd 给 tmux 要执行的 claude 命令串前置 seed 逻辑。仅当存在「待 seed」标记时才包裹,
// 否则原样返回(本机/用户自登录设备完全不受影响)。cmd 为空(无命令)时不处理。
func wrapClaudeCmd(cmd string) string {
	if cmd == "" {
		return cmd
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return cmd
	}
	if _, err := os.Stat(filepath.Join(home, ".ccfly", "keychain-seed-pending")); err != nil {
		return cmd // 无待 seed 标记 → 原样起 claude
	}
	const seed = `F="$HOME/.ccfly/keychain-seed-pending"; C="$HOME/.claude/.credentials.json"; ` +
		`if [ -f "$F" ] && [ -f "$C" ]; then H=$(/usr/bin/xxd -p "$C" 2>/dev/null | tr -d '\n'); ` +
		`if [ -n "$H" ]; then ` +
		`/usr/bin/security delete-generic-password -s "Claude Code-credentials" >/dev/null 2>&1; ` +
		`/usr/bin/security add-generic-password -A -a "$(/usr/bin/id -un)" -s "Claude Code-credentials" -X "$H" >/dev/null 2>&1 && rm -f "$F"; ` +
		`fi; fi; `
	return seed + "exec " + cmd
}
