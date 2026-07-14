package tmuxbin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 覆盖释放三态:首次释放、已最新零写入(mtime 不变)、内容被篡改后自愈。
// 仅在带 blob 的构建(darwin/arm64、darwin/amd64)上有效,其余平台跳过。
func TestEnsureAt(t *testing.T) {
	if !Bundled() {
		t.Skip("no bundled tmux on this platform")
	}
	dir := t.TempDir()

	p, err := EnsureAt(dir)
	if err != nil {
		t.Fatalf("EnsureAt: %v", err)
	}
	if p != filepath.Join(dir, "tmux") {
		t.Fatalf("path = %q", p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", fi.Mode().Perm())
	}

	// 幂等:重复 Ensure 不应重写(mtime 不变)。
	if _, err := EnsureAt(dir); err != nil {
		t.Fatalf("EnsureAt again: %v", err)
	}
	fi2, _ := os.Stat(p)
	if !fi2.ModTime().Equal(fi.ModTime()) {
		t.Fatalf("idempotent Ensure rewrote the file")
	}

	// 损坏/旧版自愈:内容不同 → 重写恢复。
	if err := os.WriteFile(p, []byte("corrupted"), 0o755); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if _, err := EnsureAt(dir); err != nil {
		t.Fatalf("EnsureAt after corrupt: %v", err)
	}
	if fi3, _ := os.Stat(p); fi3.Size() == int64(len("corrupted")) {
		t.Fatalf("corrupted file not repaired")
	}

	// 释放出的二进制真的可执行(测试机架构与构建架构一致,直接跑 -V)。
	out, err := exec.Command(p, "-V").CombinedOutput()
	if err != nil {
		t.Fatalf("run bundled tmux -V: %v (%s)", err, out)
	}
	if !strings.HasPrefix(string(out), "tmux ") {
		t.Fatalf("tmux -V output = %q", out)
	}
	t.Logf("bundled %s", strings.TrimSpace(string(out)))
}
