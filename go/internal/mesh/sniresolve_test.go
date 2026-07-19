package mesh

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
)

// 系统解析校验:全 loopback 才 ok;fake-ip/公网/非法地址 → fake。
func TestClassifyResolved(t *testing.T) {
	cases := []struct {
		name  string
		addrs []string
		ok    bool
		fake  bool
	}{
		{"v4 loopback", []string{"127.0.0.1"}, true, false},
		{"v4+v6 loopback", []string{"127.0.0.1", "::1"}, true, false},
		{"loopback 全段", []string{"127.0.0.53"}, true, false},
		{"fake-ip 198.18", []string{"198.18.0.23"}, false, true},
		{"fake-ip 198.19", []string{"198.19.255.255"}, false, true},
		{"公网地址", []string{"1.2.3.4"}, false, true},
		{"loopback 混入公网", []string{"127.0.0.1", "1.2.3.4"}, false, true},
		{"非法地址", []string{"not-an-ip"}, false, true},
		{"空结果", nil, false, false},
	}
	for _, c := range cases {
		ok, fake := classifyResolved(c.addrs)
		if ok != c.ok || fake != c.fake {
			t.Fatalf("%s: classifyResolved(%v)=(%v,%v) want (%v,%v)", c.name, c.addrs, ok, fake, c.ok, c.fake)
		}
	}
}

// firstNonLoopback:诊断文案取第一个非 loopback 地址并辨识 fake-ip 段。
func TestFirstNonLoopback(t *testing.T) {
	if addr, kind := firstNonLoopback([]string{"127.0.0.1", "198.18.0.23"}); addr != "198.18.0.23" || kind != "fake-ip" {
		t.Fatalf("fake-ip 辨识不对:%q %q", addr, kind)
	}
	if addr, kind := firstNonLoopback([]string{"203.0.113.9"}); addr != "203.0.113.9" || kind != "公网地址" {
		t.Fatalf("公网辨识不对:%q %q", addr, kind)
	}
	if addr, kind := firstNonLoopback([]string{"127.0.0.1", "::1"}); addr != "" || kind != "" {
		t.Fatalf("全 loopback 应为空:%q %q", addr, kind)
	}
}

