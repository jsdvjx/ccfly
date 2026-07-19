package mesh

// sni.go — 客户端 SNI arm(第⑥步):设备装本地 DNS 拦截器 + 本地 :443 透传,把 AI 域名的流量经
// overlay 送到账号出口 byway-sni(SNI passthrough、真证书、无 HTTP 代理、无 MITM)。
//
// 配置来自云端 GET /api/device/config 的 `sni` 段(ccfly-cloud 第⑤步 addSNIAdvertise 下发,仅对准入
// 且绑定了 SNI 账号的设备):有段 → 装并配置;无段 → 幂等卸载。由 runTunnel 经 applyMeshSNI 驱动。
//
// 三段:
//   ① 进程内 CoreDNS(TCP+UDP 127.0.0.1:53):intercept 域(含子域)→ A=127.0.0.1 / AAAA=::1;
//      其余查询交给 OSS 配置的上游。
//   ② 本地 :443 双栈 TCP(127.0.0.1 + [::1]):把连接经 overlay netstack 透传到 exit(账号 byway-sni),
//      byway-sni peek SNI 后按设备源 IP 的池规则从账号 IP 出网。
//   ③ 系统解析指向(pointResolver,三平台各异):Linux=/etc/resolv.conf 指向 127.0.0.1(备份原文件,
//      真上游列为次级 nameserver 做 fail-open);macOS root helper 写 scoped /etc/resolver;Windows
//      使用 hosts 托管块把精确主机名钉到 loopback(见 sni_hosts.go)。
//
// 失败安全:任一组件起不来(非 root/非管理员无法 bind :53/:443 或写 hosts)→ 不改系统解析、不 brick,只 log。
// 卸载:恢复 resolv.conf、停 DNS、关 :443。幂等(重复无段 = 保持卸载态)。

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/tun/netstack"
)

// SNIConfig 是云端下发的 sni 段。
type SNIConfig struct {
	Enabled   bool     `json:"enabled"`
	Account   string   `json:"account"`
	Exit      SNIExit  `json:"exit"`
	Intercept []string `json:"intercept"` // apex 域清单(含所有子域)
	Upstream  []string `json:"upstream"`  // 拦截 DNS 的上游(阿里)
}

// SNIExit 是账号 SNI 出口端点(overlay 上的 byway-sni,host:port)。
type SNIExit struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// activeNet 是当前 WG 会话的 netstack(bringUpWG 发布、session close 清)。SNI :443 relay 据此经 overlay 拨 exit。
var activeNet atomic.Pointer[netstack.Net]

// sameSNI 判断两份 sni 段是否等价(refreshConfig 用来决定是否 changed;避免无谓重启)。
func sameSNI(a, b *SNIConfig) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Enabled != b.Enabled || a.Account != b.Account || a.Exit != b.Exit {
		return false
	}
	return strings.Join(a.Intercept, ",") == strings.Join(b.Intercept, ",") &&
		strings.Join(a.Upstream, ",") == strings.Join(b.Upstream, ",")
}

// ── SNI 管理器(单例,config 驱动的生命周期)──

type sniManager struct {
	mu         sync.Mutex
	cur        *SNIConfig      // 当前生效配置(nil=未装)
	dns        *coreDNSService // :53 CoreDNS(TCP+UDP);darwin helper 路径由 helper 自持
	listeners  []net.Listener  // :443 v4 + v6(darwin helper 路径下=非特权 relay 监听)
	resolvOn   bool            // 是否已改过 resolv.conf(卸载时才恢复)
	helperConn net.Conn        // darwin only:关连接即撤 helper 租约→恢复 resolver/停 CoreDNS/关 :443
	since      time.Time       // arm 成功起来的时刻(卸载清零);供 /sni-status 与上报
	lastErr    string          // 最近一次 setup 失败原因(成功清空);解释非 root/非 Linux 静默 no-op
	prober     *sniProber      // 检测调度器(armed 期间持有;teardown 停)
}

var sniMgr = &sniManager{}

// applySNI 幂等地把 SNI arm 收敛到目标配置:cfg 有效且 enabled → 装(配置变了先卸再装);否则卸。
func applySNI(cfg *SNIConfig) {
	if cfg != nil && cfg.Enabled && (cfg.Exit.Host == "" || cfg.Exit.Port == 0) {
		cfg = nil // 段不完整 = 当无段处理
	}
	sniMgr.apply(cfg)
}

