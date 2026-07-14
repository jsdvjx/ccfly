package mesh

// snistatus.go — SNI arm 运行状态自检 + 可观测(回答「用户装了最新版,如何确定他的 SNI proxy 正常」)。
//
// 两类信号:
//   ① passive 快照(零 I/O,读 sniMgr):armed(setup 成功)、account/exit、:53 DNS 起没起、:443 监听数、
//      resolver 是否指向本地、overlay 是否就绪、since、最后一次 setup 失败原因、平台。
//   ② active 探测(缓存,可选同步刷新):
//        - dns_ok:向 127.0.0.1:53 解析 canary,期望 loopback —— 证明内嵌 DNS 起且 intercept 命中。
//        - path_ok:连本地 :443(生产入口)发 SNI=canary 的真实 TLS 握手 —— 握手成功 = 本地 :443 监听 +
//          overlay + 账号出口 byway-sni + 源规则匹配 + 真出网 全通(证书对 canary 校验通过,端到端真证书)。
//          任一环断(非 root 未 bind :443 / overlay 断 / 源规则不匹配 fail-closed / 出口不可达)→ 握手失败。
//
// 暴露路径:
//   - 上报云端:syncer 的 20s pushSummaries 带上快照(同包直读,无跨包环)。
//   - 本地自检:control 的 GET /sni-status,经注入的 control.SNIStatusFn → 这里(control 不能 import mesh)。

import (
	"context"
	"crypto/tls"
	"net"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	sniCanary       = "api.anthropic.com" // 探测用的锚点主机:恒在 byway-sni 默认 allowlist(*.anthropic.com)且恒在线
	sniProbeTTL     = 90 * time.Second    // 缓存探测的最大新鲜度(async 刷新;不阻塞 syncer/上报)
	sniProbeTimeout = 8 * time.Second     // 单次探测的硬超时(DNS 3s + TLS 握手 8s 上限)
)

// SNIStatusJSON 是 mesh 暴露给 control 包的注入点(control 不能 import mesh,见 cmd/ccfly 接线)。
// fresh=true 同步跑一次新探测(供人工 GET /sni-status?probe=1 拿实时结果);否则用缓存(async 刷新)。
func SNIStatusJSON(fresh bool) any { return sniMgr.status(fresh) }

// sniSnapshot 是设备侧上报/自检的线格式(cloud 原样存,web 据此渲染徽章)。
type sniSnapshot struct {
	Armed           bool      `json:"armed"` // arm 已装且 setup 成功(sniMgr.cur != nil)
	Account         string    `json:"account,omitempty"`
	Exit            string    `json:"exit,omitempty"` // host:port
	Intercept       []string  `json:"intercept,omitempty"`
	DNSBound        bool      `json:"dns_bound"`        // :53 UDP 起
	Listeners       int       `json:"listeners"`        // :443 监听数(健康=2:v4+v6)
	ResolverPointed bool      `json:"resolver_pointed"` // pointResolver 成功(系统解析已指向本地)
	OverlayUp       bool      `json:"overlay_up"`       // activeNet != nil(WG overlay 就绪,:443 relay 才拨得出去)
	LastError       string    `json:"last_error,omitempty"`
	Since           int64     `json:"since,omitempty"` // arm 起来的 unix 秒
	Platform        string    `json:"platform"`        // runtime.GOOS(非 Linux + 非 root 会静默 no-op,便于解释)
	Probe           *sniProbe `json:"probe,omitempty"` // 主动探测(仅 armed 时;缓存,可能为 nil=探测中)
}

// sniProbe 是一次主动自检的结果。
type sniProbe struct {
	Canary string `json:"canary"`
	DNSOK  bool   `json:"dns_ok"`  // 127.0.0.1:53 解析 canary → loopback
	PathOK bool   `json:"path_ok"` // 127.0.0.1:443 → overlay → exit,TLS 握手到 canary 成功
	Error  string `json:"error,omitempty"`
	MS     int64  `json:"ms"`
	At     int64  `json:"at"` // unix 秒
}

