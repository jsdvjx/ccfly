// Command ccfly is the local control service for Claude Code sessions.
//
// It reads the jsonl transcripts Claude writes under ~/.claude and drives the
// session's tmux pane, exposing a local HTTP/WS API that @ccfly/react renders.
//
// Security model: this service performs NO authentication of its own. By default
// it binds the loopback interface (127.0.0.1) only. Exposing it to other hosts
// is the consumer's responsibility — front it with a reverse proxy / hub that
// authenticates before forwarding (mirroring ttyd's "bind loopback + reverse
// proxy auth" posture). No mesh/wireguard binding is included.
//
// Usage:
//
//	ccfly serve [--port 7699] [--bind 127.0.0.1] [--claude-dir <dir>]
//
// Env fallbacks (flags win): CCFLY_PORT, CCFLY_BIND, CCFLY_CLAUDE_DIR.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/ccfly/ccfly/go/internal/control"
	"github.com/ccfly/ccfly/go/internal/mesh"
	"github.com/ccfly/ccfly/go/internal/svc"
)

// version is overridden at build time via -ldflags.
var version = "0.0.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "serve":
		if err := runServe(ctx, os.Args[2:]); err != nil {
			log.Fatalf("serve: %v", err)
		}
	case "connect":
		if err := runConnect(ctx, os.Args[2:]); err != nil {
			log.Fatalf("connect: %v", err)
		}
	case "install":
		if err := runInstall(os.Args[2:]); err != nil {
			log.Fatalf("install: %v", err)
		}
	case "uninstall":
		if err := runUninstall(os.Args[2:]); err != nil {
			log.Fatalf("uninstall: %v", err)
		}
	case "version", "-v", "--version":
		fmt.Println("ccfly", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

// runServe parses serve flags (env-backed) and starts the control service.
func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.String("port", envOr("CCFLY_PORT", "7699"), "TCP port to listen on (env CCFLY_PORT)")
	bind := fs.String("bind", envOr("CCFLY_BIND", "127.0.0.1"), "interface/host to bind (env CCFLY_BIND); default loopback only")
	claudeDir := fs.String("claude-dir", os.Getenv("CCFLY_CLAUDE_DIR"), "Claude projects dir, e.g. ~/.claude/projects (env CCFLY_CLAUDE_DIR; default ~/.claude/projects)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ccfly serve [--port 7699] [--bind 127.0.0.1] [--claude-dir <dir>]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *claudeDir != "" {
		control.SetClaudeDir(*claudeDir)
	}
	addr := net.JoinHostPort(*bind, *port)
	return control.Serve(ctx, addr)
}

// runConnect enrolls this device with a ccfly-cloud and holds the mesh tunnel
// open. By default it ALSO runs the control service in-process on an ephemeral
// loopback port (the overlay listener proxies to it) — one command serves and
// joins. --no-serve instead proxies the overlay to a separately-run `ccfly
// serve` (CCFLY_LOCAL_PORT, default 7699).
func runConnect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	noServe := fs.Bool("no-serve", false, "don't run the control service in-process; proxy the overlay to a separate `ccfly serve` (CCFLY_LOCAL_PORT, default 7699)")
	claudeDir := fs.String("claude-dir", os.Getenv("CCFLY_CLAUDE_DIR"), "Claude projects dir for the in-process control service (default ~/.claude/projects)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ccfly connect <host>/<code> [--no-serve] [--claude-dir <dir>]")
		fs.PrintDefaults()
	}
	if len(args) < 1 || args[0] == "" {
		fs.Usage()
		return errors.New("missing <host>/<code>")
	}
	target := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	if *noServe {
		// Point the overlay at a separately-run control service.
		if p := os.Getenv("CCFLY_LOCAL_PORT"); p != "" {
			if n, perr := strconv.Atoi(p); perr == nil {
				mesh.SetLocalControlPort(n)
			}
		}
	} else {
		// Run the control service in-process on an ephemeral loopback port; the
		// overlay listener proxies to it. One command = serve + join overlay.
		if *claudeDir != "" {
			control.SetClaudeDir(*claudeDir)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("start control service: %w", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		srv := &http.Server{Handler: control.Handler()}
		go func() { _ = srv.Serve(ln) }()
		go func() { <-ctx.Done(); _ = srv.Close() }()
		mesh.SetLocalControlPort(port)
		log.Printf("ccfly: control service (in-process) on 127.0.0.1:%d", port)
	}
	return mesh.Connect(ctx, target)
}

// runInstall installs `ccfly connect` as a persistent OS service (launchd/systemd).
func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	system := fs.Bool("system", false, "system-wide service (needs sudo; survives logout/reboot)")
	claudeDir := fs.String("claude-dir", "", "Claude projects dir (default ~/.claude/projects)")
	dry := fs.Bool("dry-run", false, "print what would be done; change nothing")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ccfly install <host>/<code> [--system] [--claude-dir <dir>] [--dry-run]")
		fs.PrintDefaults()
	}
	if len(args) < 1 || args[0] == "" {
		fs.Usage()
		return errors.New("missing <host>/<code>")
	}
	target := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	return svc.Install(svc.Options{Target: target, System: *system, ClaudeDir: *claudeDir, DryRun: *dry})
}

// runUninstall removes the persistent service.
func runUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	system := fs.Bool("system", false, "remove the system-wide service (needs sudo)")
	dry := fs.Bool("dry-run", false, "print what would be done; change nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return svc.Uninstall(svc.Options{System: *system, DryRun: *dry})
}

func usage() {
	fmt.Println(`ccfly — local Claude Code control service

Usage:
  ccfly serve [--port 7699] [--bind 127.0.0.1] [--claude-dir <dir>]
      Run the HTTP control service: tmux send-keys / capture, jsonl transcript
      tailing + SSE follow, subagents / workflow read views, session info.
  ccfly connect <host>/<code> [--no-serve] [--claude-dir <dir>]
      Enroll with a ccfly-cloud (using a connect code) AND run the control
      service in-process, then hold the overlay tunnel open — one command serves
      + joins. --no-serve proxies to a separate "ccfly serve" instead. Loopback
      hosts use http.
  ccfly install <host>/<code> [--system] [--claude-dir <dir>] [--dry-run]
      Install ccfly connect as a persistent service (macOS launchd / Linux
      systemd) so the device stays joined across logout / reboot / sleep.
      --system = system-wide (sudo, survives logout). Default = user-level.
  ccfly uninstall [--system]
      Remove the service installed by "ccfly install".
  ccfly version
  ccfly help

Flags (serve) — env fallbacks in parentheses, flags win:
  --port        TCP port to listen on            (CCFLY_PORT,        default 7699)
  --bind        host/interface to bind           (CCFLY_BIND,        default 127.0.0.1)
  --claude-dir  Claude projects dir to read       (CCFLY_CLAUDE_DIR,  default ~/.claude/projects)

Security: the service does NOT authenticate. It binds loopback by default;
front it with a reverse proxy / hub for any remote exposure.`)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
