//go:build darwin

package mesh

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// 这些集成测试在真 macOS 上执行真实 helper 代码,但把生产的 socket/resolver/:53/:443 换成临时资源
// 和非特权端口，于是非 root 也能端到端跑通协议、CoreDNS、租约与 splice。

// withHelperTestEnv 把 helper 的可注入点指向临时资源,返回临时 resolver 目录。
// socket 必须放 /tmp 短路径:macOS unix socket 路径有 ~104 字符上限,t.TempDir() 太长会 bind 失败。
func withHelperTestEnv(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ccfly-snit")
	if err != nil {
		t.Fatal(err)
	}
	hostsPath := filepath.Join(dir, "hosts")
	// 种一个真实感的 /etc/hosts(LF)。
	seed := "##\n# Host Database\n##\n127.0.0.1\tlocalhost\n::1             localhost\n"
	if err := os.WriteFile(hostsPath, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	oldSock, oldHosts, oldResolver, oldFront, oldFlush := sniHelperSocket, unixHostsPath, resolverDir, sniHelperFront, flushUnixDNS
	oldDNSIP, oldDNSPort := sniCoreDNSListenIP, sniCoreDNSPort
	sniHelperSocket = filepath.Join(dir, "h.sock")
	unixHostsPath = hostsPath
	resolverDir = filepath.Join(dir, "resolver")
	sniHelperFront = []string{"127.0.0.1:0"} // 非特权临时端口顶替真 :443
	sniCoreDNSListenIP = "127.0.0.1"
	sniCoreDNSPort = freeDNSPort(t)
	flushUnixDNS = func() {} // 测试里不真刷系统 DNS
	t.Cleanup(func() {
		sniHelperSocket, unixHostsPath, resolverDir, sniHelperFront, flushUnixDNS = oldSock, oldHosts, oldResolver, oldFront, oldFlush
		sniCoreDNSListenIP, sniCoreDNSPort = oldDNSIP, oldDNSPort
		_ = os.RemoveAll(dir)
	})
	return resolverDir
}

func freeDNSPort(t *testing.T) int {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := tcp.Addr().(*net.TCPAddr).Port
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		tcp.Close()
		t.Fatal(err)
	}
	tcp.Close()
	udp.Close()
	return port
}

// startTestHelper 起真实 helper 服务端(含常驻 DNS 策略服务,fetchURL 指向不可达地址→兜底清单),
// 但测试自持 listener → cleanup 关它即停 serve 循环,不像 RunSNIHelper 那样永久 Accept 泄漏 goroutine
// (泄漏的 disarm 会踩后续测试的 hosts 文件)。返回 helper 句柄(测试可经 h.dnsSvc 注入新策略)。
func startTestHelper(t *testing.T) *sniHelper {
	t.Helper()
	_ = restoreResolver()
	_ = os.Remove(sniHelperSocket)
	ln, err := net.Listen("unix", sniHelperSocket)
	if err != nil {
		t.Fatalf("listen helper socket: %v", err)
	}
	_ = os.Chmod(sniHelperSocket, 0o666)
	h := &sniHelper{}
	svc := newDNSPolicyService(sniCoreDNSListenIP, sniCoreDNSPort)
	svc.fetchURL = "http://127.0.0.1:1/unreachable" // 测试不碰真 OSS;兜底清单(含 anthropic.com/claude.ai)
	svc.onChange = func(domains []string) { h.rewriteResolver(domains) }
	if err := svc.start(); err != nil {
		t.Fatalf("start dns policy service: %v", err)
	}
	h.dnsSvc = svc
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = h.serve(ln)
	}()
	t.Cleanup(func() {
		ln.Close()
		<-serveDone
		h.connWG.Wait()
		svc.Stop()
	})
	return h
}

// armClient 模拟 agent 侧线格:连 socket、发 arm(只带 relay_port)、读应答。返回保持打开的控制连接(关它=撤租约)。
func armClient(t *testing.T, relayPort int) (net.Conn, sniArmResp) {
	t.Helper()
	c, err := net.Dial("unix", sniHelperSocket)
	if err != nil {
		t.Fatalf("dial helper: %v", err)
	}
	if err := writeJSONLine(c, sniArmReq{Cmd: "arm", RelayPort: relayPort}); err != nil {
		t.Fatalf("write arm: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read resp: %v", err)
	}
	_ = c.SetReadDeadline(time.Time{})
	var resp sniArmResp
	if err := json.Unmarshal(trimJSONLine(line), &resp); err != nil {
		t.Fatalf("bad resp json: %v", err)
	}
	return c, resp
}

