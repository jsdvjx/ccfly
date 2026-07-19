package mesh

// snistatus.go — SNI arm 运行状态自检 + 可观测(回答「用户装了最新版,如何确定他的 SNI proxy 正常」)。
//
// 检测模型分两层,仅探测 127.0.0.1 不足以识别 FakeDNS/TUN 抢流量:
//
//	① 组件自检(component_ok):直连 127.0.0.1 的主动检查 —— 本地 :53 是否工作、本地 :443 是否
//	   可连、overlay 是否在线、直连 127.0.0.1:443 能否到目标节点。只回答「ccfly 自身组件是否
//	   正常」,不再直接作为最终 path_ok。
//	② 真实应用路径检测:系统原生解析 canary(macOS 走 libinfo//etc/resolver,Windows 走
//	   GetAddrInfo/hosts,Linux 走 resolv.conf)→ 严格要求全部落 loopback(出现 198.18.0.0/15
//	   fake-ip 或公网地址 = FAKEDNS_HIJACK)→ 按解析给出的地址(真实应用会连的地址)建连发
//	   nonce 核对节点+账号出口 → 真实上游 TLS。可检出:FakeDNS 抢解析、TUN 抢 TCP、loopback
//	   被透明代理重定向、resolver 配置被覆盖、到达错误节点/账号出口。
//
// 最终判定:path_ok = resolver_ok && loopback_route_ok && target_ok && tls_ok。
// 状态分类(mode):transparent / fakedns_conflict / local_conflict / target_mismatch /
// upstream_failed / checking(结果超过 45s 过期 → checking,不标记健康)。
//
// 检测周期:arm 后立即一次;正常每 30s;网络变化(WG up/down)/配置刷新 kick 立即重检;
// 失败退避 2s→5s→10s→30s;target_mismatch 首次出现即上报(无防抖)。
//
// 暴露路径:
//   - 上报云端:syncer 的 20s pushSummaries 带上快照(同包直读,无跨包环)。
//   - 本地自检:control 的 GET /sni-status,经注入的 control.SNIStatusFn → 这里(control 不能 import mesh)。

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	sniCanary       = "api.anthropic.com" // 探测用的锚点主机:恒在 byway-sni 默认 allowlist(*.anthropic.com)且恒在线
	sniProbeTTL     = 45 * time.Second    // 结果最大新鲜度:超过即过期 → mode=checking,不标记健康
	sniProbeTimeout = 8 * time.Second     // 单次探测的硬超时(DNS 3s + TLS 握手 8s 上限)
	sniProbePeriod  = 30 * time.Second    // 正常状态的周期检测间隔
)

// 状态分类(线格式 mode 字段;web 据此渲染明确原因)。
const (
	sniModeTransparent    = "transparent"     // DNS、loopback、目标身份、TLS 全部正常
	sniModeFakeDNS        = "fakedns_conflict" // 系统解析被 FakeDNS/TUN 接管
	sniModeLocalConflict  = "local_conflict"   // :53/:443/loopback 被占用或劫持
	sniModeTargetMismatch = "target_mismatch"  // 到了错误节点或错误账号出口
	sniModeUpstreamFailed = "upstream_failed"  // 节点正确但真实上游不可用
	sniModeChecking       = "checking"         // 尚无新鲜检测结果
)

// 失败退避:2s→5s→10s→30s(封顶=正常周期)。
var sniProbeBackoff = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

// sniProbePathPort 是真实路径探测拨入的本地端口(生产恒 443;可变仅为隔离测试,同 sniCoreDNSPort 模式)。
var sniProbePathPort = 443

// SNIStatusJSON 是 mesh 暴露给 control 包的注入点(control 不能 import mesh,见 cmd/ccfly 接线)。
// fresh=true 同步跑一次新探测(供人工 GET /sni-status?probe=1 拿实时结果);否则用调度器缓存。
func SNIStatusJSON(fresh bool) any { return sniMgr.status(fresh) }

