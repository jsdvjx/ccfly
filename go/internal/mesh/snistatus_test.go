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

// 已装 arm → passive 字段从 cur/状态派生;探测走缓存(预置新鲜结果,避免真实 :443/:53 I/O)。
func TestSNIStatusArmedSnapshot(t *testing.T) {
	// 预置一份新鲜探测缓存 → cachedProbe 直接返回、不 spawn goroutine、不做网络 I/O。
	probeCache.Store(&sniProbe{Canary: sniCanary, DNSOK: true, PathOK: true, At: time.Now().Unix()})
	t.Cleanup(func() { probeCache.Store(nil); probeRunning.Store(false) })

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
	if s.Probe == nil || !s.Probe.PathOK || !s.Probe.DNSOK {
		t.Fatalf("armed 应带(缓存的)探测:%+v", s.Probe)
	}

	// JSON 键与 cloud/web 对齐(线契约回归)。
	b, _ := json.Marshal(s)
	for _, k := range []string{`"armed"`, `"exit"`, `"platform"`, `"probe"`, `"path_ok"`, `"dns_bound"`, `"listeners"`, `"resolver_pointed"`, `"overlay_up"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("JSON 缺键 %s:%s", k, b)
		}
	}
}