// startEchoServer 起一个把收到字节原样回写的 TCP 服务,冒充 agent 的 relay 端(现实里那侧经 overlay 拨出口)。
func startEchoServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); _, _ = io.Copy(c, c) }(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func fileHas(t *testing.T, path, want string) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) != "" && bytesContains(b, want)
}

func bytesContains(b []byte, s string) bool { return len(s) == 0 || indexOf(string(b), s) >= 0 }

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// 端到端:arm → resolver 按策略服务清单写入;关控制连接 → resolver 删除(CoreDNS 属常驻服务,不随租约停)。
func TestHelperArmStartsCoreDNSAndDisarmRestores(t *testing.T) {
	resolverPath := withHelperTestEnv(t)
	startTestHelper(t)
	echoPort := startEchoServer(t)

	ctrl, resp := armClient(t, echoPort)
	defer ctrl.Close()
	if !resp.OK {
		t.Fatalf("arm not ok: %+v", resp)
	}
	resolverFile := filepath.Join(resolverPath, "anthropic.com")
	if !fileHas(t, resolverFile, resolverMarker) || !fileHas(t, resolverFile, "nameserver 127.0.0.1") {
		b, _ := os.ReadFile(resolverFile)
		t.Fatalf("scoped resolver missing after arm:\n%s", b)
	}
	got, err := lookupViaCoreDNS(helperDNSProbeName)
	if err != nil {
		t.Fatalf("lookup through CoreDNS: %v", err)
	}
	if !containsString(got, "127.0.0.1") || !containsString(got, "::1") {
		t.Fatalf("CoreDNS synthesized %v, want both loopback families", got)
	}

	// 关控制连接 = 撤租约:resolver 删除;CoreDNS 属常驻 dnsPolicyService,撤租约后仍在(与生产一致)。
	ctrl.Close()
	if !waitFor(2*time.Second, func() bool {
		_, err := os.Stat(resolverFile)
		return os.IsNotExist(err)
	}) {
		t.Fatal("resolver file remained after disarm")
	}
}

// 探测名:在 intercept apex 之下(会被合成 loopback),但绝不在真实 /etc/hosts 里。
// Go 纯 resolver 查 hosts 优先于发 DNS —— 用 api.anthropic.com 会被本机残留/现役
// ccfly hosts 块短路,根本不发 DNS 请求,租约撤销断言会被骗。
const helperDNSProbeName = "dns-probe.anthropic.com"

func lookupViaCoreDNS(host string) ([]string, error) {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(sniCoreDNSListenIP, strconv.Itoa(sniCoreDNSPort)))
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return r.LookupHost(ctx, host)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// 数据面:经 helper 承接的前端端口发字节,应被 splice 到 relay(echo)再原样回来。
func TestHelperSplicesFrontToRelay(t *testing.T) {
	withHelperTestEnv(t)
	startTestHelper(t)
	echoPort := startEchoServer(t)

	// 前端监听是 helper 在 arm 时按 sniHelperFront 绑的(测试里=127.0.0.1:0 临时端口)。arm 后需要
	// 拿到它的实际端口 → 用一个可发现端口:把 sniHelperFront 换成我们预先占好的监听地址。
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	frontAddr := front.Addr().String()
	front.Close() // 立刻释放,让 helper arm 时重新绑同一端口
	sniHelperFront = []string{frontAddr}

	ctrl, resp := armClient(t, echoPort)
	if !resp.OK {
		t.Fatalf("arm not ok: %+v", resp)
	}
	defer ctrl.Close()

	// 连前端(=claude 会连到的 127.0.0.1:443),发字节,期望经 splice→echo→splice 原样回来。
	var conn net.Conn
	if !waitFor(2*time.Second, func() bool {
		c, e := net.Dial("tcp", frontAddr)
		if e != nil {
			return false
		}
		conn = c
		return true
	}) {
		t.Fatal("front listener never accepted")
	}
	defer conn.Close()

	msg := []byte("CLIENT-HELLO-" + strconv.Itoa(echoPort))
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echoed: %v", err)
	}
	if string(got) != string(msg) {
		t.Fatalf("splice corrupted bytes: sent %q got %q", msg, got)
	}
}

// 租约代际:新 arm 顶掉旧 arm 后,旧控制连接 EOF 不应误撤新租约(gen 守卫)。
func TestHelperLeaseSupersedeKeepsNewResolver(t *testing.T) {
	resolverPath := withHelperTestEnv(t)
	startTestHelper(t)
	echoPort := startEchoServer(t)

	old, r1 := armClient(t, echoPort)
	if !r1.OK {
		t.Fatalf("first arm not ok: %+v", r1)
	}
	// 第二次合法 arm 顶掉第一次(清单同源,断言 claude.ai 在兜底清单内)。
	newer, r2 := armClient(t, echoPort)
	if !r2.OK {
		t.Fatalf("second arm not ok: %+v", r2)
	}
	defer newer.Close()
	resolverFile := filepath.Join(resolverPath, "claude.ai")
	if !waitFor(2*time.Second, func() bool { _, err := os.Stat(resolverFile); return err == nil }) {
		t.Fatal("new lease resolver not applied")
	}

	// 关掉旧控制连接:它的 EOF 撤租约不能误伤当前(新)租约 → claude.ai 块必须仍在。
	old.Close()
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(resolverFile); err != nil {
		t.Fatalf("stale-lease EOF wrongly tore down current lease: %v", err)
	}
}