// sniSnapshot 是设备侧上报/自检的线格式(cloud 原样存,web 据此渲染徽章)。
type sniSnapshot struct {
	Armed           bool      `json:"armed"` // arm 已装且 setup 成功(sniMgr.cur != nil)
	Account         string    `json:"account,omitempty"`
	Exit            string    `json:"exit,omitempty"` // host:port
	Intercept       []string  `json:"intercept,omitempty"`
	ListVersion     string    `json:"list_version,omitempty"` // 设备实际 arm 生效的清单版本(=读到的 OSS ETag);cloud 与期望版本比对判落后
	DNSBound        bool      `json:"dns_bound"`              // :53 CoreDNS TCP+UDP 起
	Listeners       int       `json:"listeners"`              // :443 监听数(健康=2:v4+v6)
	ResolverPointed bool      `json:"resolver_pointed"`       // 系统拦截已安装(resolver 或 hosts/helper)
	OverlayUp       bool      `json:"overlay_up"`             // activeNet != nil(WG overlay 就绪,:443 relay 才拨得出去)
	LastError       string    `json:"last_error,omitempty"`
	Since           int64     `json:"since,omitempty"` // arm 起来的 unix 秒
	Platform        string    `json:"platform"`        // runtime.GOOS(非 Linux + 非 root 会静默 no-op,便于解释)
	Probe           *sniProbe `json:"probe,omitempty"` // 主动探测(仅 armed 时;缓存,可能为 nil=探测中)
}

// sniProbe 是一次主动检测的结果:① 组件自检 + ② 真实应用路径检测 + 状态分类。
type sniProbe struct {
	Canary string `json:"canary"`

	// ① 组件自检(直连 127.0.0.1;只证明 ccfly 自身组件正常,不作最终判定)。
	ComponentOK bool `json:"component_ok"`     // 以下四项全过
	ListenerOK  bool `json:"listener_443_ok"`  // 本地 :443 可连接
	DNS53OK     bool `json:"dns_53_ok"`        // 本地 :53 解析 canary→loopback(无需本地 DNS 的平台恒 true)
	OverlayOK   bool `json:"overlay_ok"`       // WG overlay 在线
	DirectOK    bool `json:"direct_target_ok"` // 直连 127.0.0.1:443 nonce 核对节点+账号出口通过

	// ② 真实应用路径检测。
	ResolverOK      bool     `json:"resolver_ok"`                // 系统原生解析 canary 全部落 loopback
	ResolvedAddrs   []string `json:"resolved_addresses,omitempty"` // 原生解析实际返回的地址
	FakeDNSDetected bool     `json:"fake_dns_detected,omitempty"`  // 出现 fake-ip/公网地址(解析被抢)
	LoopbackRouteOK bool     `json:"loopback_route_ok"`          // 按解析结果建连,nonce 应答合法(到达某个 byway)
	TargetOK        bool     `json:"target_ok"`                  // nonce + 目标 overlay 节点 + 账号出口身份均匹配
	TLSOK           bool     `json:"tls_ok"`                     // 域名路径真实上游 TLS 握手成功
	PathOK          bool     `json:"path_ok"`                    // resolver_ok && loopback_route_ok && target_ok && tls_ok
	Mode            string   `json:"mode"`                       // transparent/fakedns_conflict/local_conflict/target_mismatch/upstream_failed/checking
	ErrorCode       string   `json:"error_code,omitempty"`       // FAKEDNS_HIJACK/LOCAL_*/OVERLAY_DOWN/LOOPBACK_ROUTE/TARGET_MISMATCH/UPSTREAM_TLS
	Stale           bool     `json:"stale,omitempty"`            // 结果已过期(>45s),按 checking 处理

	// 兼容字段:老 dashboard/老前端只读 dns_ok。语义=系统解析检测(原「DNS 拦截可用」的最近似)。
	DNSOK bool `json:"dns_ok"`

	TargetNode     string `json:"target_node,omitempty"`
	TargetExitID   string `json:"target_exit_id,omitempty"`
	TargetIdentity string `json:"target_identity,omitempty"`
	BoundEgress4   string `json:"bound_egress_ipv4,omitempty"`
	Error          string `json:"error,omitempty"`
	MS             int64  `json:"ms"`
	At             int64  `json:"at"` // unix 秒
}

