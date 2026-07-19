//go:build darwin

package mesh

// snihelper_darwin.go — macOS 双进程 SNI arm 的两侧:
//
//   ① root helper（RunSNIHelper,由独立 LaunchDaemon `ccfly sni-helper` 以 root 跑）——承接三件
//      agent 非 root 干不了的特权事:绑本地 :443/:53、写 scoped /etc/resolver。它不碰 overlay。
//   ② agent 客户端（setupViaHelper,在非 root 的 ccfly connect 进程里）——在非特权 loopback 端口起
//      relay 监听跑现有 serve443（overlay 拨号必须留 agent:gVisor netstack 是进程内对象,不可跨进程),
//      再连 helper 控制 socket 发一次 arm{relayPort, intercept, upstream}。
//
// 为什么 macOS 要拆:<1024 特权端口 macOS 只有 root 能绑,而 ccfly agent 故意以用户身份跑(共用同一
// tmux/~/.claude 才能镜像会话),又无 CAP_NET_BIND_SERVICE 之类细粒度授权 → 单进程绑 :443 必 EACCES。
// DNS 侧由 helper 内嵌 CoreDNS，并用 /etc/resolver/<apex> 只劫持目标域，不改系统全局 DNS。
//
// 数据面:claude → 127.0.0.1:443(helper accept) → splice 到 127.0.0.1:relayPort(agent) → overlay
//         → 账号 byway-sni 出口。ClientHello 全程裸透传,exit 端 peek SNI 按设备源 IP 路由。
//
// 生命周期 = 控制连接:arm 时 helper 起 CoreDNS、写 resolver、绑 :443,保持控制连接=租约;连接
// EOF(agent 正常退出或崩溃)→ helper 删除 resolver、停 CoreDNS、关 :443。helper 由 launchd KeepAlive;
// 自身重启时先清带 ccfly 标记的孤儿 resolver 文件。

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// 以下三者生产恒为固定值;声明为 var 便于集成测试注入临时 socket/hosts 文件与非特权端口
// (同 sni_resolv_darwin.go 的 resolverDir 惯例)。
var (
	sniHelperSocket = "/var/run/ccfly-sni-helper.sock"       // root helper 控制 socket(agent 连它发 arm)
	unixHostsPath   = "/etc/hosts"                           // macOS hosts 文件
	sniHelperFront  = []string{"127.0.0.1:443", "[::1]:443"} // helper 承接的本地 :443 双栈
	// flushUnixDNS 刷 macOS DNS 缓存让 hosts 改动立即生效(best-effort);var 便于测试打桩。
	flushUnixDNS = func() {
		_ = exec.Command("dscacheutil", "-flushcache").Run()
		_ = exec.Command("killall", "-HUP", "mDNSResponder").Run()
	}
)

// sniUsesHelper 报告本平台 SNI arm 是否走 root helper 双进程路径。darwin=是。
func sniUsesHelper() bool { return true }

func sniHelperFrontListenerCount() int { return len(sniHelperFront) }

// sniArmReq 是 agent → helper 的 arm 请求(单行 JSON;连接保持=arm 租约)。
// 域名清单/上游不在请求里:DNS 策略由 helper 自持的 dnsPolicyService 直拉 OSS(三端统一),
// agent 只告诉 helper「把 :443 splice 到哪个 relay」。
type sniArmReq struct {
	Cmd       string `json:"cmd"`        // "arm"
	RelayPort int    `json:"relay_port"` // agent 侧非特权 relay 端口(helper 把 :443 连接 splice 到此)
}

// sniArmResp 是 helper → agent 的应答。
type sniArmResp struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Version string `json:"version,omitempty"` // helper 当前生效的 OSS 清单 ETag(供 agent 状态上报)
}

// ── agent 侧:经 helper arm ──

