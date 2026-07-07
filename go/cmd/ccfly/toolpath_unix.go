//go:build !windows

package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func ensureToolPath() {
	const fallbackExtras = "/opt/homebrew/bin:/usr/local/bin"
	current := os.Getenv("PATH")
	if current == "" {
		current = "/usr/bin:/bin:/usr/sbin:/sbin"
	}

	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}

	// 依次尝试:交互登录(-ilc,能 source .zshrc,有 tty 时最全)→ 登录非交互(-lc,launchd
	// 无控制终端下也跑得通)。旧版只试 -ilc:在 launchd(守护进程无 tty)下 -i 交互探测会失败,
	// 于是回落到只含 homebrew/系统的兜底 PATH —— 缺 ~/.local/bin,而 claude 官方原生安装器恰恰
	// 把二进制装在那里。结果:exec.LookPath("claude") 找不到 → /term·/start·/reload 的 --resume
	// 退化成裸壳;tmux new-session 直起的 `claude` 也秒退 → 新建会话「始终连接中」。
	for _, flags := range []string{"-ilc", "-lc"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, sh, flags, "echo $PATH")
		cmd.Stderr = nil
		out, err := cmd.Output()
		cancel()
		if err != nil {
			continue
		}
		loginPath := strings.TrimSpace(string(out))
		if !strings.Contains(loginPath, "/") {
			continue
		}
		resolved := ensureUserBinDirs(loginPath)
		os.Setenv("PATH", resolved)
		log.Printf("ccfly: PATH resolved from login shell %s (%d entries)", flags, strings.Count(resolved, ":")+1)
		return
	}
	// 登录 shell 探测全数失败(常见于 launchd 无 tty):兜底也必须带上用户级 bin 目录,
	// 否则 launchd 下永远找不到 ~/.local/bin 里的 claude。
	os.Setenv("PATH", ensureUserBinDirs(fallbackExtras+":"+current))
	log.Printf("ccfly: PATH fallback (login shell probe failed), prepended %s + user bin dirs", fallbackExtras)
}

// ensureUserBinDirs 保证 PATH 里含 claude 原生安装器使用的用户级目录(~/.local/bin、~/bin);
// 缺失则前置。这些目录不在系统/homebrew PATH 里,登录 shell 探测一旦失败(launchd 无 tty)就会
// 丢掉它们 —— 而 exec.LookPath("claude") 与 tmux 起会话都靠 PATH 找 claude。纯函数,便于单测。
func ensureUserBinDirs(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	want := []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin")}
	has := make(map[string]bool)
	for _, seg := range strings.Split(path, ":") {
		has[seg] = true
	}
	prepend := make([]string, 0, len(want))
	for _, w := range want {
		if !has[w] {
			prepend = append(prepend, w)
		}
	}
	if len(prepend) == 0 {
		return path
	}
	return strings.Join(append(prepend, path), ":")
}