// status 组装快照;armed 时附带探测(fresh=同步刷新,否则读调度器缓存)。
func (m *sniManager) status(fresh bool) sniSnapshot {
	m.mu.Lock()
	dnsBound := m.dnsSvc != nil && m.dnsSvc.Running()
	s := sniSnapshot{
		Platform:  runtime.GOOS,
		OverlayUp: activeNet.Load() != nil,
		DNSBound:  dnsBound,
		Listeners: len(m.listeners),
	}
	s.ResolverPointed = m.resolvOn
	helperMode := m.helperConn != nil
	if helperMode {
		// macOS helper owns the real v4/v6 :443 fronts, CoreDNS and resolver while
		// the agent owns one unprivileged relay listener.  Report production
		// ingress state, not the implementation-detail relay count.
		s.Listeners = sniHelperFrontListenerCount()
		s.ResolverPointed = true
		s.DNSBound = true
	}
	s.LastError = m.lastErr
	var exit SNIExit
	if m.cur != nil {
		s.Armed = true
		s.Account = m.cur.Account
		exit = m.cur.Exit
		s.Exit = net.JoinHostPort(exit.Host, strconv.Itoa(exit.Port))
		switch {
		case m.dnsSvc != nil: // linux/windows:进程内策略服务即权威
			s.Intercept = m.dnsSvc.Domains()
			s.ListVersion = m.dnsSvc.Version()
		case helperMode: // darwin:权威在 helper;resolver 文件即实际生效清单
			s.Intercept = managedResolverDomains()
			s.ListVersion = m.helperVersion
		}
	}
	if !m.since.IsZero() {
		s.Since = m.since.Unix()
	}
	m.mu.Unlock()

	if s.Armed && helperMode {
		// darwin:配置权威在 helper 的 dnsPolicyService;agent 只做版本观测(HEAD 取 ETag),
		// 让上报的 list_version 跟上 helper 的热重载。失败保留 arm 时的版本。
		if v := observeDomainListVersion(); v != "" {
			s.ListVersion = v
		}
	}
	if s.Armed {
		s.Probe = cachedProbe(exit, s.Account, fresh)
	}
	return s
}

// ── 探测缓存(调度器持有刷新;fresh 路径同步单飞)──

var (
	probeCache atomic.Pointer[sniProbe]
	probeGen   atomic.Uint64 // config generation; an old async probe must not populate a new account's cache
	probeMu    sync.Mutex    // fresh 路径单飞
)

func resetSNIProbe() {
	probeGen.Add(1)
	probeCache.Store(nil)
}

func cachedProbe(exit SNIExit, identity string, fresh bool) *sniProbe {
	if fresh {
		probeMu.Lock()
		defer probeMu.Unlock()
		gen := probeGen.Load()
		p := runSNIProbe(exit, identity)
		if probeGen.Load() == gen {
			probeCache.Store(p)
		}
		return p
	}
	p := probeCache.Load()
	if p == nil {
		return nil // 首次探测尚未完成;上报/展示端按「探测中」处理
	}
	if time.Since(time.Unix(p.At, 0)) >= sniProbeTTL {
		stale := *p
		stale.Mode = sniModeChecking // 结果过期:不标记健康,按「检测中」展示
		stale.Stale = true
		stale.PathOK = false
		return &stale
	}
	return p
}

// ── 检测调度器(arm 生命周期持有:立即一次 + 30s 周期 + 失败退避 + kick 立即重检)──

type sniProber struct {
	gen     uint64 // 启动时的配置代;teardown/rearm 递增后,本 prober 的迟到结果不得入缓存
	exit    SNIExit
	account string
	kick    chan struct{} // 配置刷新/网络变化 → 立即重检并清退避
	stop    chan struct{}
}

func startSNIProber(exit SNIExit, account string) *sniProber {
	p := &sniProber{
		gen:     probeGen.Load(),
		exit:    exit,
		account: account,
		kick:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
	}
	go p.loop()
	return p
}

// Close 停止调度。不等待在途探测(最长 ~10s,持锁 teardown 不能阻塞);迟到的结果被 gen 挡住。
func (p *sniProber) Close() { close(p.stop) }

// Kick 触发一次立即重检(非阻塞;已在跑/已有排队则合并)。
func (p *sniProber) Kick() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

