// Command ccfly-mesh is a minimal, mesh-only ccfly client for servers that just
// need to join the overlay (组网) — e.g. the sing-box "center" and the byway
// "exit" hosts. It enrolls with a ccfly-cloud hub (same web-pairing flow as
// `ccfly install`), holds the WireGuard mesh tunnel, and runs the overlay port
// bridges (--overlay-expose / --overlay-forward).
//
// It deliberately does NOT link the ccfly control service (Claude session
// mirroring, tmux, scanner, hooks, uploads): it imports only internal/mesh +
// internal/svc, so the binary is small and carries none of those features, and
// the installed service needs no tmux.
//
// Usage:
//
//	ccfly-mesh [flags] <host>[/<code>]                 # run + hold the tunnel
//	ccfly-mesh install [flags] <host>[/<code>] [--system]   # web-pair once, install a persistent service
//	ccfly-mesh uninstall [--system]                    # remove that service
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ccfly/ccfly/go/internal/mesh"
	"github.com/ccfly/ccfly/go/internal/svc"
)

var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "install":
			exit(runInstall(args[1:]))
		case "uninstall":
			exit(runUninstall(args[1:]))
		case "version", "-version", "--version":
			fmt.Println("ccfly-mesh", version)
			return
		case "help", "-h", "--help":
			usage()
			return
		}
	}
	exit(runConnect(args)) // default: enroll + hold the tunnel
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ccfly-mesh:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func usage() {
	fmt.Fprint(os.Stderr, `ccfly-mesh — minimal mesh-only ccfly client (组网 only; no control service)

Usage:
  ccfly-mesh [flags] <host>[/<code>]
      Enroll with a hub and hold the overlay tunnel (run by the service).
        • <host>         codeless web pairing (opens a browser to log in & approve)
        • <host>/<code>  connect code
  ccfly-mesh install [flags] <host>[/<code>] [--system]
      Web-pair once, then install a persistent service (launchd/systemd) that
      runs the line above across logout/reboot. --system = system-wide (sudo).
  ccfly-mesh uninstall [--system]
      Remove the installed service.

Flags (run + install):
  --overlay-expose  expose local TCP services on the overlay (exit side):
                    'overlayPort:localPort[@allowCIDR|...][,...]'  (env CCFLY_OVERLAY_EXPOSE)
  --overlay-forward forward loopback ports to other nodes' overlay services (center side):
                    'localPort:overlayIP:port[,...]'              (env CCFLY_OVERLAY_FORWARD)

Examples:
  ccfly-mesh install --system --overlay-expose 8080:18080@100.64.0.2/32 cc.hn
  ccfly-mesh install --system --overlay-forward 19001:100.64.0.5:8080,19002:100.64.0.6:8080 cc.hn
`)
}

// meshFlags registers the flags shared by run + install. Kernel mode (a real
// kernel interface with the overlay IP) is the default; --netstack opts into the
// userspace netstack + the expose/forward bridges (the bridges only apply then).
func meshFlags(fs *flag.FlagSet) (expose, forward *string, netstack *bool) {
	expose = fs.String("overlay-expose", os.Getenv("CCFLY_OVERLAY_EXPOSE"),
		"[--netstack only] expose local TCP services on the overlay: 'overlayPort:localPort[@allowCIDR|...][,...]'")
	forward = fs.String("overlay-forward", os.Getenv("CCFLY_OVERLAY_FORWARD"),
		"[--netstack only] forward loopback ports to overlay services: 'localPort:overlayIP:port[,...]'")
	netstack = fs.Bool("netstack", false,
		"use the userspace netstack + expose/forward bridges instead of a kernel interface (default: kernel mode)")
	return
}

