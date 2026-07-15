//go:build darwin

package mesh

// snihelper_darwin.go — macOS 双进程 SNI arm 的两侧:
//
//   ① root helper（RunSNIHelper,由独立 LaunchDaemon `ccfly sni-helper` 以 root 跑）——承接两件
//      agent 非 root 干不了的特权事:绑本地 :443、写 /etc/hosts。它不碰 overlay。
//   ② agent 客户端（setupViaHelper,在非 root 的 ccfly connect 进程里）——在非特权 loopback 端口起
//      relay 监听跑现有 serve443（overlay 拨号必须留 agent:gVisor netstack 是进程内对象,不可跨进程),
//      再连 helper 控制 socket 发一次 arm{relayPort, 精确主机名}。
//
// 为什么 macOS 要拆:<1024 特权端口 macOS 只有 root 能绑,而 ccfly agent 故意以用户身份跑(共用同一
// tmux/~/.claude 才能镜像会话),又无 CAP_NET_BIND_SERVICE 之类细粒度授权 → 单进程绑 :443 必 EACCES。
// DNS 侧改用 /etc/hosts 精确主机名钉 loopback（复用 sni_hosts.go,与 Windows 对称),免掉本地 :53。
//
// 数据面:claude → 127.0.0.1:443(helper accept) → splice 到 127.0.0.1:relayPort(agent) → overlay
//         → 账号 byway-sni 出口。ClientHello 全程裸透传,exit 端 peek SNI 按设备源 IP 路由。
//
// 生命周期 = 控制连接:arm 时 helper 写 hosts+绑 :443,保持控制连接=租约;连接 EOF(agent 卸载/退出)
// → helper 恢复 /etc/hosts + 关 :443。helper 自身重启先清残留 hosts 块(fail-open,不 brick 整机 claude)。

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
type sniArmReq struct {
	Cmd       string   `json:"cmd"`        // "arm"
	RelayPort int      `json:"relay_port"` // agent 侧非特权 relay 端口(helper 把 :443 连接 splice 到此)
	Hosts     []string `json:"hosts"`      // 要钉到 loopback 的精确主机名(sniPinnedHosts)
}

// sniArmResp 是 helper → agent 的应答。
type sniArmResp struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ── agent 侧:经 helper arm ──

// setupViaHelper 在 macOS 上把 SNI arm 拆成双进程:非特权 relay 监听 + 现有 serve443(overlay 拨号留
// agent)+ 连 helper 承接 :443 与 /etc/hosts。控制连接存进 m.helperConn,teardownLocked 关它即撤租约。
func (m *sniManager) setupViaHelper(cfg *SNIConfig) error {
	// ① 非特权 relay 监听(loopback 随机端口);helper 把每条 :443 连接 splice 到此。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	m.listeners = append(m.listeners, ln) // teardownLocked 会关它
	go m.serve443(ln, cfg.Exit)           // 复用现有 accept→relaySNIToExit(activeNet 经 overlay 拨 exit)
	relayPort := ln.Addr().(*net.TCPAddr).Port

	// ② 连 helper 控制 socket,发 arm。
	c, err := net.DialTimeout("unix", sniHelperSocket, 3*time.Second)
	if err != nil {
		return fmt.Errorf("sni-helper 未运行(macOS SNI 需 root helper 承接 :443;请以 sudo 重跑 ccfly install 安装): %w", err)
	}
	if err := writeJSONLine(c, sniArmReq{Cmd: "arm", RelayPort: relayPort, Hosts: sniPinnedHosts}); err != nil {
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
	return nil
}

// ── root helper 服务端 ──

// RunSNIHelper 跑 root 特权 helper(独立 LaunchDaemon)。监听 unix 控制 socket,为非 root 的 agent
// 承接 :443 与 /etc/hosts。每个连接=一次 arm 租约(新 arm 顶掉旧租约)。阻塞直到 socket 出错。
func RunSNIHelper() error {
	allowedUID, err := configuredSNIHelperUID()
	if err != nil {
		return err
	}
	_ = restoreUnixHosts(unixHostsPath) // 上次非正常退出可能残留 hosts 块 → 先清,保证 fail-open(不把 claude 钉死)
	_ = os.Remove(sniHelperSocket)      // 清残留 socket 文件,否则 bind EADDRINUSE
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
	return (&sniHelper{hostsPath: unixHostsPath, allowedUID: uint32(allowedUID), enforcePeer: true}).serve(ln)
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
// hostsPath 在构造时捕获(生产恒 /etc/hosts):disarm 触发时按捕获值写,不随全局漂移(测试隔离关键)。
type sniHelper struct {
	mu          sync.Mutex
	hostsPath   string
	allowedUID  uint32
	enforcePeer bool
	connWG      sync.WaitGroup // lets tests/shutdown wait for lease teardown to finish
	gen         uint64
	listeners   []net.Listener // 当前租约的 :443 v4+v6
	hostsOn     bool
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
	_ = writeJSONLine(c, sniArmResp{OK: true})
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
	if req.Cmd != "arm" || req.RelayPort < 1024 || req.RelayPort > 65535 || len(req.Hosts) != len(sniPinnedHosts) {
		return false
	}
	for i := range sniPinnedHosts {
		if req.Hosts[i] != sniPinnedHosts[i] {
			return false
		}
	}
	return true
}

// arm 落地一次租约:顶掉旧租约 → 写 /etc/hosts 钉域 → 绑 :443 双栈 splice 到 relayPort。返回代际号。
func (h *sniHelper) arm(req sniArmReq) (uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.teardownLocked() // 顶掉旧租约(关旧 :443;hosts 下面覆盖写)
	// Never trust the request as root-owned hosts content, even after peer auth.
	// The wire field remains for compatibility with older agents but must exactly
	// match the compiled allowlist validated above.
	if err := writeUnixHosts(h.hostsPath, sniPinnedHosts); err != nil {
		return 0, err
	}
	h.hostsOn = true
	for _, addr := range sniHelperFront {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			h.teardownLocked() // 回滚已绑的 + 恢复 hosts
			return 0, err
		}
		h.listeners = append(h.listeners, ln)
		go spliceAccept(ln, req.RelayPort)
	}
	h.gen++
	return h.gen, nil
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
	for _, ln := range h.listeners {
		_ = ln.Close()
	}
	h.listeners = nil
	if h.hostsOn {
		_ = restoreUnixHosts(h.hostsPath)
		h.hostsOn = false
	}
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

// writeUnixHosts 把精确主机名写进 hosts 文件的 ccfly 托管块(局部替换,保留用户条目)。需 root。
func writeUnixHosts(path string, hosts []string) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	out := applyCcflyHostsBlockEOL(string(b), hosts, "\n")
	if strings.TrimRight(out, "\r\n") == strings.TrimRight(string(b), "\r\n") {
		return nil // 等价(含块)→ 免写盘/免刷缓存
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return err
	}
	flushUnixDNS()
	return nil
}

// restoreUnixHosts 删除 hosts 文件里的 ccfly 托管块,恢复用户原文。幂等(无块=no-op)。
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
