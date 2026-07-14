package mesh

// sni.go — 客户端 SNI arm(第⑥步):设备装本地 DNS 拦截器 + 本地 :443 透传,把 AI 域名的流量经
// overlay 送到账号出口 byway-sni(SNI passthrough、真证书、无 HTTP 代理、无 MITM)。
//
// 配置来自云端 GET /api/device/config 的 `sni` 段(ccfly-cloud 第⑤步 addSNIAdvertise 下发,仅对准入
// 且绑定了 SNI 账号的设备):有段 → 装并配置;无段 → 幂等卸载。由 runTunnel 经 applyMeshSNI 驱动。
//
// 三段:
//   ① 内嵌极小 DNS(UDP 127.0.0.1:53):intercept 域(含子域)→ A=127.0.0.1 / AAAA=::1;其余原样转上游。
//      —— 比外部 SmartDNS 轻(无需下载/装二进制),ccfly 本就是 Go 进程。
//   ② 本地 :443 双栈 TCP(127.0.0.1 + [::1]):把连接经 overlay netstack 透传到 exit(账号 byway-sni),
//      byway-sni peek SNI 后按设备源 IP 的池规则从账号 IP 出网。
//   ③ 系统解析指向(pointResolver,三平台各异):Linux=/etc/resolv.conf 指向 127.0.0.1(备份原文件,
//      真上游列为次级 nameserver 做 fail-open);macOS=/etc/resolver/<域> scoped resolver;
//      Windows=hosts 托管块把精确主机名钉到 loopback(需管理员 token,见 sni_hosts.go)。
//
// 失败安全:任一组件起不来(非 root/非管理员无法 bind :53/:443 或写 hosts)→ 不改系统解析、不 brick,只 log。
// 卸载:恢复 resolv.conf、停 DNS、关 :443。幂等(重复无段 = 保持卸载态)。

import (
	"context"
	"encoding/binary"
	"log"
	"net"
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
	cur        *SNIConfig     // 当前生效配置(nil=未装)
	dnsConn    *net.UDPConn   // :53 UDP
	listeners  []net.Listener // :443 v4 + v6(darwin helper 路径下=非特权 relay 监听)
	resolvOn   bool           // 是否已改过 resolv.conf(卸载时才恢复)
	helperConn net.Conn       // darwin only:到 root sni-helper 的控制连接(关它即撤 arm 租约→恢复 hosts/关 :443)
	since      time.Time      // arm 成功起来的时刻(卸载清零);供 /sni-status 与上报
	lastErr    string         // 最近一次 setup 失败原因(成功清空);解释非 root/非 Linux 静默 no-op
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
	log.Printf("ccfly: SNI arm up (account=%s exit=%s intercept=%v)", cfg.Account, net.JoinHostPort(cfg.Exit.Host, strconv.Itoa(cfg.Exit.Port)), cfg.Intercept)
}

