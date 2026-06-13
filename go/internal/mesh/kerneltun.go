// Package mesh — kernel-interface (kernel TUN) datapath.
//
// By default the device runs WireGuard over a userspace gVisor netstack, whose
// overlay IP is invisible to the kernel — so ordinary processes need the
// expose/forward bridges to use it. Kernel mode instead brings up a REAL kernel
// TUN interface (`ccfly0`) with the overlay IP assigned, visible to `ip addr`
// and the kernel routing table. Services (byway, sing-box) then bind/dial the
// overlay IPs directly — no bridges. Linux + root (CAP_NET_ADMIN) only; the
// transport (WG-over-WSS) and the hub's forwarding are unchanged.
package mesh

import (
	"bytes"
	"fmt"
	"log"
	"net/netip"
	"os/exec"
	"runtime"
	"strings"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// kernelMode selects the kernel-TUN datapath over the userspace netstack.
var kernelMode bool

// SetKernelMode toggles kernel-interface mode. Must be set before connect.
func SetKernelMode(on bool) { kernelMode = on }

// kernelIfName is the kernel network interface name we create for the overlay.
const kernelIfName = "ccfly0"

// bringUpKernel brings WireGuard up over a real kernel TUN and assigns the
// overlay address to it. No overlay bridges are started — the interface is
// directly usable by any process.
func bringUpKernel(st *State, overlayAddr netip.Addr, uapi string, bind *clientWSBind, logger *device.Logger) (*wgSession, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("mesh: kernel mode is Linux-only (GOOS=%s) — use netstack", runtime.GOOS)
	}
	_ = ipCmd("link", "del", kernelIfName) // best-effort: clear a stale iface left by a crash

	tunDev, err := tun.CreateTUN(kernelIfName, device.DefaultMTU)
	if err != nil {
		return nil, fmt.Errorf("mesh: create kernel TUN %s (needs root/CAP_NET_ADMIN): %w", kernelIfName, err)
	}
	name, _ := tunDev.Name()

	dev := device.NewDevice(tunDev, bind, logger)
	sess := &wgSession{dev: dev, bind: bind, tun: tunDev}
	if err := dev.IpcSet(uapi); err != nil {
		sess.close()
		return nil, fmt.Errorf("mesh: wg IpcSet: %w", err)
	}
	if err := dev.Up(); err != nil {
		sess.close()
		return nil, fmt.Errorf("mesh: wg up: %w", err)
	}

	prefix := overlayPrefixLen(st.OverlayCIDR)
	if err := configureIface(name, overlayAddr, prefix); err != nil {
		sess.close()
		return nil, err
	}
	log.Printf("ccfly: wireguard up (kernel) — iface %s, overlay %s/%d (direct overlay routing, no bridges)",
		name, overlayAddr, prefix)
	return sess, nil
}

// configureIface assigns the overlay address (the /prefix adds the connected
// route for the whole overlay) and brings the interface up.
func configureIface(name string, addr netip.Addr, prefix int) error {
	if err := ipCmd("addr", "add", fmt.Sprintf("%s/%d", addr.String(), prefix), "dev", name); err != nil {
		return fmt.Errorf("mesh: assign %s/%d to %s: %w", addr, prefix, name, err)
	}
	if err := ipCmd("link", "set", name, "up"); err != nil {
		return fmt.Errorf("mesh: bring %s up: %w", name, err)
	}
	return nil
}

func ipCmd(args ...string) error {
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %v: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}

// overlayPrefixLen extracts the overlay prefix length (e.g. 16 from
// 100.64.0.0/16), defaulting to /16 if the stored CIDR is missing/unparseable.
func overlayPrefixLen(cidr string) int {
	if p, err := netip.ParsePrefix(strings.TrimSpace(cidr)); err == nil {
		return p.Bits()
	}
	return 16
}