// setupViaHelper 在 macOS 上把 SNI arm 拆成双进程:非特权 relay 监听 + 现有 serve443(overlay 拨号留
// agent)+ 连 helper 承接 :443、CoreDNS 与 /etc/resolver。控制连接存进 m.helperConn,关闭即撤租约。
func (m *sniManager) setupViaHelper(cfg *SNIConfig) error {
	// ① 非特权 relay 监听(loopback 随机端口);helper 把每条 :443 连接 splice 到此。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	m.listeners = append(m.listeners, ln) // teardownLocked 会关它
	go m.serve443(ln, cfg.Exit)           // 复用现有 accept→relaySNIToExit(activeNet 经 overlay 拨 exit)
	relayPort := ln.Addr().(*net.TCPAddr).Port

	// ② 连 helper 控制 socket,发 arm(域名策略由 helper 自持,不传)。
	c, err := net.DialTimeout("unix", sniHelperSocket, 3*time.Second)
	if err != nil {
		return fmt.Errorf("sni-helper 未运行(macOS SNI 需 root helper 承接 :443/:53 与 resolver;请以 sudo 重跑 ccfly install 安装): %w", err)
	}
	if err := writeJSONLine(c, sniArmReq{Cmd: "arm", RelayPort: relayPort}); err != nil {
		c.Close()
		return err
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		c.Close()
		return fmt.Errorf("sni-helper 无响应: %w", err)
	}
	var resp sniArmResp
	if e := json.Unmarshal(trimJSONLine(line), &resp); e != nil || !resp.OK {
		c.Close()
		if resp.Error != "" {
			return errors.New("sni-helper: " + resp.Error)
		}
		return errors.New("sni-helper arm 失败")
	}
	_ = c.SetReadDeadline(time.Time{}) // 清超时:连接保持=租约存活
	m.helperConn = c
	m.helperVersion = resp.Version
	return nil
}

// ── root helper 服务端 ──

// RunSNIHelper 跑 root 特权 helper(独立 LaunchDaemon)。监听 unix 控制 socket,为非 root 的 agent
// 承接 :443/:53 与 /etc/resolver。每个连接=一次 arm 租约(新 arm 顶掉旧租约)。阻塞直到 socket 出错。
//
// DNS 策略服务(dnsPolicyService)随进程启动即常驻:自己周期拉 OSS、自持 CoreDNS 热重载,
// 不再由 agent 经 arm 下发清单(三端统一;agent 只传 relay_port)。resolver 文件在 arm 时按
// 服务当前清单写入,清单热更新时由 OnChange 重写(仅 armed)。
func RunSNIHelper() error {
	allowedUID, err := configuredSNIHelperUID()
	if err != nil {
		return err
	}
	if err := restoreResolver(); err != nil { // helper 上次崩溃可能残留 resolver 标记文件;KeepAlive 重启先清
		log.Printf("ccfly-sni-helper: startup resolver cleanup: %v", err)
	}
	h := &sniHelper{allowedUID: uint32(allowedUID), enforcePeer: true}
	svc := newDNSPolicyService(sniCoreDNSListenIP, sniCoreDNSPort)
	svc.onChange = func(domains []string) { h.rewriteResolver(domains) }
	if err := svc.start(); err != nil {
		// :53 被占(罕见:别的本地 DNS):不致命——继续服务控制 socket,arm 时会明确报错,
		// 与旧版 arm 失败的 fail-open 语义一致。
		log.Printf("ccfly-sni-helper: DNS policy service start failed (arm will fail until :53 free): %v", err)
	} else {
		h.dnsSvc = svc
		defer svc.Stop()
	}
	_ = os.Remove(sniHelperSocket) // 清残留 socket 文件,否则 bind EADDRINUSE
	ln, err := net.Listen("unix", sniHelperSocket)
	if err != nil {
		return err
	}
	defer ln.Close()
	// Filesystem ACL and peer credentials independently pin the privileged
	// helper to the one agent UID selected at install time.
	if err := os.Chown(sniHelperSocket, allowedUID, -1); err != nil {
		return fmt.Errorf("chown helper socket to uid %d: %w", allowedUID, err)
	}
	if err := os.Chmod(sniHelperSocket, 0o600); err != nil {
		return fmt.Errorf("chmod helper socket: %w", err)
	}
	log.Printf("ccfly-sni-helper: listening on %s (root, allowed_uid=%d)", sniHelperSocket, allowedUID)
	return h.serve(ln)
}

