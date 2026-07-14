package mesh

import (
	"strings"
	"testing"
)

func TestStripCcflyHostsBlock_NoBlock(t *testing.T) {
	in := "127.0.0.1 localhost\r\n10.0.0.5 myserver\r\n"
	if got := stripCcflyHostsBlock(in); got != in {
		t.Fatalf("no-block content must be unchanged; got %q", got)
	}
}

func TestApplyAndStrip_PreservesUserEntries(t *testing.T) {
	user := "# my hosts\r\n127.0.0.1 localhost\r\n192.168.1.10 nas.local\r\n"
	applied := applyCcflyHostsBlock(user, []string{"api.anthropic.com", "claude.ai"})

	// user lines must survive verbatim
	for _, must := range []string{"127.0.0.1 localhost", "192.168.1.10 nas.local", "# my hosts"} {
		if !strings.Contains(applied, must) {
			t.Fatalf("user entry %q lost after apply:\n%s", must, applied)
		}
	}
	// pinned hosts present, both v4 and v6
	for _, must := range []string{"127.0.0.1 api.anthropic.com", "::1 api.anthropic.com", "127.0.0.1 claude.ai", "::1 claude.ai"} {
		if !strings.Contains(applied, must) {
			t.Fatalf("pinned entry %q missing:\n%s", must, applied)
		}
	}
	if !strings.Contains(applied, hostsBeginPrefix) || !strings.Contains(applied, hostsEndPrefix) {
		t.Fatalf("markers missing:\n%s", applied)
	}

	// stripping returns to (trailing-trimmed) user content — no ccfly residue
	stripped := stripCcflyHostsBlock(applied)
	if strings.Contains(stripped, "anthropic") || strings.Contains(stripped, hostsBeginPrefix) {
		t.Fatalf("ccfly block not fully removed:\n%s", stripped)
	}
	for _, must := range []string{"127.0.0.1 localhost", "192.168.1.10 nas.local"} {
		if !strings.Contains(stripped, must) {
			t.Fatalf("user entry %q lost after strip:\n%s", must, stripped)
		}
	}
}

func TestApply_Idempotent(t *testing.T) {
	user := "127.0.0.1 localhost\r\n"
	once := applyCcflyHostsBlock(user, sniPinnedHosts)
	twice := applyCcflyHostsBlock(once, sniPinnedHosts)
	if once != twice {
		t.Fatalf("apply not idempotent:\nonce=%q\ntwice=%q", once, twice)
	}
	// exactly one managed block after re-apply
	if n := strings.Count(twice, hostsBeginPrefix); n != 1 {
		t.Fatalf("expected exactly 1 ccfly block, got %d:\n%s", n, twice)
	}
}

func TestApply_EmptyExisting(t *testing.T) {
	out := applyCcflyHostsBlock("", []string{"api.anthropic.com"})
	if !strings.HasPrefix(out, hostsBeginPrefix) {
		t.Fatalf("empty-existing should yield just the block, got:\n%s", out)
	}
	if !strings.Contains(out, "::1 api.anthropic.com") {
		t.Fatalf("v6 pin missing:\n%s", out)
	}
}

func TestPinnedHostsAreExactHostnames(t *testing.T) {
	if len(sniPinnedHosts) == 0 {
		t.Fatal("sniPinnedHosts must not be empty")
	}
	for _, h := range sniPinnedHosts {
		if strings.ContainsAny(h, "*/ ") || !strings.Contains(h, ".") {
			t.Fatalf("pinned host %q must be an exact hostname (no wildcard/space)", h)
		}
	}
}
