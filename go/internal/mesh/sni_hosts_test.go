package mesh

import (
	"strings"
	"testing"
)

// hosts 方案已下线(三端统一 :53 DNS),这里只保留「旧版托管块剥离」的回归——卸载/重装时
// 清理老版本残留仍依赖 stripCcflyHostsBlock(darwin restoreUnixHosts、restoreResolver 兼容清理)。

// 手写的旧版块样例(不再保留渲染函数,剥离必须容忍任意旧文案)。
const legacyHostsBlock = "# BEGIN ccfly-sni (managed by ccfly — 局部块，勿手改；卸载 ccfly 可删本块)\r\n127.0.0.1 api.anthropic.com\r\n::1 api.anthropic.com\r\n127.0.0.1 claude.ai\r\n::1 claude.ai\r\n# END ccfly-sni\r\n"

func TestStripCcflyHostsBlock_NoBlock(t *testing.T) {
	in := "127.0.0.1 localhost\r\n10.0.0.5 myserver\r\n"
	if got := stripCcflyHostsBlock(in); got != in {
		t.Fatalf("no-block content must be unchanged; got %q", got)
	}
}

func TestStripCcflyHostsBlock_RemovesLegacyBlockKeepsUserEntries(t *testing.T) {
	user := "# my hosts\r\n127.0.0.1 localhost\r\n192.168.1.10 nas.local\r\n"
	stripped := stripCcflyHostsBlock(user + legacyHostsBlock)
	if strings.Contains(stripped, "anthropic") || strings.Contains(stripped, hostsBeginPrefix) {
		t.Fatalf("ccfly block not fully removed:\n%s", stripped)
	}
	for _, must := range []string{"127.0.0.1 localhost", "192.168.1.10 nas.local", "# my hosts"} {
		if !strings.Contains(stripped, must) {
			t.Fatalf("user entry %q lost after strip:\n%s", must, stripped)
		}
	}
}

func TestStripCcflyHostsBlock_LFVariant(t *testing.T) {
	user := "##\n# Host Database\n##\n127.0.0.1\tlocalhost\n"
	lfBlock := strings.ReplaceAll(legacyHostsBlock, "\r\n", "\n")
	stripped := strings.TrimRight(stripCcflyHostsBlock(user+lfBlock), "\n")
	if strings.Contains(stripped, "anthropic") || strings.Contains(stripped, hostsBeginPrefix) {
		t.Fatalf("ccfly residue after LF strip:\n%s", stripped)
	}
	if stripped != strings.TrimRight(user, "\n") {
		t.Fatalf("LF strip did not restore user content:\nwant %q\ngot  %q", strings.TrimRight(user, "\n"), stripped)
	}
}
