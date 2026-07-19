// Package mesh — Increment 2: the device-side WireGuard datapath.
//
// Once the /mesh WebSocket (Increment 1, mesh.go) is up, we bring up a userspace
// WireGuard device whose transport IS that single WebSocket. Every WG UDP
// datagram travels as exactly one WS BINARY frame containing only the raw bytes
// (no header) — the cloud identifies us by which device WS delivered the frame.
//
// On top of the resulting netstack overlay we run a TCP listener on
// <OverlayIP>:7699 that proxies each accepted connection to the local
// `ccfly serve` (127.0.0.1:7699 by default), so the cloud can reach this
// device's control API over the overlay.
//
// FRAME CONTRACT: one WG packet == one WS BINARY message, raw datagram bytes.
// UAPI keys are HEX (base64-decode the stored keys to 32 raw bytes, then hex).
package mesh

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// localControlPort is the loopback port the device's `ccfly serve` listens on.
// Override via SetLocalControlPort (CCFLY_LOCAL_PORT) if `ccfly serve --port`
// differs. The overlay listener is always on overlayServicePort, so the cloud
// dials a fixed overlay port regardless of the local one.
var localControlPort = 7699

// overlayServicePort is the FIXED overlay-side TCP port the device exposes its
// control API on (overlayIP:7699) and that the cloud gateway dials. Decoupled
// from localControlPort so a non-default local serve port still works.
const overlayServicePort = 7699

// SetLocalControlPort overrides the local `ccfly serve` target port that the
// overlay TCP listener proxies to (and the overlay port it listens on).
func SetLocalControlPort(p int) {
	if p > 0 && p < 65536 {
		localControlPort = p
	}
}

// controlProxyEnabled gates the overlay control-API proxy (overlayIP:7699 →
// local ccfly serve). Mesh-only clients (ccfly-mesh on sing-box / byway hosts)
// have no control service, so they turn it off and run only the configured
// expose/forward bridges.
var controlProxyEnabled = true

// SetControlProxyEnabled toggles the overlay control-API proxy on this node.
func SetControlProxyEnabled(on bool) { controlProxyEnabled = on }

// maxWGFrameBytes caps a single inbound WS frame. WG MTU is 1420; this is
// generous headroom while bounding memory per read.
const maxWGFrameBytes = 64 * 1024

// wgWriteTimeout 单条 WG datagram 写入 WS 的上限。背景 ctx 下写会永久阻塞、串行化地堵死整条
// 发送路径(见 Send);超时即拆连触发重连。比 WG 重传节拍宽,正常发送绝不触及。
const wgWriteTimeout = 10 * time.Second

// allowedOverlayCIDR is the route the cloud peer owns on our overlay: the whole
// 100.64.0.0/16 carrier-grade-NAT space ccfly assigns out of.
const allowedOverlayCIDR = "100.64.0.0/16"

// ── conn.Endpoint: the cloud, the one and only peer ──

// wsCloudEndpoint is the device's single synthetic peer (the cloud). Its
// identity strings are fixed so WG's endpoint cache treats every packet as
// coming from / going to the same peer.
type wsCloudEndpoint struct{}

var _ conn.Endpoint = wsCloudEndpoint{}

func (wsCloudEndpoint) DstToString() string { return "ws-cloud" }
func (wsCloudEndpoint) DstToBytes() []byte  { return []byte("cloud") }
func (wsCloudEndpoint) DstIP() netip.Addr   { return netip.Addr{} }
func (wsCloudEndpoint) SrcIP() netip.Addr   { return netip.Addr{} }
func (wsCloudEndpoint) SrcToString() string { return "" }
func (wsCloudEndpoint) ClearSrc()           {}

// ── conn.Bind: single-WebSocket transport ──

// clientWSBind implements conn.Bind by carrying all WG traffic over one *coder*
// WebSocket. The connection is injected once (the /mesh conn from dialOnce) and
// torn down when that conn drops; we do not reconnect inside the bind — the
// outer runTunnel loop does, building a fresh device per dial.
type clientWSBind struct {
	mu     sync.RWMutex
	conn   *websocket.Conn
	recv   chan []byte
	closed chan struct{} // recreated each Open(); closed by Close()
	// lastAct is unix-nanos of the last WG frame sent or received. Shared with the
	// keepalive goroutine (dialOnce): real traffic proves the tunnel is alive, so
	// a starved keepalive pong must not trigger a self-teardown (the flap bug).
	lastAct *atomic.Int64
}