func (m *sniManager) apply(cfg *SNIConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 目标=卸载。
	if cfg == nil || !cfg.Enabled {
		if m.cur != nil {
			m.teardownLocked()
		}
		return
	}
	// 已按相同配置在跑 → no-op。
	if m.cur != nil && sameSNI(m.cur, cfg) {
		return
	}
	// 配置变了 → 先卸再装。
	if m.cur != nil {
		m.teardownLocked()
	}
	if err := m.setupLocked(cfg); err != nil {
		log.Printf("ccfly: SNI setup failed (fail-open, 不影响正常上网): %v", err)
		m.lastErr = err.Error() // 供 /sni-status 暴露(如非 root 无法 bind :443/:53)
		m.teardownLocked()      // 回滚已起的部分,恢复 resolv.conf
		return
	}
	m.cur = cfg
	m.since = time.Now()
	m.lastErr = ""
	resetSNIProbe()
	m.prober = startSNIProber(cfg.Exit, cfg.Account) // 配置生效后立即检测一次,其后 30s 周期+失败退避
	log.Printf("ccfly: SNI arm up (account=%s exit=%s intercept=%v)", cfg.Account, net.JoinHostPort(cfg.Exit.Host, strconv.Itoa(cfg.Exit.Port)), cfg.Intercept)
}

// setupLocked 起 DNS + :443 + 把系统解析指向本地(三平台各异,见 pointResolver)。任一步失败返回 err,
// 交调用方 teardown 回滚。
func (m *sniManager) setupLocked(cfg *SNIConfig) error {
	// macOS:agent 非 root 绑不了 :443/:53、写不了 /etc/resolver → 特权部分交 root sni-helper 承接
	// (overlay 拨号仍在本进程,见 snihelper_darwin.go)。其余平台走下面的单进程内联直绑。
	if sniUsesHelper() {
		return m.setupViaHelper(cfg)
	}
	// ① :443 双栈监听(需 root)。exit 经 overlay 拨,故先起监听、拨号在 accept 时按 activeNet 走(fail-open)。
	for _, addr := range []string{"127.0.0.1:443", "[::1]:443"} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return err // 通常是非 root(bind :443 需特权)
		}
		m.listeners = append(m.listeners, ln)
		go m.serve443(ln, cfg.Exit)
	}
	intercept := effectiveIntercept(cfg)
	upstreams := effectiveUpstreams(cfg)
	// ② DNS 127.0.0.1:53(需 root;resolv.conf/NRPT 的 nameserver 只接受 IP 不带端口 → 必须 :53)。
	//    仅在需要「把系统解析导到本地 nameserver」的平台起(Linux resolv.conf / macOS /etc/resolver)。
	//    Windows 走 hosts 直钉(见 sni_resolv_windows.go),不需要 :53 —— 也因此躲开 Clash 等占 :53 的代理。
	if resolverNeedsLocalDNS() {
		dns, err := startCoreDNS(sniCoreDNSListenIP, sniCoreDNSPort, intercept, upstreams)
		if err != nil {
			return err
		}
		m.dns = dns
	}
	// ③ 把系统解析指向本地:Linux=resolv.conf 全局(+次级上游 fail-open);macOS=/etc/resolver/<域> scoped;
	//    Windows=hosts 精确主机名钉 loopback(无通配、逐主机名、局部块替换)。
	if err := pointResolver(intercept, upstreamIP(upstreams[0]), effectivePinnedHosts()); err != nil {
		return err
	}
	m.resolvOn = true
	return nil
}

// CleanupResolver 兜底清掉本机的系统解析改动(Windows hosts 托管块 / macOS /etc/resolver
// 标记文件 / Linux resolv.conf 备份恢复),给 `ccfly uninstall` 收尾用:常驻服务是被硬杀的
// (schtasks /End、launchctl),不会走 teardown —— Windows 上残留的 hosts 块会把 Anthropic
// 域钉死在 loopback(无人监听 :443),整机 Claude 全断。幂等,未写过时是 no-op。
func CleanupResolver() error { return restoreResolver() }

