//go:build darwin

package mesh

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// 这些集成测试在真 macOS 上执行真实 helper 代码,但把生产的固定 socket/hosts/:443 换成临时 socket、
// 临时 hosts 文件、非特权端口 —— 于是非 root 也能端到端跑通协议+租约+splice(唯一没跑的是「绑真 :443」,
// 那步纯粹是端口号 <1024 的特权判定,与逻辑无关)。

// withHelperTestEnv 把 helper 的可注入点指向临时资源,返回临时 hosts 路径。
// socket 必须放 /tmp 短路径:macOS unix socket 路径有 ~104 字符上限,t.TempDir() 太长会 bind 失败。
func withHelperTestEnv(t *testing.T) (hostsPath string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ccfly-snit")
	if err != nil {
		t.Fatal(err)
	}
	hostsPath = filepath.Join(dir, "hosts")
	// 种一个真实感的 /etc/hosts(LF)。
	seed := "##\n# Host Database\n##\n127.0.0.1\tlocalhost\n::1             localhost\n"
	if err := os.WriteFile(hostsPath, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	oldSock, oldHosts, oldFront, oldFlush := sniHelperSocket, unixHostsPath, sniHelperFront, flushUnixDNS
	sniHelperSocket = filepath.Join(dir, "h.sock")
	unixHostsPath = hostsPath
	sniHelperFront = []string{"127.0.0.1:0"} // 非特权临时端口顶替真 :443
	flushUnixDNS = func() {}                 // 测试里不真刷系统 DNS
	t.Cleanup(func() {
		sniHelperSocket, unixHostsPath, sniHelperFront, flushUnixDNS = oldSock, oldHosts, oldFront, oldFlush
		_ = os.RemoveAll(dir)
	})
	return hostsPath
}

// startTestHelper 起真实 helper 服务端,但测试自持 listener → cleanup 关它即停 serve 循环,
// 不像 RunSNIHelper 那样永久 Accept 泄漏 goroutine(泄漏的 disarm 会踩后续测试的 hosts 文件)。
func startTestHelper(t *testing.T) {
	t.Helper()
	_ = restoreUnixHosts(unixHostsPath)
	_ = os.Remove(sniHelperSocket)
	ln, err := net.Listen("unix", sniHelperSocket)
	if err != nil {
		t.Fatalf("listen helper socket: %v", err)
	}
	_ = os.Chmod(sniHelperSocket, 0o666)
	h := &sniHelper{hostsPath: unixHostsPath}
	go func() { _ = h.serve(ln) }()
	t.Cleanup(func() { ln.Close() })
}

// armClient 模拟 agent 侧线格:连 socket、发 arm、读应答。返回保持打开的控制连接(关它=撤租约)。
func armClient(t *testing.T, relayPort int, hosts []string) (net.Conn, sniArmResp) {
	t.Helper()
	c, err := net.Dial("unix", sniHelperSocket)
	if err != nil {
		t.Fatalf("dial helper: %v", err)
	}
	if err := writeJSONLine(c, sniArmReq{Cmd: "arm", RelayPort: relayPort, Hosts: hosts}); err != nil {
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

// 端到端:arm → /etc/hosts 写入托管块 + 应答 ok;关控制连接 → hosts 恢复(租约撤销)。
func TestHelperArmWritesHostsAndDisarmRestores(t *testing.T) {
	hostsPath := withHelperTestEnv(t)
	startTestHelper(t)
	echoPort := startEchoServer(t)

	ctrl, resp := armClient(t, echoPort, []string{"api.anthropic.com", "claude.ai"})
	if !resp.OK {
		t.Fatalf("arm not ok: %+v", resp)
	}
	if !fileHas(t, hostsPath, "127.0.0.1 api.anthropic.com") || !fileHas(t, hostsPath, "::1 claude.ai") {
		b, _ := os.ReadFile(hostsPath)
		t.Fatalf("hosts block missing after arm:\n%s", b)
	}
	if !fileHas(t, hostsPath, hostsBeginPrefix) {
		t.Fatal("marker missing after arm")
	}

	// 关控制连接 = 撤租约 → helper 恢复 /etc/hosts。
	ctrl.Close()
	if !waitFor(2*time.Second, func() bool {
		b, _ := os.ReadFile(hostsPath)
		return indexOf(string(b), hostsBeginPrefix) < 0
	}) {
		b, _ := os.ReadFile(hostsPath)
		t.Fatalf("hosts block not restored after disarm:\n%s", b)
	}
	// 用户原条目仍在。
	if !fileHas(t, hostsPath, "127.0.0.1\tlocalhost") {
		t.Fatal("user hosts entry lost after restore")
	}
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

	ctrl, resp := armClient(t, echoPort, []string{"api.anthropic.com"})
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
func TestHelperLeaseSupersedeKeepsNewHosts(t *testing.T) {
	hostsPath := withHelperTestEnv(t)
	startTestHelper(t)
	echoPort := startEchoServer(t)

	old, r1 := armClient(t, echoPort, []string{"api.anthropic.com"})
	if !r1.OK {
		t.Fatalf("first arm not ok: %+v", r1)
	}
	// 第二次 arm(不同主机名)顶掉第一次。
	newer, r2 := armClient(t, echoPort, []string{"claude.ai"})
	if !r2.OK {
		t.Fatalf("second arm not ok: %+v", r2)
	}
	defer newer.Close()
	if !waitFor(2*time.Second, func() bool { return fileHas(t, hostsPath, "127.0.0.1 claude.ai") }) {
		t.Fatal("new lease hosts not applied")
	}

	// 关掉旧控制连接:它的 EOF 撤租约不能误伤当前(新)租约 → claude.ai 块必须仍在。
	old.Close()
	time.Sleep(300 * time.Millisecond)
	if !fileHas(t, hostsPath, "127.0.0.1 claude.ai") || !fileHas(t, hostsPath, hostsBeginPrefix) {
		b, _ := os.ReadFile(hostsPath)
		t.Fatalf("stale-lease EOF wrongly tore down current lease:\n%s", b)
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