func (p *sniProber) loop() {
	failures := 0
	delay := time.Duration(0) // arm 后立即检测一次
	for {
		t := time.NewTimer(delay)
		select {
		case <-p.stop:
			t.Stop()
			return
		case <-p.kick:
			t.Stop()
			failures = 0
		case <-t.C:
		}
		probe := runSNIProbe(p.exit, p.account)
		if probeGen.Load() == p.gen {
			probeCache.Store(probe)
		}
		if probe.PathOK {
			failures = 0
		} else {
			failures++
		}
		delay = sniProbeDelay(failures)
	}
}

// sniProbeDelay:成功=30s 周期;失败按 2s→5s→10s→30s 退避。
func sniProbeDelay(failures int) time.Duration {
	if failures <= 0 || failures > len(sniProbeBackoff) {
		return sniProbePeriod
	}
	return sniProbeBackoff[failures-1]
}

// kickSNIProbe 在网络变化(WG up/down)时立即重检一次(若正 armed)。
func kickSNIProbe() {
	sniMgr.mu.Lock()
	p := sniMgr.prober
	sniMgr.mu.Unlock()
	if p != nil {
		p.Kick()
	}
}

// ── 一次完整检测:① 组件自检 → ② 系统解析 → ③ 真实域名路径(nonce 身份 + TLS)──
//
// 两个网络探测都按「真实应用路径」走:系统原生解析给出地址,连那个地址进本地 :443 生产入口;
// 身份不匹配时即使 TLS 可通,path_ok 也必须 fail closed。
func runSNIProbe(exit SNIExit, expectedIdentity string) *sniProbe {
	start := time.Now()
	p := &sniProbe{Canary: sniCanary, At: start.Unix(), OverlayOK: activeNet.Load() != nil}

	// ① 组件自检(直连 127.0.0.1;只回答「ccfly 自身组件是否正常」)。
	var directErr error
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); p.ListenerOK = probeTCPDial(egressProbeAddr) }()
	go func() { defer wg.Done(); p.DNS53OK = !resolverNeedsLocalDNS() || probeDNSLoopback(sniCanary) }()
	go func() { defer wg.Done(); _, directErr = probeEgressIdentity(exit.Host, expectedIdentity) }()
	wg.Wait()
	p.DirectOK = directErr == nil
	p.ComponentOK = p.ListenerOK && p.DNS53OK && p.OverlayOK && p.DirectOK

	// ② 系统解析检测:平台原生解析 canary,严格要求全部落 loopback(FakeDNS/TUN 抢解析在此现形)。
	addrs, resolveErr := nativeResolveHost(sniCanary)
	p.ResolvedAddrs = addrs
	ok, fake := classifyResolved(addrs)
	p.ResolverOK = resolveErr == nil && ok
	p.FakeDNSDetected = fake

	// ③ 真实域名路径探测:按系统解析给出的地址(真实应用会连的地址)建连,发 nonce 核对目标
	//    身份,再做真实上游 TLS。解析已被劫持时跳过(无合法入口可连;分类见 classifyProbe)。
	var pathErr, tlsErr error
	if p.ResolverOK {
		var id egressIdentity
		var dialAddr string
		dialAddr, p.LoopbackRouteOK, id, pathErr = probeAppPathIdentity(addrs, exit.Host, expectedIdentity)
		p.TargetNode, p.TargetExitID = id.NodeID, id.ExitID
		p.TargetIdentity, p.BoundEgress4 = id.Identity, id.BoundEgress4
		p.TargetOK = pathErr == nil
		if p.LoopbackRouteOK {
			p.TLSOK, tlsErr = probeSNIPathVia(dialAddr, sniCanary)
		}
	}

	p.DNSOK = p.ResolverOK // 兼容字段
	p.PathOK = p.ResolverOK && p.LoopbackRouteOK && p.TargetOK && p.TLSOK
	p.Mode, p.ErrorCode, p.Error = classifyProbe(p, resolveErr, directErr, pathErr, tlsErr)
	p.MS = time.Since(start).Milliseconds()
	return p
}