var _ conn.Bind = (*clientWSBind)(nil)

func newClientWSBind(c *websocket.Conn, lastAct *atomic.Int64) *clientWSBind {
	return &clientWSBind{conn: c, recv: make(chan []byte, 256), lastAct: lastAct}
}

// touch stamps last-activity (WG frame sent or received). Cheap; called on the
// hot data path.
func (b *clientWSBind) touch() {
	if b.lastAct != nil {
		b.lastAct.Store(time.Now().UnixNano())
	}
}

// Open returns a single ReceiveFunc that drains the recv channel (fed by the
// reader goroutine, see pump). actualPort is 0: there is no UDP socket.
func (b *clientWSBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	b.closed = make(chan struct{})
	closedCh := b.closed
	b.mu.Unlock()

	recv := func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		select {
		case pkt := <-b.recv:
			n := copy(packets[0], pkt)
			sizes[0] = n
			eps[0] = wsCloudEndpoint{}
			return 1, nil
		case <-closedCh:
			return 0, net.ErrClosed
		}
	}
	return []conn.ReceiveFunc{recv}, 0, nil
}

// Close signals the ReceiveFunc to return net.ErrClosed. Idempotent.
func (b *clientWSBind) Close() error {
	b.mu.Lock()
	if b.closed != nil {
		select {
		case <-b.closed:
		default:
			close(b.closed)
		}
	}
	b.mu.Unlock()
	return nil
}

// Send writes each WG packet as one WS BINARY frame. If the WS is gone we drop
// silently — WG retransmits handshakes / keepalives.
func (b *clientWSBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	b.mu.RLock()
	c := b.conn
	b.mu.RUnlock()
	if c == nil {
		return nil
	}
	// 有界写超时(M3):背景 ctx 下 c.Write 在 TCP 发送缓冲写满 + 云端卡住时会**永远阻塞**;
	// coder/websocket 写串行化,一个卡死的 Send 把后续所有 WG 握手/keepalive/数据全堵在后面
	// → WS 还连着、overlay 数据不流(「在线却不通」)。超时即拆连,让 pump 读循环出错触发重连。
	for _, pkt := range bufs {
		ctx, cancel := context.WithTimeout(context.Background(), wgWriteTimeout)
		err := c.Write(ctx, websocket.MessageBinary, pkt)
		cancel()
		if err != nil {
			c.CloseNow() // 写失败/超时:主动拆连,别让死连接拖住数据面
			return err
		}
	}
	b.touch() // outbound WG traffic = tunnel alive (keepalive trusts this over pong)
	return nil
}

// ParseEndpoint only ever yields the cloud peer; the device has exactly one.
func (b *clientWSBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	if !strings.HasPrefix(s, "ws-") {
		return nil, fmt.Errorf("mesh: bad endpoint %q (want ws-cloud)", s)
	}
	return wsCloudEndpoint{}, nil
}

func (b *clientWSBind) SetMark(uint32) error { return nil }
func (b *clientWSBind) BatchSize() int       { return 1 }

// pump reads WS BINARY frames off the connection and feeds them to recv until
// the connection errors or the bind is closed. Returns the read error so the
// caller (dialOnce) can treat it as the tunnel drop. Each frame is copied
// because WG owns the destination buffer only inside ReceiveFunc.
func (b *clientWSBind) pump(ctx context.Context) error {
	b.mu.RLock()
	c := b.conn
	b.mu.RUnlock()
	if c == nil {
		return errors.New("mesh: bind has no websocket")
	}
	c.SetReadLimit(maxWGFrameBytes)
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return err
		}
		b.touch() // inbound WG traffic = tunnel alive
		buf := make([]byte, len(data))
		copy(buf, data)
		// Non-blocking handoff — never park outside c.Read. Blocking on a full
		// recv channel would stall this WS read loop so we stop auto-ponging the
		// cloud's keepalive and get false-killed (the flap bug's receive-side
		// face). Drop under backpressure; the inner TCP retransmits.
		select {
		case b.recv <- buf:
		default:
			// recv full: drop and keep reading.
		}
	}
}

