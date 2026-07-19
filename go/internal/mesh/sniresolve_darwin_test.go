//go:build darwin

package mesh

import "testing"

// dscacheutil 输出解析:抽 ip_address/ipv6_address,去重,忽略无关行。
func TestParseDscacheutilHosts(t *testing.T) {
	out := []byte(`name: api.anthropic.com
ip_address: 127.0.0.1

name: api.anthropic.com
ipv6_address: ::1

name: api.anthropic.com
ip_address: 127.0.0.1
`)
	got := parseDscacheutilHosts(out)
	if len(got) != 2 || got[0] != "127.0.0.1" || got[1] != "::1" {
		t.Fatalf("parse=%v", got)
	}
	if got := parseDscacheutilHosts(nil); len(got) != 0 {
		t.Fatalf("空输出应无地址:%v", got)
	}
	// fake-ip 场景:dscacheutil 如实返回,分类交给 classifyResolved
	fake := parseDscacheutilHosts([]byte("name: api.anthropic.com\nip_address: 198.18.0.23\n"))
	if len(fake) != 1 || fake[0] != "198.18.0.23" {
		t.Fatalf("fake-ip 输出解析=%v", fake)
	}
}