func configuredSNIHelperUID() (int, error) {
	if raw := strings.TrimSpace(os.Getenv("CCFLY_SNI_HELPER_UID")); raw != "" {
		uid, err := strconv.Atoi(raw)
		if err == nil && uid > 0 {
			return uid, nil
		}
		return 0, fmt.Errorf("invalid CCFLY_SNI_HELPER_UID %q", raw)
	}
	// Backward-compatible fallback for an older installed plist: authorize the
	// currently logged-in console user.  The next `ccfly install` persists it.
	if fi, err := os.Stat("/dev/console"); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid > 0 {
			return int(st.Uid), nil
		}
	}
	return 0, errors.New("cannot determine non-root agent UID; reinstall ccfly while logged in")
}

// sniHelper 持单一 arm 租约。gen 做租约代际:EOF 撤租约时只撤自己那代,不误伤后来的新租约。
// dnsSvc 常驻(进程级):CoreDNS 与 OSS 策略归它;租约只拥有 :443 监听与 resolver 文件。
type sniHelper struct {
	mu          sync.Mutex
	allowedUID  uint32
	enforcePeer bool
	connWG      sync.WaitGroup // lets tests/shutdown wait for lease teardown to finish
	gen         uint64
	listeners   []net.Listener    // 当前租约的 :443 v4+v6
	dnsSvc      *dnsPolicyService // 常驻 DNS 策略服务(非租约所有)
	resolverOn  bool
}

// serve accept 控制连接的循环,阻塞直到 ln 出错/关闭。
func (h *sniHelper) serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		h.connWG.Add(1)
		go func() {
			defer h.connWG.Done()
			h.serveConn(c)
		}()
	}
}

func (h *sniHelper) serveConn(c net.Conn) {
	defer c.Close()
	if h.enforcePeer {
		uid, err := unixPeerUID(c)
		if err != nil || uid != h.allowedUID {
			_ = writeJSONLine(c, sniArmResp{OK: false, Error: "unauthorized local uid"})
			return
		}
	}
	r := bufio.NewReader(c)
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req sniArmReq
	if e := json.Unmarshal(trimJSONLine(line), &req); e != nil || !validSNIArmRequest(req) {
		_ = writeJSONLine(c, sniArmResp{OK: false, Error: "bad arm request"})
		return
	}
	gen, err := h.arm(req)
	if err != nil {
		_ = writeJSONLine(c, sniArmResp{OK: false, Error: err.Error()})
		return
	}
	version := ""
	if h.dnsSvc != nil {
		version = h.dnsSvc.Version()
	}
	_ = writeJSONLine(c, sniArmResp{OK: true, Version: version})
	_, _ = io.Copy(io.Discard, r) // 阻塞到 EOF(agent 卸载/退出)
	h.disarm(gen)
}

func unixPeerUID(c net.Conn) (uint32, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("helper peer is not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Xucred
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		cred, sockErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if sockErr != nil {
		return 0, sockErr
	}
	return cred.Uid, nil
}

func validSNIArmRequest(req sniArmReq) bool {
	// 域名策略由 helper 自持(信任点=OSS),请求只携带 relay 端口;不做厂商判断。
	return req.Cmd == "arm" && req.RelayPort >= 1024 && req.RelayPort <= 65535
}

// arm 落地一次租约。resolver 最后安装，确保系统开始把查询导来时 :443 和 CoreDNS 都已就绪。
// CoreDNS 与域名策略由常驻 dnsPolicyService 自持,租约只管 :443 splice 与 /etc/resolver。
func (h *sniHelper) arm(req sniArmReq) (uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.teardownLocked() // 顶掉旧租约
	if h.dnsSvc == nil || !h.dnsSvc.Running() {
		return 0, errors.New("DNS policy service not running (bind :53 failed?)")
	}
	for _, addr := range sniHelperFront {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			h.teardownLocked()
			return 0, err
		}
		h.listeners = append(h.listeners, ln)
		go spliceAccept(ln, req.RelayPort)
	}
	domains := h.dnsSvc.Domains()
	h.resolverOn = true // pointResolver 写到一半失败时也必须清理已写标记文件
	if err := pointResolver(domains, "", nil); err != nil {
		h.teardownLocked()
		return 0, err
	}
	h.gen++
	return h.gen, nil
}