// detach clears the underlying connection so a racing Send becomes a no-op.
func (b *clientWSBind) detach(c *websocket.Conn) {
	b.mu.Lock()
	if b.conn == c {
		b.conn = nil
	}
	b.mu.Unlock()
}

// ── device lifecycle ──

// wgSession bundles a running WireGuard device + its overlay listener so the
// caller can tear it all down in one shot when the WS drops.
type wgSession struct {
	dev       *device.Device
	bind      *clientWSBind
	tun       io.Closer
	tnet      *netstack.Net // this session's netstack (published to activeNet for the SNI :443 relay)
	listeners []io.Closer   // overlay control proxy + any expose/forward bridges
}

// close brings the device down and releases the netstack TUN + listener.
// Safe to call once; order: stop accepting, signal recv funcs, close device.
// NOTE: s.dev.Close() calls s.tun.Close() internally (wireguard-go device
// teardown always closes the TUN); calling s.tun.Close() separately would
// double-close netTun.done → "close of closed channel" panic.
func (s *wgSession) close() {
	if s == nil {
		return
	}
	activeNet.CompareAndSwap(s.tnet, nil) // 只清自己发布的那个 netstack(避免竞掉重连后的新会话)
	kickSNIProbe()                        // overlay 下线=网络变化:SNI 立即重检(若正 armed)
	for _, l := range s.listeners {
		if l != nil {
			_ = l.Close()
		}
	}
	if s.bind != nil {
		_ = s.bind.Close()
	}
	if s.dev != nil {
		s.dev.Close() // closes the TUN (netstack) internally; do NOT call s.tun.Close() after this
		return
	}
	// dev was never created (early error path): close the TUN directly.
	if s.tun != nil {
		_ = s.tun.Close()
	}
}

// bringUpWG configures a userspace WireGuard device bound to the given WS conn.
// It parses st.OverlayIP for the device's overlay address, sets up netstack,
// applies the UAPI config (hex keys), brings the device up, and starts the
// overlay TCP proxy listener. On any error it tears down whatever was built and
// returns it so the caller need not. The returned *wgSession must be close()d.
func bringUpWG(ctx context.Context, st *State, c *websocket.Conn, lastAct *atomic.Int64) (*wgSession, error) {
	overlayAddr, err := parseOverlayAddr(st.OverlayIP)
	if err != nil {
		return nil, err
	}
	uapi, err := buildUAPIConfig(st)
	if err != nil {
		return nil, err
	}

	bind := newClientWSBind(c, lastAct)
	logger := device.NewLogger(device.LogLevelError, "ccfly-wg: ")

	// Kernel mode: real kernel TUN with the overlay IP on it, no bridges.
	if kernelMode {
		return bringUpKernel(st, overlayAddr, uapi, bind, logger)
	}

	tunDev, tnet, err := netstack.CreateNetTUN(
		[]netip.Addr{overlayAddr},
		nil, // no overlay DNS
		device.DefaultMTU,
	)
	if err != nil {
		return nil, fmt.Errorf("mesh: create netstack tun: %w", err)
	}
	dev := device.NewDevice(tunDev, bind, logger)

	sess := &wgSession{dev: dev, bind: bind, tun: tunDev, tnet: tnet}
	activeNet.Store(tnet) // 供 SNI :443 relay 经 overlay 拨到账号出口(见 sni.go)
	kickSNIProbe()        // overlay 上线=网络变化:SNI 立即重检(若正 armed)

	if err := dev.IpcSet(uapi); err != nil {
		sess.close()
		return nil, fmt.Errorf("mesh: wg IpcSet: %w", err)
	}
	if err := dev.Up(); err != nil {
		sess.close()
		return nil, fmt.Errorf("mesh: wg up: %w", err)
	}

	if controlProxyEnabled {
		ln, err := startOverlayProxy(ctx, tnet, overlayAddr, overlayServicePort, localControlPort)
		if err != nil {
			sess.close()
			return nil, err
		}
		sess.listeners = append(sess.listeners, ln)
		log.Printf("ccfly: wireguard up — overlay %s, proxy %s:%d → 127.0.0.1:%d",
			overlayAddr, overlayAddr, overlayServicePort, localControlPort)
	} else {
		log.Printf("ccfly: wireguard up — overlay %s (mesh-only: control proxy off)", overlayAddr)
	}

	// Exit side: expose local services (e.g. byway) on the overlay, gated to the
	// configured source prefixes.
	for _, sp := range exposeSpecs {
		l, err := startOverlayExpose(ctx, tnet, overlayAddr, sp)
		if err != nil {
			sess.close()
			return nil, err
		}
		sess.listeners = append(sess.listeners, l)
		log.Printf("ccfly: overlay expose %s:%d → 127.0.0.1:%d (allow %s)",
			overlayAddr, sp.overlayPort, sp.localPort, sp.allowDesc())
	}
	// Center side: forward loopback ports to other nodes' overlay services.
	for _, fp := range forwardSpecs {
		l, err := startLocalForward(ctx, tnet, fp)
		if err != nil {
			sess.close()
			return nil, err
		}
		sess.listeners = append(sess.listeners, l)
		log.Printf("ccfly: local forward 127.0.0.1:%d → overlay %s:%d",
			fp.localPort, fp.dst, fp.dstPort)
	}
	return sess, nil
}

