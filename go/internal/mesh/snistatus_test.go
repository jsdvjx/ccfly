package mesh

import (
	"encoding/json"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 未装 arm → armed=false、无 probe、平台字段填好。
func TestSNIStatusNotArmed(t *testing.T) {
	m := &sniManager{}
	s := m.status(false)
	if s.Armed {
		t.Fatal("未装 arm 应 armed=false")
	}
	if s.Probe != nil {
		t.Fatal("未 armed 不应跑探测")
	}
	if s.Platform != runtime.GOOS {
		t.Fatalf("platform=%q want %q", s.Platform, runtime.GOOS)
	}
	if s.Listeners != 0 || s.DNSBound || s.ResolverPointed {
		t.Fatalf("未装 arm 各监听应为空:%+v", s)
	}
}

// 已装 arm → passive 字段从 cur/状态派生;探测走调度器缓存(预置新鲜结果,避免真实 :443/:53 I/O)。
func TestSNIStatusArmedSnapshot(t *testing.T) {
	// 预置一份新鲜探测缓存 → cachedProbe 直接返回、不做网络 I/O。
	probeCache.Store(&sniProbe{
		Canary: sniCanary, ComponentOK: true, ListenerOK: true, DNS53OK: true, OverlayOK: true, DirectOK: true,
		ResolverOK: true, ResolvedAddrs: []string{"127.0.0.1", "::1"}, LoopbackRouteOK: true,
		TargetOK: true, TLSOK: true, PathOK: true, Mode: sniModeTransparent, DNSOK: true,
		TargetNode: "100.64.0.16", TargetExitID: "eg-a", TargetIdentity: "a@x.com",
		BoundEgress4: "10.0.0.84", At: time.Now().Unix(),
	})
	t.Cleanup(func() { probeCache.Store(nil) })

	since := time.Now().Add(-2 * time.Minute)
	m := &sniManager{
		cur: &SNIConfig{
			Enabled: true, Account: "a@x.com",
			Exit:      SNIExit{Host: "100.64.0.16", Port: 443},
			Intercept: []string{"anthropic.com", "claude.ai"},
		},
		listeners: make([]net.Listener, 2), // 两个 nil 元素:仅测 len(=v4+v6)
		resolvOn:  true,
		since:     since,
	}
	s := m.status(false)
	if !s.Armed || s.Account != "a@x.com" || s.Exit != "100.64.0.16:443" {
		t.Fatalf("armed 派生字段不对:%+v", s)
	}
	if len(s.Intercept) != 2 || s.Intercept[0] != "anthropic.com" {
		t.Fatalf("intercept 不对:%+v", s.Intercept)
	}
	if s.Listeners != 2 || !s.ResolverPointed {
		t.Fatalf("监听/resolver 派生不对:%+v", s)
	}
	if s.Since != since.Unix() {
		t.Fatalf("since=%d want %d", s.Since, since.Unix())
	}
	if s.Probe == nil || !s.Probe.PathOK || !s.Probe.TargetOK || !s.Probe.TLSOK || !s.Probe.ResolverOK || !s.Probe.ComponentOK {
		t.Fatalf("armed 应带(缓存的)探测:%+v", s.Probe)
	}
	if s.Probe.Mode != sniModeTransparent || s.Probe.Stale {
		t.Fatalf("新鲜通过结果应 mode=transparent 且不 stale:%+v", s.Probe)
	}

	// JSON 键与 cloud/web 对齐(线契约回归)。
	b, _ := json.Marshal(s)
	for _, k := range []string{`"armed"`, `"exit"`, `"platform"`, `"probe"`,
		`"component_ok"`, `"listener_443_ok"`, `"dns_53_ok"`, `"overlay_ok"`, `"direct_target_ok"`,
		`"resolver_ok"`, `"resolved_addresses"`, `"loopback_route_ok"`, `"target_ok"`, `"tls_ok"`,
		`"path_ok"`, `"mode"`, `"dns_ok"`,
		`"target_node"`, `"target_exit_id"`, `"bound_egress_ipv4"`,
		`"dns_bound"`, `"listeners"`, `"resolver_pointed"`, `"overlay_up"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("JSON 缺键 %s:%s", k, b)
		}
	}
}

// 探测结果过期(>45s)→ mode=checking、stale=true、path_ok 摘掉(不标记健康)。
func TestSNIStatusStaleProbeIsChecking(t *testing.T) {
	probeCache.Store(&sniProbe{
		Canary: sniCanary, ResolverOK: true, LoopbackRouteOK: true, TargetOK: true, TLSOK: true,
		PathOK: true, Mode: sniModeTransparent, At: time.Now().Add(-2 * time.Minute).Unix(),
	})
	t.Cleanup(func() { probeCache.Store(nil) })

	m := &sniManager{cur: &SNIConfig{Enabled: true, Account: "a@x.com", Exit: SNIExit{Host: "100.64.0.16", Port: 443}}}
	s := m.status(false)
	if s.Probe == nil {
		t.Fatal("过期缓存仍应带回(供调试)")
	}
	if s.Probe.Mode != sniModeChecking || !s.Probe.Stale || s.Probe.PathOK {
		t.Fatalf("过期结果应 mode=checking + stale + path_ok=false:%+v", s.Probe)
	}
}

// 检测周期退避序列:成功=30s;失败 2s→5s→10s→30s 封顶。
func TestSNIProbeDelay(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 30 * time.Second},
		{1, 2 * time.Second},
		{2, 5 * time.Second},
		{3, 10 * time.Second},
		{4, 30 * time.Second},
		{9, 30 * time.Second},
	}
	for _, c := range cases {
		if got := sniProbeDelay(c.failures); got != c.want {
			t.Fatalf("sniProbeDelay(%d)=%v want %v", c.failures, got, c.want)
		}
	}
}
