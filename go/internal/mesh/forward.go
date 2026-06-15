// Package mesh — overlay TCP port bridges.
//
// The overlay IP lives only inside the userspace netstack, so ordinary
// processes (byway, sing-box) can neither listen on it nor dial it. Two bridges
// connect them to the overlay:
//
//   - EXPOSE (exit side): listen on <overlayIP>:<port> and proxy to a local
//     127.0.0.1 service (e.g. byway). An allowlist of source overlay prefixes
//     gates who may reach it — byway is unauthenticated, so the exit only opens
//     its door to the center node.
//
//   - FORWARD (center side): listen on 127.0.0.1:<port> and dial out to another
//     node's overlay service (<overlayIP>:<port>) through the netstack. sing-box
//     points an outbound at the loopback port; bytes ride the overlay to the
//     chosen exit's byway.
//
// Both are declared via `ccfly connect --overlay-expose/--overlay-forward`
// (env CCFLY_OVERLAY_EXPOSE / CCFLY_OVERLAY_FORWARD) and brought up/torn down
// with the WireGuard session.
package mesh

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/tun/netstack"
)

// exposeSpec exposes 127.0.0.1:localPort on the overlay at overlayPort, limited
// to source overlay addresses in allow (empty = any, which is logged loudly).
type exposeSpec struct {
	overlayPort int
	localPort   int
	allow       []netip.Prefix
}

// forwardSpec exposes overlay dst:dstPort on the local loopback at localPort.
type forwardSpec struct {
	localPort int
	dst       netip.Addr
	dstPort   int
}

var (
	exposeSpecs  []exposeSpec
	forwardSpecs []forwardSpec
)

// addAutoForward 追加一条「云端下发的代理策略」自动转发(localPort → dstIP:dstPort),供
// applyMeshProxy 用。幂等:同 localPort 已存在(用户用 --overlay-forward 配了 / 已加过)则跳过,
// 不覆盖用户配置、也不重复。dstIP 解析失败 → 跳过(失败安全,绝不让入网因此失败)。
func addAutoForward(localPort int, dstIP string, dstPort int) {
	for _, f := range forwardSpecs {
		if f.localPort == localPort {
			return // 该本地端口已有转发(用户配的或已加过)→ 不动
		}
	}
	dst, err := netip.ParseAddr(strings.TrimSpace(dstIP))
	if err != nil {
		return
	}
	forwardSpecs = append(forwardSpecs, forwardSpec{localPort: localPort, dst: dst, dstPort: dstPort})
}

// SetOverlayExpose parses a comma-separated expose list, replacing any prior
// config. Each item is "overlayPort:localPort[@allowCIDR|allowCIDR|...]"; a bare
// allow IP means a host (/32) rule. Empty input clears the config.
func SetOverlayExpose(s string) error {
	var out []exposeSpec
	for _, item := range splitList(s) {
		portPart, allowPart, _ := strings.Cut(item, "@")
		op, lp, err := parsePortPair(portPart)
		if err != nil {
			return fmt.Errorf("expose %q: %w", item, err)
		}
		spec := exposeSpec{overlayPort: op, localPort: lp}
		for _, c := range strings.Split(allowPart, "|") {
			if c = strings.TrimSpace(c); c == "" {
				continue
			}
			p, err := parseAllowPrefix(c)
			if err != nil {
				return fmt.Errorf("expose %q: bad allow %q: %w", item, c, err)
			}
			spec.allow = append(spec.allow, p)
		}
		out = append(out, spec)
	}
	exposeSpecs = out
	return nil
}

// SetOverlayForward parses a comma-separated forward list, replacing any prior
// config. Each item is "localPort:overlayIP:overlayPort". Empty input clears it.
func SetOverlayForward(s string) error {
	var out []forwardSpec
	for _, item := range splitList(s) {
		parts := strings.Split(item, ":")
		if len(parts) != 3 {
			return fmt.Errorf("forward %q: want localPort:overlayIP:overlayPort", item)
		}
		lp, err := parsePort(parts[0])
		if err != nil {
			return fmt.Errorf("forward %q: %w", item, err)
		}
		dst, err := netip.ParseAddr(strings.TrimSpace(parts[1]))
		if err != nil {
			return fmt.Errorf("forward %q: bad overlay IP: %w", item, err)
		}
		dp, err := parsePort(parts[2])
		if err != nil {
			return fmt.Errorf("forward %q: %w", item, err)
		}
		out = append(out, forwardSpec{localPort: lp, dst: dst, dstPort: dp})
	}
	forwardSpecs = out
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, it := range strings.Split(s, ",") {
		if it = strings.TrimSpace(it); it != "" {
			out = append(out, it)
		}
	}
	return out
}