// startOverlayProxy listens on <overlay>:<port> inside the netstack overlay and
// proxies every accepted connection to 127.0.0.1:<port> (the local control
// service). Returns the listener so the caller can close it on teardown.
func startOverlayProxy(ctx context.Context, tnet *netstack.Net, overlay netip.Addr, overlayPort, localPort int) (io.Closer, error) {
	ln, err := tnet.ListenTCP(&net.TCPAddr{IP: overlay.AsSlice(), Port: overlayPort})
	if err != nil {
		return nil, fmt.Errorf("mesh: overlay listen %s:%d: %w", overlay, overlayPort, err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed (teardown) or fatal
			}
			go proxyConn(ctx, conn, localPort)
		}
	}()
	return ln, nil
}

// proxyConn bridges one overlay connection to the loopback control service.
func proxyConn(ctx context.Context, overlayConn net.Conn, port int) {
	defer overlayConn.Close()
	target := fmt.Sprintf("127.0.0.1:%d", port)
	var d net.Dialer
	local, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		log.Printf("ccfly: overlay proxy dial %s: %v", target, err)
		return
	}
	defer local.Close()
	relay(overlayConn, local)
}

// ── UAPI config (HEX keys) ──

// buildUAPIConfig renders the device.IpcSet string: our private key, the cloud
// peer's public key + endpoint + allowed IPs + keepalive. All keys hex-encoded.
func buildUAPIConfig(st *State) (string, error) {
	privHex, err := b64KeyToHex(st.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("mesh: device private key: %w", err)
	}
	cloudPubHex, err := b64KeyToHex(st.CloudPublicKey)
	if err != nil {
		return "", fmt.Errorf("mesh: cloud public key: %w", err)
	}
	keepalive := st.KeepaliveSec
	if keepalive <= 0 {
		keepalive = 25
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "private_key=%s\n", privHex)
	fmt.Fprintf(&sb, "public_key=%s\n", cloudPubHex)
	fmt.Fprintf(&sb, "endpoint=ws-cloud\n")
	fmt.Fprintf(&sb, "allowed_ip=%s\n", allowedOverlayCIDR)
	fmt.Fprintf(&sb, "persistent_keepalive_interval=%d\n", keepalive)
	sb.WriteString("\n")
	return sb.String(), nil
}

// b64KeyToHex decodes a base64-std WireGuard key (32 bytes) and hex-encodes it
// for UAPI. Accepts both std and url-safe base64 defensively.
func b64KeyToHex(b64 string) (string, error) {
	b64 = strings.TrimSpace(b64)
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		if raw, err = base64.URLEncoding.DecodeString(b64); err != nil {
			return "", fmt.Errorf("not valid base64: %w", err)
		}
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("expected 32-byte key, got %d", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

// parseOverlayAddr extracts the device's overlay IP. It tolerates a bare IP or
// an IP/prefix form (st.OverlayIP is documented as the IP string, but be lax).
func parseOverlayAddr(s string) (netip.Addr, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, errors.New("mesh: empty overlay IP")
	}
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Addr{}, fmt.Errorf("mesh: bad overlay prefix %q: %w", s, err)
		}
		return p.Addr(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("mesh: bad overlay IP %q: %w", s, err)
	}
	return a, nil
}