// classifyProbe 把一次检测结果归一成 (mode, error_code, 人类可读原因)。优先级沿真实路径顺序:
// 解析被抢 > 自身组件坏 > 解析未落 loopback > loopback 路由不通 > 目标身份不符 > 上游 TLS 失败。
func classifyProbe(p *sniProbe, resolveErr, directErr, pathErr, tlsErr error) (mode, code, why string) {
	switch {
	case p.FakeDNSDetected:
		addr, kind := firstNonLoopback(p.ResolvedAddrs)
		return sniModeFakeDNS, "FAKEDNS_HIJACK",
			fmt.Sprintf("系统解析被劫持:%s 解析到%s %s(应为 loopback),检查代理软件的 fake-ip/TUN 模式", p.Canary, kind, addr)
	case !p.ComponentOK:
		switch {
		case !p.ListenerOK:
			return sniModeLocalConflict, "LOCAL_LISTENER_443", "本地 :443 不可连接(端口被占用/劫持或未监听)"
		case !p.DNS53OK:
			return sniModeLocalConflict, "LOCAL_DNS_53", "本地 :53 DNS 未工作(被其他 DNS/代理占用或未起)"
		case !p.OverlayOK:
			return sniModeLocalConflict, "OVERLAY_DOWN", "WireGuard overlay 不在线"
		default:
			return sniModeLocalConflict, "LOCAL_TARGET", errText(directErr)
		}
	case !p.ResolverOK:
		if resolveErr != nil {
			return sniModeFakeDNS, "RESOLVER_NOT_LOOPBACK", "系统原生解析失败: " + resolveErr.Error()
		}
		return sniModeFakeDNS, "RESOLVER_NOT_LOOPBACK", "系统原生解析未指向 loopback(resolver/hosts 未生效)"
	case !p.LoopbackRouteOK:
		return sniModeLocalConflict, "LOOPBACK_ROUTE",
			"按系统解析结果无法到达本地 :443 生产入口(loopback 被透明代理重定向?): " + errText(pathErr)
	case !p.TargetOK:
		return sniModeTargetMismatch, "TARGET_MISMATCH", errText(pathErr)
	case !p.TLSOK:
		return sniModeUpstreamFailed, "UPSTREAM_TLS", "目标节点正确但真实上游 TLS 失败: " + errText(tlsErr)
	default:
		return sniModeTransparent, "", ""
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// probeAppPathIdentity 按系统解析给出的地址依次建连(真实应用会连的地址),第一条拨通的连接做
// nonce 身份核对。routeOK=false = 根本到不了本地 :443(loopback 被抢/未监听);routeOK=true +
// err!=nil(egressTargetError)= 到了一个 byway 但身份不符(target_mismatch)。
func probeAppPathIdentity(addrs []string, expectedNode, expectedIdentity string) (dialAddr string, routeOK bool, id egressIdentity, err error) {
	var targetErr *egressTargetError
	for _, raw := range addrs {
		ip := net.ParseIP(raw)
		if ip == nil {
			continue
		}
		addr := net.JoinHostPort(ip.String(), strconv.Itoa(sniProbePathPort))
		id, err = probeEgressIdentityAt(addr, expectedNode, expectedIdentity)
		switch {
		case err == nil:
			return addr, true, id, nil
		case errors.As(err, &targetErr):
			return addr, true, id, err
		}
		// 路由级失败:尝试下一个解析地址(如 ::1 不通退回 127.0.0.1)
	}
	if err == nil {
		err = fmt.Errorf("no dialable address in %v", addrs)
	}
	return "", false, id, err
}

// probeTCPDial 验证 addr 可 TCP 建连(组件自检的 :443 监听检查)。
func probeTCPDial(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
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

// probeSNIPathVia 连解析给出的 loopback 地址的 :443(SNI arm 的生产入口),发 SNI=host 的真实
// TLS 握手。握手成功(证书对 host 校验通过)= 该入口 + overlay + 账号出口 + 源规则 + 真出网全通。
func probeSNIPathVia(addr, host string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sniProbeTimeout)
	defer cancel()
	d := net.Dialer{Timeout: sniProbeTimeout}
	raw, err := d.DialContext(ctx, "tcp", addr)
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
