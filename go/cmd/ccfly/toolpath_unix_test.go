//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureUserBinDirs(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	localBin := filepath.Join(home, ".local", "bin")
	userBin := filepath.Join(home, "bin")

	// 兜底 PATH(homebrew + 系统,launchd 探测失败时的形态)缺 ~/.local/bin → 必须被补上。
	// 这正是 claude 原生安装器的落点;不补 = exec.LookPath("claude") 与 tmux 起会话都找不到。
	t.Run("prepends missing local bin", func(t *testing.T) {
		got := ensureUserBinDirs("/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin")
		segs := strings.Split(got, ":")
		if segs[0] != localBin {
			t.Fatalf("expected %s prepended first, got PATH=%s", localBin, got)
		}
		if !contains(segs, userBin) {
			t.Fatalf("expected %s present, got PATH=%s", userBin, got)
		}
	})

	// 已含则不重复添加(幂等),且顺序不动。
	t.Run("idempotent when already present", func(t *testing.T) {
		in := localBin + ":" + userBin + ":/usr/bin"
		if got := ensureUserBinDirs(in); got != in {
			t.Fatalf("expected unchanged %q, got %q", in, got)
		}
	})
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