func TestHelperRejectsPrivilegedRelay(t *testing.T) {
	withHelperTestEnv(t)
	startTestHelper(t)

	// 域名/上游校验已随「策略由 helper 自持」移除;arm 请求只校验 relay 端口范围。
	c, resp := armClient(t, 443)
	c.Close()
	if resp.OK {
		t.Fatal("root helper accepted a privileged relay port")
	}
}

// 策略热更新:dnsPolicyService 发布新清单并 reload → OnChange 重写 resolver(新增域补齐、移除域删文件),
// CoreDNS 应答随之变化;arm 应答携带当前清单版本。
func TestHelperPolicyHotReloadRewritesResolver(t *testing.T) {
	resolverPath := withHelperTestEnv(t)
	h := startTestHelper(t)
	echoPort := startEchoServer(t)

	ctrl, resp := armClient(t, echoPort)
	defer ctrl.Close()
	if !resp.OK {
		t.Fatalf("arm not ok: %+v", resp)
	}
	// 兜底清单从未拉过 OSS → arm 时无版本。
	if resp.Version != "" {
		t.Fatalf("fallback policy should carry no version, got %q", resp.Version)
	}
	if _, err := os.Stat(filepath.Join(resolverPath, "example.test")); !os.IsNotExist(err) {
		t.Fatal("example.test resolver should not exist before policy change")
	}

	// 模拟 OSS 新清单(加 example.test、去掉 statsig.com)并热重载。
	if !h.dnsSvc.publish([]byte(`{"intercept":["anthropic.com","claude.ai","claude.com","example.test"],"upstream":["223.5.5.5"]}`), "etag-hot1") {
		t.Fatal("policy publish should report change")
	}
	h.dnsSvc.reload()

	// resolver 被重写:新域补齐,被移除的域文件删除,版本更新。
	if !fileHas(t, filepath.Join(resolverPath, "example.test"), resolverMarker) {
		t.Fatal("example.test resolver missing after hot reload")
	}
	if _, err := os.Stat(filepath.Join(resolverPath, "statsig.com")); !os.IsNotExist(err) {
		t.Fatal("statsig.com resolver should be removed after policy shrink")
	}
	if got := h.dnsSvc.Version(); got != "etag-hot1" {
		t.Fatalf("version=%q", got)
	}
	// CoreDNS 已重载:新域被拦截。
	got, err := lookupViaCoreDNS("a.example.test")
	if err != nil || !containsString(got, "127.0.0.1") {
		t.Fatalf("example.test not intercepted after reload: %v %v", got, err)
	}
	// 新 arm 的应答应带当前版本。
	c2, r2 := armClient(t, echoPort)
	defer c2.Close()
	if !r2.OK || r2.Version != "etag-hot1" {
		t.Fatalf("second arm resp=%+v", r2)
	}
}

func TestHelperResolverConflictRollsBack(t *testing.T) {
	resolverPath := withHelperTestEnv(t)
	startTestHelper(t)
	echoPort := startEchoServer(t)
	if err := os.MkdirAll(resolverPath, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(resolverPath, "anthropic.com")
	original := "nameserver 10.0.0.53\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	c, resp := armClient(t, echoPort)
	c.Close()
	if resp.OK {
		t.Fatal("helper overwrote a user-owned scoped resolver")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != original {
		t.Fatalf("user resolver changed during rollback: %q, %v", b, err)
	}
}

func TestUnixPeerUID(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ccfly-peeruid")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "peer.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan uint32, 1)
	errs := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			errs <- err
			return
		}
		defer c.Close()
		uid, err := unixPeerUID(c)
		if err != nil {
			errs <- err
			return
		}
		got <- uid
	}()
	c, err := net.Dial("unix", filepath.Join(dir, "peer.sock"))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	select {
	case err := <-errs:
		t.Fatal(err)
	case uid := <-got:
		if uid != uint32(os.Getuid()) {
			t.Fatalf("peer uid=%d want=%d", uid, os.Getuid())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("peer credential lookup timed out")
	}
}

func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(15 * time.Millisecond)
	}
	return cond()
}
