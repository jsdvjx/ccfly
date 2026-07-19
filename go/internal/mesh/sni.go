package mesh

// sni.go — 客户端 SNI arm(第⑥步):设备装本地 DNS 拦截器 + 本地 :443 透传,把 AI 域名的流量经
// overlay 送到账号出口 byway-sni(SNI passthrough、真证书、无 HTTP 代理、无 MITM)。
//
// 配置来自云端 GET /api/device/config 的 `sni` 段(ccfly-cloud 第⑤步 addSNIAdvertise 下发,仅对准入
// 且绑定了 SNI 账号的设备):有段 → 装并配置;无段 → 幂等卸载。由 runTunnel 经 applyMeshSNI 驱动。
// 段的 intercept/upstream 字段仅兼容保留——**域名清单与上游由 DNS 策略服务自持 OSS**(dnspolicy.go,
// 三端统一),不参与 sameSNI 判等。
//
// 三段:
//   ① 内嵌 CoreDNS(TCP+UDP 127.0.0.1:53,由 dnsPolicyService 自持):intercept 域(含子域)→
//      A=127.0.0.1 / AAAA=::1;其余查询交给 OSS 配置的上游。
//   ② 本地 :443 双栈 TCP(127.0.0.1 + [::1]):把连接经 overlay netstack 透传到 exit(账号 byway-sni),
//      byway-sni peek SNI 后按设备源 IP 的池规则从账号 IP 出网。
//   ③ 系统解析指向(pointResolver,三平台各异):Linux=/etc/resolv.conf 指向 127.0.0.1(备份原文件,
//      真上游列为次级 nameserver 做 fail-open);macOS root helper 写 scoped /etc/resolver;Windows
//      网卡 DNS 指 127.0.0.1 + 次级上游(见 sni_resolv_windows.go)。
//
// 失败安全:任一组件起不来(非 root/非管理员无法 bind :53/:443 或写系统 DNS)→ 不改系统解析、不 brick,只 log。
// 卸载:恢复系统 DNS、停 DNS 服务、关 :443。幂等(重复无段 = 保持卸载态)。

import (
	"context"
	"log"
	"net"
	"strconv"
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
// 域名清单/上游不再是配置面(由 DNS 策略服务自持 OSS),不参与判等。
func sameSNI(a, b *SNIConfig) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Enabled == b.Enabled && a.Account == b.Account && a.Exit == b.Exit
}

// ── SNI 管理器(单例,config 驱动的生命周期)──

type sniManager struct {
	mu         sync.Mutex
	cur        *SNIConfig        // 当前生效配置(nil=未装)
	dnsSvc     *dnsPolicyService // 自持 OSS 策略的 :53 DNS 服务(linux/windows 进程内;darwin 归 helper)
	listeners  []net.Listener    // :443 v4 + v6(darwin helper 路径下=非特权 relay 监听)
	resolvOn   bool              // 是否已改过系统解析(卸载时才恢复)
	helperConn net.Conn          // darwin only:关连接即撤 helper 租约→恢复 resolver/停 CoreDNS/关 :443
	helperVersion string         // darwin only:helper arm 应答给的 OSS 清单 ETag(状态上报用)
	since      time.Time         // arm 成功起来的时刻(卸载清零);供 /sni-status 与上报
	lastErr    string            // 最近一次 setup 失败原因(成功清空);解释非 root/非 Linux 静默 no-op
	prober     *sniProber        // 检测调度器(armed 期间持有;teardown 停)
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

// setupLocked 起 DNS 策略服务 + :443 + 把系统解析指向本地(三平台各异,见 pointResolver)。任一步
// 失败返回 err,交调用方 teardown 回滚。
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
	// ② DNS 策略服务 127.0.0.1:53(需 root;resolv.conf/网卡 DNS 的 nameserver 只接受 IP 不带端口 →
	//    必须 :53)。三端统一:服务自持 OSS 清单轮询与热重载(dnspolicy.go),agent 不再经手配置。
	svc := newDNSPolicyService(sniCoreDNSListenIP, sniCoreDNSPort)
	if err := svc.start(); err != nil {
		return err
	}
	m.dnsSvc = svc
	// ③ 把系统解析指向本地:Linux=resolv.conf 全局(+次级上游 fail-open);Windows=网卡 DNS
	//    127.0.0.1 + 次级上游;macOS=/etc/resolver/<域> scoped(helper 侧,见 setupViaHelper)。
	upstreams := svc.currentUpstreams()
	if err := pointResolver(svc.Domains(), upstreamIP(upstreams[0]), nil); err != nil {
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
	// darwin helper 路径:关控制连接即通知 root helper 撤租约(恢复 resolver + 关 :443)。
	if m.helperConn != nil {
		_ = m.helperConn.Close()
		m.helperConn = nil
		m.helperVersion = ""
	}
	if m.resolvOn {
		if err := restoreResolver(); err != nil {
			log.Printf("ccfly: SNI restore resolver: %v", err)
		}
		m.resolvOn = false
	}
	if m.dnsSvc != nil {
		m.dnsSvc.Stop()
		m.dnsSvc = nil
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