// status 组装快照;armed 时附带探测(fresh=同步刷新,否则 async 缓存)。
func (m *sniManager) status(fresh bool) sniSnapshot {
	m.mu.Lock()
	s := sniSnapshot{
		Platform:  runtime.GOOS,
		OverlayUp: activeNet.Load() != nil,
		DNSBound:  m.dnsConn != nil,
		Listeners: len(m.listeners),
	}
	s.ResolverPointed = m.resolvOn
	s.LastError = m.lastErr
	var exit SNIExit
	if m.cur != nil {
		s.Armed = true
		s.Account = m.cur.Account
		exit = m.cur.Exit
		s.Exit = net.JoinHostPort(exit.Host, strconv.Itoa(exit.Port))
		s.Intercept = append([]string(nil), m.cur.Intercept...)
	}
	if !m.since.IsZero() {
		s.Since = m.since.Unix()
	}
	m.mu.Unlock()

	if s.Armed {
		s.Probe = cachedProbe(exit, fresh)
	}
	return s
}

// ── 主动探测(单例缓存,async 刷新以免阻塞 syncer/上报;fresh 时同步跑一次)──

var (
	probeCache   atomic.Pointer[sniProbe]
	probeRunning atomic.Bool
	probeMu      sync.Mutex // fresh 路径单飞
)

func cachedProbe(exit SNIExit, fresh bool) *sniProbe {
	if fresh {
		probeMu.Lock()
		defer probeMu.Unlock()
		p := runSNIProbe(exit)
		probeCache.Store(p)
		return p
	}
	p := probeCache.Load()
	stale := p == nil || time.Since(time.Unix(p.At, 0)) >= sniProbeTTL
	if stale && probeRunning.CompareAndSwap(false, true) {
		go func() {
			defer probeRunning.Store(false)
			probeMu.Lock()
			np := runSNIProbe(exit)
			probeMu.Unlock()
			probeCache.Store(np)
		}()
	}
	return p // 可能为 nil(首次探测尚未完成);上报/展示端按「探测中」处理
}

// runSNIProbe 跑一次 DNS + PATH 自检。exit 仅用于日志/上下文;PATH 探测走本地 :443 生产入口(自带 exit)。
func runSNIProbe(exit SNIExit) *sniProbe {
	start := time.Now()
	p := &sniProbe{Canary: sniCanary, At: start.Unix()}
	p.DNSOK = probeDNSLoopback(sniCanary)
	ok, err := probeSNIPath(sniCanary)
	p.PathOK = ok
	if err != nil {
		p.Error = err.Error()
	}
	p.MS = time.Since(start).Milliseconds()
	return p
}

// probeDNSLoopback 强制走 127.0.0.1:53 解析 host,命中 loopback 即证明内嵌 DNS 起且 intercept 命中。
func probeDNSLoopback(host string) bool {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 3 * time.Second}
			return d.DialContext(ctx, "udp", "127.0.0.1:53")
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := r.LookupHost(ctx, host)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if a == "127.0.0.1" || a == "::1" {
			return true
		}
	}
	return false
}

// probeSNIPath 连本地 :443(SNI arm 的生产入口),发 SNI=host 的真实 TLS 握手。握手成功(证书对 host
// 校验通过)= 本地 :443 + overlay + 账号出口 + 源规则 + 真出网 全通。非 root 未 bind → dial 被拒 → 不通(正确信号)。
func probeSNIPath(host string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sniProbeTimeout)
	defer cancel()
	d := net.Dialer{Timeout: sniProbeTimeout}
	raw, err := d.DialContext(ctx, "tcp", "127.0.0.1:443")
	if err != nil {
		return false, err
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(sniProbeTimeout))
	tc := tls.Client(raw, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err := tc.HandshakeContext(ctx); err != nil {
		return false, err
	}
	_ = tc.Close()
	return true, nil
}