func parsePortPair(s string) (a, b int, err error) {
	l, r, ok := strings.Cut(s, ":")
	if !ok {
		return 0, 0, fmt.Errorf("want overlayPort:localPort, got %q", s)
	}
	if a, err = parsePort(l); err != nil {
		return 0, 0, err
	}
	if b, err = parsePort(r); err != nil {
		return 0, 0, err
	}
	return a, b, nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("bad port %q", s)
	}
	return n, nil
}

func parseAllowPrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		return netip.ParsePrefix(s)
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func (s exposeSpec) permits(remote net.Addr) bool {
	if len(s.allow) == 0 {
		return true
	}
	ap, err := netip.ParseAddrPort(remote.String())
	if err != nil {
		return false
	}
	for _, p := range s.allow {
		if p.Contains(ap.Addr()) {
			return true
		}
	}
	return false
}

func (s exposeSpec) allowDesc() string {
	if len(s.allow) == 0 {
		return "ANY"
	}
	parts := make([]string, len(s.allow))
	for i, p := range s.allow {
		parts[i] = p.String()
	}
	return strings.Join(parts, "|")
}

// startOverlayExpose listens on <overlay>:<overlayPort> inside the netstack and
// proxies allowed connections to 127.0.0.1:<localPort>.
func startOverlayExpose(ctx context.Context, tnet *netstack.Net, overlay netip.Addr, spec exposeSpec) (io.Closer, error) {
	ln, err := tnet.ListenTCP(&net.TCPAddr{IP: overlay.AsSlice(), Port: spec.overlayPort})
	if err != nil {
		return nil, fmt.Errorf("mesh: overlay expose listen %s:%d: %w", overlay, spec.overlayPort, err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if !spec.permits(c.RemoteAddr()) {
				log.Printf("ccfly: overlay expose :%d denied %s (allow %s)", spec.overlayPort, c.RemoteAddr(), spec.allowDesc())
				_ = c.Close()
				continue
			}
			go proxyConn(ctx, c, spec.localPort)
		}
	}()
	return ln, nil
}

// startLocalForward listens on 127.0.0.1:<localPort> and dials each connection
// out to the overlay dst:dstPort through the netstack.
func startLocalForward(ctx context.Context, tnet *netstack.Net, spec forwardSpec) (io.Closer, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", spec.localPort))
	if err != nil {
		return nil, fmt.Errorf("mesh: local forward listen 127.0.0.1:%d: %w", spec.localPort, err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go forwardToOverlay(ctx, tnet, c, spec)
		}
	}()
	return ln, nil
}

func forwardToOverlay(ctx context.Context, tnet *netstack.Net, local net.Conn, spec forwardSpec) {
	defer local.Close()
	oc, err := tnet.DialContextTCP(ctx, &net.TCPAddr{IP: spec.dst.AsSlice(), Port: spec.dstPort})
	if err != nil {
		log.Printf("ccfly: overlay forward dial %s:%d: %v", spec.dst, spec.dstPort, err)
		return
	}
	defer oc.Close()
	relay(local, oc)
}

// relayIdleTimeout reaps bridged connections whose peer vanished without a FIN
// (e.g. the cloud rebuilt its WS tunnel, or a gateway client was abandoned):
// the netstack TCP side then never errors, so without a watchdog the pair —
// plus its loopback dial — stays ESTABLISHED forever and the host's connection
// table fills up. Live SSE streams ping every ~15s, so 5m never trips for a
// healthy conn.
const relayIdleTimeout = 5 * time.Minute

// relay copies bytes both ways and half-closes each write side on EOF so the
// peer sees a clean shutdown. Returns when the first direction finishes; the
// deferred closes in callers unblock the other. A watchdog force-closes both
// conns when no bytes move in either direction for relayIdleTimeout.
func relay(a, b net.Conn) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(relayIdleTimeout / 4)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if time.Since(time.Unix(0, last.Load())) > relayIdleTimeout {
					_ = a.Close()
					_ = b.Close()
					return
				}
			}
		}
	}()
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := src.Read(buf)
			if n > 0 {
				last.Store(time.Now().UnixNano())
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if rerr != nil {
				break
			}
		}
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
}
