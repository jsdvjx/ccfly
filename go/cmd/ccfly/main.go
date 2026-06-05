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
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/ccfly/ccfly/go/internal/control"
	"github.com/ccfly/ccfly/go/internal/mesh"
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

// runConnect enrolls this device with a ccfly-cloud and holds the mesh tunnel open.
func runConnect(ctx context.Context, args []string) error {
	if len(args) < 1 || args[0] == "" {
		return errors.New(`usage: ccfly connect <host>/<code>`)
	}
	// CCFLY_LOCAL_PORT points the overlay proxy at a non-default `ccfly serve` port.
	if p := os.Getenv("CCFLY_LOCAL_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			mesh.SetLocalControlPort(n)
		}
	}
	return mesh.Connect(ctx, args[0])
}

func usage() {
	fmt.Println(`ccfly — local Claude Code control service

Usage:
  ccfly serve [--port 7699] [--bind 127.0.0.1] [--claude-dir <dir>]
      Run the HTTP control service: tmux send-keys / capture, jsonl transcript
      tailing + SSE follow, subagents / workflow read views, session info.
  ccfly connect <host>/<code>
      Enroll this device with a ccfly-cloud control plane (using a connect code
      generated there) and hold the overlay tunnel open. Loopback hosts use http.
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