// rewriteResolver 在 DNS 策略热更新后重写 /etc/resolver(仅 armed;先清后写,删掉被移除的域)。
// 由 dnsPolicyService.onChange 触发(其调用时不持锁,此处取 h.mu 安全)。
func (h *sniHelper) rewriteResolver(domains []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.resolverOn {
		return
	}
	if err := restoreResolver(); err != nil {
		log.Printf("ccfly-sni-helper: restore resolver before rewrite: %v", err)
	}
	if err := pointResolver(domains, "", nil); err != nil {
		log.Printf("ccfly-sni-helper: rewrite resolver on policy change: %v", err)
		return
	}
	log.Printf("ccfly-sni-helper: resolver rewritten on policy change (%d domains)", len(domains))
}

// disarm 撤租约(仅当 gen 仍是当前代际;已被新 arm 顶掉则不动新的)。
func (h *sniHelper) disarm(gen uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if gen != h.gen {
		return
	}
	h.teardownLocked()
}

func (h *sniHelper) teardownLocked() {
	// 先撤系统路由，避免 :443 关闭后还有新查询被导入。
	// CoreDNS 属常驻 dnsPolicyService,不随租约停。
	if h.resolverOn {
		if err := restoreResolver(); err != nil {
			log.Printf("ccfly-sni-helper: restore resolver: %v", err)
		}
		h.resolverOn = false
	}
	for _, ln := range h.listeners {
		_ = ln.Close()
	}
	h.listeners = nil
}

// spliceAccept accept 本地 :443 连接,每条 splice 到 agent 的 relayPort。
func spliceAccept(ln net.Listener, relayPort int) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return // listener 关闭
		}
		go spliceToRelay(c, relayPort)
	}
}

// spliceToRelay 把一条本地 :443 连接双向 splice 到 agent 的非特权 relay 端口(agent 那侧再经 overlay
// 拨真出口)。relay 端口不在(agent 卸载窗口)→ 丢弃(fail-open,claude 重试)。
func spliceToRelay(client net.Conn, relayPort int) {
	defer client.Close()
	up, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(relayPort), 5*time.Second)
	if err != nil {
		return
	}
	defer up.Close()
	relay(client, up) // 复用 forward.go 双向拷贝 + 5min 看门狗
}

// ── /etc/hosts 管理(root;复用 sni_hosts.go 的块逻辑,LF 行尾)──

// restoreUnixHosts 删除 hosts 文件里的 ccfly 托管块,恢复用户原文。幂等(无块=no-op)。
// 仅作旧版(hosts 方案时代)残留清理;现行 DNS 方案不再写 hosts。
func restoreUnixHosts(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil // 文件不存在=没写过
	}
	cleaned := strings.TrimRight(stripCcflyHostsBlock(string(b)), "\r\n")
	if cleaned != "" {
		cleaned += "\n"
	}
	if cleaned == string(b) {
		return nil // 无块
	}
	if err := os.WriteFile(path, []byte(cleaned), 0o644); err != nil {
		return err
	}
	flushUnixDNS()
	return nil
}

// ── 小工具 ──

func writeJSONLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func trimJSONLine(b []byte) []byte { return []byte(strings.TrimSpace(string(b))) }