// setupLocked 起 DNS + :443 + 把系统解析指向本地(三平台各异,见 pointResolver)。任一步失败返回 err,
// 交调用方 teardown 回滚。
func (m *sniManager) setupLocked(cfg *SNIConfig) error {
	// macOS:agent 非 root 绑不了 :443、写不了 /etc/hosts → 拆双进程,特权部分交 root sni-helper 承接
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
	intercept := normalizeDomains(cfg.Intercept)
	upstream := firstUpstream(cfg.Upstream)
	// ② DNS 127.0.0.1:53(需 root;resolv.conf/NRPT 的 nameserver 只接受 IP 不带端口 → 必须 :53)。
	//    仅在需要「把系统解析导到本地 nameserver」的平台起(Linux resolv.conf / macOS /etc/resolver)。
	//    Windows 走 hosts 直钉(见 sni_resolv_windows.go),不需要 :53 —— 也因此躲开 Clash 等占 :53 的代理。
	if resolverNeedsLocalDNS() {
		uc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53})
		if err != nil {
			return err
		}
		m.dnsConn = uc
		go m.serveDNS(uc, intercept, upstream)
	}
	// ③ 把系统解析指向本地:Linux=resolv.conf 全局(+次级上游 fail-open);macOS=/etc/resolver/<域> scoped;
	//    Windows=hosts 精确主机名钉 loopback(无通配、逐主机名、局部块替换)。
	if err := pointResolver(intercept, upstream); err != nil {
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

// teardownLocked 恢复 resolv.conf、停 DNS、关 :443。幂等。
func (m *sniManager) teardownLocked() {
	// darwin helper 路径:关控制连接即通知 root sni-helper 撤租约(恢复 /etc/hosts + 关它持的 :443)。
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
	if m.dnsConn != nil {
		_ = m.dnsConn.Close()
		m.dnsConn = nil
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

// ── 内嵌极小 DNS(拦截 intercept 域到 loopback,其余转上游)──

func (m *sniManager) serveDNS(uc *net.UDPConn, intercept []string, upstream string) {
	buf := make([]byte, 1500)
	for {
		n, src, err := uc.ReadFromUDP(buf)
		if err != nil {
			return // conn 关闭
		}
		q := make([]byte, n)
		copy(q, buf[:n])
		go handleDNSQuery(uc, src, q, intercept, upstream)
	}
}

// handleDNSQuery:命中 intercept 且 A/AAAA → 本地合成 loopback 应答;否则原样转上游再回。
func handleDNSQuery(uc *net.UDPConn, src *net.UDPAddr, query []byte, intercept []string, upstream string) {
	name, qtype, ok := parseDNSQuestion(query)
	if ok && matchesIntercept(name, intercept) && (qtype == 1 || qtype == 28) {
		if resp := buildLoopbackAnswer(query, qtype); resp != nil {
			_, _ = uc.WriteToUDP(resp, src)
			return
		}
	}
	// 转发上游(原样字节;不解析响应)。
	if resp := forwardDNS(query, upstream); resp != nil {
		_, _ = uc.WriteToUDP(resp, src)
	}
}

// parseDNSQuestion 从 DNS 查询里取第一个问题的 qname(小写、无尾点)与 qtype。
func parseDNSQuestion(msg []byte) (name string, qtype uint16, ok bool) {
	if len(msg) < 12 {
		return "", 0, false
	}
	if qd := binary.BigEndian.Uint16(msg[4:6]); qd < 1 {
		return "", 0, false
	}
	pos := 12
	var labels []string
	for {
		if pos >= len(msg) {
			return "", 0, false
		}
		l := int(msg[pos])
		pos++
		if l == 0 {
			break
		}
		if l&0xc0 != 0 { // 问题段不应有压缩指针
			return "", 0, false
		}
		if pos+l > len(msg) {
			return "", 0, false
		}
		labels = append(labels, string(msg[pos:pos+l]))
		pos += l
	}
	if pos+2 > len(msg) {
		return "", 0, false
	}
	qtype = binary.BigEndian.Uint16(msg[pos : pos+2])
	return strings.ToLower(strings.Join(labels, ".")), qtype, true
}

// matchesIntercept 判断 name 是否等于或是某 apex 域的子域。
func matchesIntercept(name string, intercept []string) bool {
	for _, d := range intercept {
		if name == d || strings.HasSuffix(name, "."+d) {
			return true
		}
	}
	return false
}

// buildLoopbackAnswer 就地把查询改造成一条 loopback 应答(A→127.0.0.1 / AAAA→::1)。
func buildLoopbackAnswer(query []byte, qtype uint16) []byte {
	if len(query) < 12 {
		return nil
	}
	// 复制原查询(含 header + question)作为应答基。
	resp := make([]byte, len(query))
	copy(resp, query)
	resp[2] |= 0x84                           // QR=1, AA=1
	resp[3] = 0x80                            // RA=1, RCODE=0
	binary.BigEndian.PutUint16(resp[6:8], 1)  // ANCOUNT=1
	binary.BigEndian.PutUint16(resp[8:10], 0) // NSCOUNT=0
	binary.BigEndian.PutUint16(resp[10:12], 0)
	// answer:name 压缩指针指向 question(0xc00c)+ type + class IN + TTL + rdlength + rdata。
	ans := []byte{0xc0, 0x0c}
	ans = append(ans, byte(qtype>>8), byte(qtype)) // TYPE
	ans = append(ans, 0x00, 0x01)                  // CLASS IN
	ans = append(ans, 0x00, 0x00, 0x00, 0x1e)      // TTL=30s
	if qtype == 1 {                                // A → 127.0.0.1
		ans = append(ans, 0x00, 0x04, 127, 0, 0, 1)
	} else { // AAAA → ::1
		ans = append(ans, 0x00, 0x10)
		ans = append(ans, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1)
	}
	return append(resp, ans...)
}

// forwardDNS 把查询原样发给上游 :53 并返回响应(UDP,3s 超时)。
func forwardDNS(query []byte, upstream string) []byte {
	c, err := net.DialTimeout("udp", net.JoinHostPort(upstream, "53"), 3*time.Second)
	if err != nil {
		return nil
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(query); err != nil {
		return nil
	}
	buf := make([]byte, 1500)
	n, err := c.Read(buf)
	if err != nil {
		return nil
	}
	return buf[:n]
}

// ── helpers ──

func normalizeDomains(ds []string) []string {
	out := []string{}
	for _, d := range ds {
		if d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), "."); d != "" {
			out = append(out, d)
		}
	}
	return out
}

func firstUpstream(us []string) string {
	for _, u := range us {
		if u = strings.TrimSpace(u); u != "" {
			return u
		}
	}
	return "223.5.5.5" // 阿里默认
}