// 状态分类:沿真实路径顺序的优先级 —— 解析被抢 > 自身组件坏 > 解析未落 loopback >
// loopback 路由不通 > 目标身份不符 > 上游 TLS 失败 > transparent。
func TestClassifyProbeTable(t *testing.T) {
	okProbe := func() *sniProbe {
		return &sniProbe{
			Canary: sniCanary, ComponentOK: true, ListenerOK: true, DNS53OK: true, OverlayOK: true, DirectOK: true,
			ResolverOK: true, ResolvedAddrs: []string{"127.0.0.1"}, LoopbackRouteOK: true, TargetOK: true, TLSOK: true,
		}
	}
	cases := []struct {
		name      string
		mutate    func(*sniProbe)
		resolve   error
		direct    error
		path      error
		tls       error
		wantMode  string
		wantCode  string
		wantInWhy string
	}{
		{"全过 transparent", nil, nil, nil, nil, nil, sniModeTransparent, "", ""},
		{"fake-ip 劫持", func(p *sniProbe) {
			p.ResolverOK, p.FakeDNSDetected = false, true
			p.ResolvedAddrs = []string{"198.18.0.23"}
			p.LoopbackRouteOK, p.TargetOK, p.TLSOK = false, false, false
		}, nil, nil, nil, nil, sniModeFakeDNS, "FAKEDNS_HIJACK", "198.18.0.23"},
		{"公网解析=未指向本地", func(p *sniProbe) {
			p.ResolverOK, p.FakeDNSDetected = false, true
			p.ResolvedAddrs = []string{"203.0.113.9"}
			p.LoopbackRouteOK, p.TargetOK, p.TLSOK = false, false, false
		}, nil, nil, nil, nil, sniModeFakeDNS, "FAKEDNS_HIJACK", "公网地址"},
		{":443 不可连", func(p *sniProbe) {
			p.ListenerOK, p.ComponentOK = false, false
		}, nil, nil, nil, nil, sniModeLocalConflict, "LOCAL_LISTENER_443", ":443"},
		{":53 不工作", func(p *sniProbe) {
			p.DNS53OK, p.ComponentOK = false, false
		}, nil, nil, nil, nil, sniModeLocalConflict, "LOCAL_DNS_53", ":53"},
		{"overlay 掉线", func(p *sniProbe) {
			p.OverlayOK, p.ComponentOK = false, false
		}, nil, nil, nil, nil, sniModeLocalConflict, "OVERLAY_DOWN", "overlay"},
		{"直连目标不符(组件层)", func(p *sniProbe) {
			p.DirectOK, p.ComponentOK = false, false
		}, nil, fmt.Errorf("egress target node mismatch: got \"x\", expected \"y\""), nil, nil, sniModeLocalConflict, "LOCAL_TARGET", "node mismatch"},
		{"解析出错", func(p *sniProbe) {
			p.ResolverOK = false
			p.ResolvedAddrs = nil
			p.LoopbackRouteOK, p.TargetOK, p.TLSOK = false, false, false
		}, fmt.Errorf("lookup api.anthropic.com: i/o timeout"), nil, nil, nil, sniModeFakeDNS, "RESOLVER_NOT_LOOPBACK", "timeout"},
		{"loopback 路由不通", func(p *sniProbe) {
			p.LoopbackRouteOK, p.TargetOK, p.TLSOK = false, false, false
		}, nil, nil, fmt.Errorf("egress identity dial: connection refused"), nil, sniModeLocalConflict, "LOOPBACK_ROUTE", "refused"},
		{"目标节点不符", func(p *sniProbe) {
			p.TargetOK, p.TLSOK = false, false
		}, nil, nil, &egressTargetError{"egress target node mismatch: got \"100.64.0.17\", expected \"100.64.0.16\""}, nil, sniModeTargetMismatch, "TARGET_MISMATCH", "node mismatch"},
		{"上游 TLS 失败", func(p *sniProbe) {
			p.TLSOK = false
		}, nil, nil, nil, fmt.Errorf("tls: handshake timeout"), sniModeUpstreamFailed, "UPSTREAM_TLS", "TLS"},
		{"fake 优先于组件坏", func(p *sniProbe) {
			p.ResolverOK, p.FakeDNSDetected = false, true
			p.ResolvedAddrs = []string{"198.18.0.1"}
			p.ListenerOK, p.ComponentOK = false, false
			p.LoopbackRouteOK, p.TargetOK, p.TLSOK = false, false, false
		}, nil, nil, nil, nil, sniModeFakeDNS, "FAKEDNS_HIJACK", ""},
	}
	for _, c := range cases {
		p := okProbe()
		if c.mutate != nil {
			c.mutate(p)
		}
		mode, code, why := classifyProbe(p, c.resolve, c.direct, c.path, c.tls)
		if mode != c.wantMode || code != c.wantCode {
			t.Fatalf("%s: classifyProbe=(%q,%q,%q) want mode=%q code=%q", c.name, mode, code, why, c.wantMode, c.wantCode)
		}
		if c.wantInWhy != "" && !strings.Contains(why, c.wantInWhy) {
			t.Fatalf("%s: why=%q 应含 %q", c.name, why, c.wantInWhy)
		}
	}
}

// probeAppPathIdentity:路由不通 → 换下一个解析地址;身份不符 → 立即返回 target mismatch;
// 全部拨不通 → routeOK=false。
func TestProbeAppPathIdentityFallback(t *testing.T) {
	old := sniProbePathPort
	t.Cleanup(func() { sniProbePathPort = old })

	// 好节点服务(复用 egresscheck 测试的 nonce 应答器)
	good := serveEgressProbeTest(t, nil)
	_, port, err := net.SplitHostPort(good)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatal(err)
	}
	sniProbePathPort, _ = strconv.Atoi(port)

	dialAddr, routeOK, id, err := probeAppPathIdentity([]string{"127.0.0.1"}, "100.64.0.16", "a@example.com")
	if !routeOK || err != nil || dialAddr == "" {
		t.Fatalf("好服务应 routeOK 且无误:%v %v %q %v", dialAddr, routeOK, id, err)
	}
	if id.NodeID != "100.64.0.16" || id.Identity != "a@example.com" {
		t.Fatalf("identity=%+v", id)
	}

	// 错误节点 → routeOK=true + egressTargetError
	bad := serveEgressProbeTest(t, func(resp *egressProbeWire) { resp.NodeID = "100.64.0.17" })
	_, badPort, err := net.SplitHostPort(bad)
	if err != nil {
		t.Fatal(err)
	}
	sniProbePathPort, _ = strconv.Atoi(badPort)
	_, routeOK, _, err = probeAppPathIdentity([]string{"127.0.0.1"}, "100.64.0.16", "a@example.com")
	var targetErr *egressTargetError
	if !routeOK || !errors.As(err, &targetErr) {
		t.Fatalf("错误节点应 routeOK=true + target 错误:%v %v", routeOK, err)
	}

	// 未监听端口 → routeOK=false
	sniProbePathPort = 1 // port 1 恒拒连
	_, routeOK, _, err = probeAppPathIdentity([]string{"127.0.0.1"}, "100.64.0.16", "a@example.com")
	if routeOK || err == nil {
		t.Fatalf("未监听应 routeOK=false:%v %v", routeOK, err)
	}
}
