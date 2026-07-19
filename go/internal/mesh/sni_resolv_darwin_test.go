//go:build darwin

package mesh

// sni_resolv_darwin_test.go — macOS /etc/resolver scoped 指向的回归(用临时目录,免 root)。
// 验证:pointResolver 为每个 intercept 域写一个带 ccfly 标记的文件;restoreResolver 只清 ccfly 标记的、
// 不动用户自己的 /etc/resolver 文件。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDarwinResolverScoped(t *testing.T) {
	dir := t.TempDir()
	// hosts 也打桩到临时文件:restoreResolver 会顺带恢复旧版 hosts 托管块,
	// 不桩就会去动真 /etc/hosts(非 root 直接 permission denied)。
	hostsPath := filepath.Join(dir, "hosts")
	if err := os.WriteFile(hostsPath, []byte("127.0.0.1\tlocalhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old, oldPort, oldHosts := resolverDir, sniCoreDNSPort, unixHostsPath
	resolverDir = dir
	sniCoreDNSPort = defaultCoreDNSPort
	unixHostsPath = hostsPath
	defer func() { resolverDir, sniCoreDNSPort, unixHostsPath = old, oldPort, oldHosts }()

	// 预置一个用户自己的 resolver 文件(不带 ccfly 标记)——restore 绝不能删它。
	userFile := filepath.Join(dir, "corp.example.com")
	if err := os.WriteFile(userFile, []byte("nameserver 10.0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	intercept := []string{"anthropic.com", "claude.ai", "statsig.com"}
	if err := pointResolver(intercept, "223.5.5.5", nil); err != nil {
		t.Fatalf("pointResolver: %v", err)
	}
	// 每个 intercept 域一个文件,内容含 127.0.0.1 + 标记。
	for _, d := range intercept {
		b, err := os.ReadFile(filepath.Join(dir, d))
		if err != nil {
			t.Fatalf("应写 /etc/resolver/%s: %v", d, err)
		}
		if s := string(b); !contains(s, "nameserver 127.0.0.1") || !contains(s, resolverMarker) {
			t.Fatalf("%s 内容不对:%q", d, s)
		}
	}
	// restore:清掉 ccfly 的,保留用户的。
	if err := restoreResolver(); err != nil {
		t.Fatalf("restoreResolver: %v", err)
	}
	for _, d := range intercept {
		if _, err := os.Stat(filepath.Join(dir, d)); !os.IsNotExist(err) {
			t.Fatalf("restore 后 %s 应被删除", d)
		}
	}
	if _, err := os.Stat(userFile); err != nil {
		t.Fatal("restore 绝不能删用户自己的 resolver 文件")
	}
}

func TestDarwinResolverRefusesExistingDomain(t *testing.T) {
	dir := t.TempDir()
	old := resolverDir
	resolverDir = dir
	defer func() { resolverDir = old }()

	path := filepath.Join(dir, "anthropic.com")
	original := "nameserver 10.0.0.53\nsearch_order 1\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pointResolver([]string{"anthropic.com"}, "223.5.5.5", nil); err == nil {
		t.Fatal("pointResolver should refuse to overwrite a user-owned resolver")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != original {
		t.Fatalf("existing resolver changed: %q, %v", b, err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