// teardownLocked 恢复 resolver、停 CoreDNS、关 :443、停检测调度器。幂等。
func (m *sniManager) teardownLocked() {
	if m.prober != nil {
		m.prober.Close() // 不等在途探测;迟到的结果被 probeGen 挡住
		m.prober = nil
	}
	// darwin helper 路径:关控制连接即通知 root helper 撤租约(恢复 resolver + 停 CoreDNS + 关 :443)。
	if m.helperConn != nil {
		_ = m.helperConn.Close()
		m.helperConn = nil
	}
	if m.resolvOn {
		if err := restoreResolver(); err != nil {
			log.Printf("ccfly: SNI restore resolver: %v", err)
		}
		m.resolvOn = false
	}
	if m.dns != nil {
		if err := m.dns.Stop(); err != nil {
			log.Printf("ccfly: stop CoreDNS: %v", err)
		}
		m.dns = nil
	}
	for _, ln := range m.listeners {
		_ = ln.Close()
	}
	m.listeners = nil
	if m.cur != nil {
		log.Printf("ccfly: SNI arm down")
	}
	m.cur = nil
	m.since = time.Time{}
	resetSNIProbe()
}

// serve443 accept 本地 :443 连接,经 overlay netstack 透传到 exit(账号 byway-sni)。
func (m *sniManager) serve443(ln net.Listener, exit SNIExit) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return // listener 关闭
		}
		go relaySNIToExit(c, exit)
	}
}

// relaySNIToExit 把一条本地 :443 连接经当前 overlay netstack 拨到 exit 并双向透传。
// overlay 未就绪(activeNet=nil,WG 断)→ 丢弃该连接(fail-open,claude 会重试;不 hang)。
func relaySNIToExit(local net.Conn, exit SNIExit) {
	defer local.Close()
	tnet := activeNet.Load()
	if tnet == nil {
		return
	}
	ip, err := net.ResolveIPAddr("ip", exit.Host)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	oc, err := tnet.DialContextTCP(ctx, &net.TCPAddr{IP: ip.IP, Port: exit.Port})
	if err != nil {
		log.Printf("ccfly: SNI overlay dial %s:%d: %v", exit.Host, exit.Port, err)
		return
	}
	defer oc.Close()
	relay(local, oc) // 复用 forward.go 的双向拷贝 + 5min 看门狗
}

// ── 设备直接读 OSS 域名清单(内置 URL,与 cloud 解耦)──
//
// URL 编译内置;设备周期 GET → sanity 过滤 → 缓存。arm 用缓存的 pinned(Windows hosts)、intercept
// 与 upstream(CoreDNS),拉不到退回 cloud/编译期兜底。拉到的 ETag 随 SNI 快照上报,cloud 只收集展示——各设备
// ETag 相同即已收敛到同一版。cloud 不读 OSS、不下发清单、不当版本时钟(客户端再多也不给服务器加压)。
// **信任点=OSS 本身**(public-read 但仅 agent 持 AK 可写 + HTTPS 传输防篡改),故加任何厂商域名(OpenAI
// 等)都只改 OSS、不发版;设备只做合法性 sanity(见 filterAllowedHosts),不判厂商。

const sniDomainListURL = "https://ccfly-domainlist.oss-cn-hongkong.aliyuncs.com/domainlist.json"

// domainListClient 直连拉 OSS。① Proxy:nil——**不读环境 HTTP(S)_PROXY**(代理会 MITM/篡改;同
// internal/cloudhttp)。② DisableCompression——**不带 Accept-Encoding: gzip**:OSS 对 gzip 传输会剥掉
// ETag 响应头(Vary: Accept-Encoding),而我们靠 ETag 当版本号;清单才 1KB,不压缩无所谓。TLS 用系统 CA。
var domainListClient = &http.Client{
	Timeout:   8 * time.Second,
	Transport: &http.Transport{Proxy: nil, DisableCompression: true},
}

type sniDomainList struct {
	mu        sync.RWMutex
	pinned    []string // pinned_hosts(Windows hosts 精确钉),已 sanity 过滤
	intercept []string // CoreDNS 通配 apex,已 sanity 过滤
	upstream  []string // CoreDNS 上游,规范化为 IP:port
	etag      string   // 设备拉到的版本(内容 MD5);上报用
}

