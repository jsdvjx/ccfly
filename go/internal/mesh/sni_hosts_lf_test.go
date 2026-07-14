package mesh

import "strings"

import "testing"

// LF 变体:macOS /etc/hosts 用 LF 行尾(Windows 用 CRLF)。验证块渲染/局部替换在 LF 下正确,
// 且能剥回用户原文(strip 是 EOL 无关的,复用同一个)。

func TestBuildCcflyHostsBlockLF_UsesLF(t *testing.T) {
	block := buildCcflyHostsBlockEOL([]string{"api.anthropic.com"}, "\n")
	if strings.Contains(block, "\r\n") {
		t.Fatalf("LF block must not contain CRLF:\n%q", block)
	}
	for _, must := range []string{
		hostsBeginPrefix,
		"127.0.0.1 api.anthropic.com\n",
		"::1 api.anthropic.com\n",
		hostsEndPrefix,
	} {
		if !strings.Contains(block, must) {
			t.Fatalf("LF block missing %q:\n%q", must, block)
		}
	}
}

func TestApplyCcflyHostsBlockLF_PreservesUserEntriesAndRoundtrips(t *testing.T) {
	// 典型 /etc/hosts(LF)。
	user := "##\n# Host Database\n##\n127.0.0.1\tlocalhost\n255.255.255.255\tbroadcasthost\n::1             localhost\n"
	applied := applyCcflyHostsBlockEOL(user, []string{"api.anthropic.com", "claude.ai"}, "\n")

	if strings.Contains(applied, "\r\n") {
		t.Fatalf("applied LF content must not contain CRLF:\n%q", applied)
	}
	// 用户原条目逐字保留。
	for _, must := range []string{"127.0.0.1\tlocalhost", "255.255.255.255\tbroadcasthost", "# Host Database"} {
		if !strings.Contains(applied, must) {
			t.Fatalf("user entry %q lost:\n%s", must, applied)
		}
	}
	// 钉入的主机名,v4+v6 都在。
	for _, must := range []string{"127.0.0.1 api.anthropic.com", "::1 api.anthropic.com", "127.0.0.1 claude.ai", "::1 claude.ai"} {
		if !strings.Contains(applied, must) {
			t.Fatalf("pinned %q missing:\n%s", must, applied)
		}
	}
	// 幂等:再 apply 一次(模拟重复 arm)不应堆叠出第二个块。
	twice := applyCcflyHostsBlockEOL(applied, []string{"api.anthropic.com", "claude.ai"}, "\n")
	if strings.Count(twice, hostsBeginPrefix) != 1 {
		t.Fatalf("re-apply must not stack blocks; got %d BEGIN markers:\n%s", strings.Count(twice, hostsBeginPrefix), twice)
	}
	// 剥回:无 ccfly 残留,用户条目还在。
	stripped := strings.TrimRight(stripCcflyHostsBlock(twice), "\n")
	if strings.Contains(stripped, "anthropic") || strings.Contains(stripped, hostsBeginPrefix) {
		t.Fatalf("ccfly residue after strip:\n%s", stripped)
	}
	if strings.TrimRight(user, "\n") != stripped {
		t.Fatalf("strip did not restore user content.\nwant: %q\ngot:  %q", strings.TrimRight(user, "\n"), stripped)
	}
}