// runConnect enrolls and holds the tunnel (this is what the installed service runs).
func runConnect(args []string) error {
	fs := flag.NewFlagSet("ccfly-mesh", flag.ExitOnError)
	expose, forward, netstack := meshFlags(fs)
	fs.Usage = usage
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 || fs.Arg(0) == "" {
		usage()
		os.Exit(2)
	}
	target := fs.Arg(0)

	mesh.SetKernelMode(!*netstack)
	mesh.SetControlProxyEnabled(false) // mesh-only: no local control API to proxy
	if *netstack {
		if err := mesh.SetOverlayExpose(*expose); err != nil {
			return fmt.Errorf("overlay-expose: %w", err)
		}
		if err := mesh.SetOverlayForward(*forward); err != nil {
			return fmt.Errorf("overlay-forward: %w", err)
		}
	} else if *expose != "" || *forward != "" {
		fmt.Fprintln(os.Stderr, "ccfly-mesh: note: --overlay-expose/--overlay-forward are ignored in kernel mode (overlay IPs are directly usable); pass --netstack to use bridges")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return mesh.Connect(ctx, target, version)
}

// runInstall web-pairs once (codeless targets) then installs a persistent
// mesh-only service that runs `ccfly-mesh [bridges] <host>`.
func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	system := fs.Bool("system", false, "system-wide service (needs sudo; survives logout/reboot)")
	dry := fs.Bool("dry-run", false, "print what would be done; change nothing")
	expose, forward, netstack := meshFlags(fs)
	fs.Usage = usage
	// Allow flags before and after the host positional (mirrors `ccfly install`).
	if err := fs.Parse(args); err != nil {
		return err
	}
	target := fs.Arg(0)
	if extra := fs.Args(); len(extra) > 1 {
		if err := fs.Parse(extra[1:]); err != nil {
			return err
		}
	}
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("missing <host>[/<code>]")
	}
	// In netstack mode, validate the bridge specs now so install fails fast.
	if *netstack {
		if err := mesh.SetOverlayExpose(*expose); err != nil {
			return fmt.Errorf("overlay-expose: %w", err)
		}
		if err := mesh.SetOverlayForward(*forward); err != nil {
			return fmt.Errorf("overlay-forward: %w", err)
		}
	}

	// Codeless target → interactive web pairing once, before installing the
	// service (the service then reconnects with the saved identity).
	if isNoCode(target) && !*dry {
		fmt.Println("ccfly-mesh install: 先完成一次网页配对,再安装常驻服务…")
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err := mesh.Pair(ctx, target, version)
		stop()
		if err != nil {
			return fmt.Errorf("配对失败,未安装服务: %w", err)
		}
	}

	prof := meshProfile(runArgs(*expose, *forward, *netstack, target))
	return svc.Install(svc.Options{Target: target, System: *system, DryRun: *dry, Profile: &prof})
}

func runUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	system := fs.Bool("system", false, "remove the system-wide service (needs sudo)")
	dry := fs.Bool("dry-run", false, "print what would be done; change nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prof := meshProfile(nil)
	return svc.Uninstall(svc.Options{System: *system, DryRun: *dry, Profile: &prof})
}

// runArgs builds the service argv (after the binary): flags FIRST, host LAST —
// Go's flag parser stops at the first non-flag arg, so the host must come after
// the flags. Kernel mode (the default) needs no flags; --netstack carries the
// bridge flags through to the service.
func runArgs(expose, forward string, netstack bool, target string) []string {
	var a []string
	if netstack {
		a = append(a, "--netstack")
		if strings.TrimSpace(expose) != "" {
			a = append(a, "--overlay-expose", expose)
		}
		if strings.TrimSpace(forward) != "" {
			a = append(a, "--overlay-forward", forward)
		}
	}
	return append(a, target)
}

func meshProfile(args []string) svc.Profile {
	return svc.Profile{
		DarwinLabel: "com.ccfly.mesh",
		LinuxUnit:   "ccfly-mesh",
		BinName:     "ccfly-mesh",
		Description: "ccfly mesh-only overlay agent",
		Args:        args,
		NeedsTmux:   false,
	}
}

// isNoCode reports whether target is a bare host (codeless web pairing) rather
// than "<host>/<code>". Mirrors mesh's own dispatch.
func isNoCode(t string) bool {
	if i := strings.Index(t, "://"); i >= 0 {
		t = t[i+3:]
	}
	slash := strings.Index(t, "/")
	if slash < 0 {
		return true
	}
	return strings.Trim(t[slash+1:], "/") == ""
}