var domainListCache = &sniDomainList{}

func (c *sniDomainList) get() (pinned []string, etag string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.pinned...), c.etag
}

func (c *sniDomainList) getIntercept() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.intercept...)
}

func (c *sniDomainList) getUpstream() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.upstream...)
}

// effectivePinnedHosts 决定 Windows hosts 实际钉的精确主机名:优先 OSS pinned,
// 拉不到/为空退回编译期 sniPinnedHosts。
func effectivePinnedHosts() []string {
	if p, _ := domainListCache.get(); len(p) > 0 {
		return p
	}
	return sniPinnedHosts
}

// domainListVersion 是设备当前拉到的清单版本(ETag),供 SNI 快照上报;空=还没成功拉到。
func domainListVersion() string {
	_, etag := domainListCache.get()
	return etag
}

// effectiveIntercept 决定 CoreDNS 通配的 apex:优先 OSS intercept,拉不到退回 cloud 下发配置。
func effectiveIntercept(cfg *SNIConfig) []string {
	if apex := domainListCache.getIntercept(); len(apex) > 0 {
		return apex
	}
	return filterAllowedHosts(cfg.Intercept)
}

// effectiveUpstreams 优先使用 OSS 清单，失败时退回 cloud 配置，再退回编译期阿里 DNS。
func effectiveUpstreams(cfg *SNIConfig) []string {
	if upstreams := domainListCache.getUpstream(); len(upstreams) > 0 {
		return upstreams
	}
	if upstreams := normalizeDNSUpstreams(cfg.Upstream); len(upstreams) > 0 {
		return upstreams
	}
	return append([]string(nil), defaultSNIUpstreams...)
}

// refreshDomainList GET 内置 URL,校验并更新缓存。返回运行策略是否变化(供触发 re-arm)。
// 任一步失败保留上次缓存(不清空,防坏拉取把 arm 打空),短超时不 hang。
func refreshDomainList() (changed bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sniDomainListURL, nil)
	if err != nil {
		return false
	}
	resp, err := domainListClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false
	}
	return updateDomainListCache(body, strings.Trim(resp.Header.Get("ETag"), `"`))
}

// updateDomainListCache validates one OSS document and atomically publishes it.
// Invalid or incomplete documents leave the last known-good policy untouched.
func updateDomainListCache(body []byte, etag string) (changed bool) {
	var parsed struct {
		Pinned    []string `json:"pinned_hosts"`
		Intercept []string `json:"intercept"`
		Upstream  []string `json:"upstream"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return false
	}
	pinned := filterAllowedHosts(parsed.Pinned)
	intercept := filterAllowedHosts(parsed.Intercept)
	upstream := normalizeDNSUpstreams(parsed.Upstream)
	if len(intercept) == 0 || len(upstream) == 0 {
		return false
	}
	c := domainListCache
	c.mu.Lock()
	changed = strings.Join(c.pinned, ",") != strings.Join(pinned, ",") ||
		strings.Join(c.intercept, ",") != strings.Join(intercept, ",") ||
		strings.Join(c.upstream, ",") != strings.Join(upstream, ",")
	c.pinned, c.intercept, c.upstream, c.etag = pinned, intercept, upstream, etag
	c.mu.Unlock()
	return changed
}

// refreshDomainListAndRearm 拉一次;intercept/upstream/pinned 变化就重装当前 arm。
func refreshDomainListAndRearm() {
	if refreshDomainList() {
		sniMgr.rearmActive()
	}
}

// rearmActive 在 OSS 策略变化后重装当前 arm(若正 armed):teardown + 用同一 cfg 重 setup。
func (m *sniManager) rearmActive() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil {
		return
	}
	cfg := m.cur
	m.teardownLocked()
	if err := m.setupLocked(cfg); err != nil {
		log.Printf("ccfly: SNI re-arm on domainlist change failed (fail-open): %v", err)
		m.lastErr = err.Error()
		m.teardownLocked()
		return
	}
	m.cur = cfg
	m.since = time.Now()
	m.lastErr = ""
	resetSNIProbe()
	m.prober = startSNIProber(cfg.Exit, cfg.Account)
	log.Printf("ccfly: SNI re-armed on domainlist change (%d intercept)", len(effectiveIntercept(cfg)))
}
