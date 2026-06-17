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
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ccfly/ccfly/go/internal/control"
	"github.com/ccfly/ccfly/go/internal/mesh"
	"github.com/ccfly/ccfly/go/internal/svc"
)

// version is overridden at build time via -ldflags.
var version = "0.0.0-dev"

func main() {
	// panemap-hook 最先短路:它作为 Claude Code 的 SessionStart hook 在**每次会话启动**时被
	// 调起,把「当前 tmux pane → 当前 session id」登记进 ~/.ccfly/panemap.json 真值表
	// (webui 控制端点据此确定性地找到会话所在 pane,杜绝消息错发)。不走 ensureToolPath
	// (登录壳探测最长 5s,会拖慢每次会话启动;tmux 路径直接继承 claude 的环境)。
	// 静默:SessionStart hook 的 stdout 会被注入 Claude 上下文,任何失败也绝不打扰会话启动。
	if len(os.Args) > 1 && os.Args[1] == "panemap-hook" {
		_ = control.RunPaneMapHook(os.Stdin)
		return
	}
	ensureToolPath()
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ensureUTF8Locale()

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
		if err := runInstall(ctx, os.Args[2:]); err != nil {
			log.Fatalf("install: %v", err)
		}
	case "uninstall":
		if err := runUninstall(os.Args[2:]); err != nil {
			log.Fatalf("uninstall: %v", err)
		}
	case "list", "ls":
		if err := runList(os.Args[2:]); err != nil {
			log.Fatalf("list: %v", err)
		}
	case "attach", "a":
		if err := runAttach(os.Args[2:]); err != nil {
			log.Fatalf("attach: %v", err)
		}
	case "new":
		if err := runNew(os.Args[2:]); err != nil {
			log.Fatalf("new: %v", err)
		}
	case "claude":
		if err := runClaude(os.Args[2:]); err != nil {
			log.Fatalf("claude: %v", err)
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

// ensureUTF8Locale 兜底设一个 UTF-8 locale。launchd 等最小环境常没有 LANG/LC_*,导致子进程 tmux
// 以「非 UTF-8 客户端」模式运行,把所有多字节字符(中文、claude 的框线/网格符号 ⛀⛁⛶ 等)向那个
// 客户端输出时统统降级成 '_' —— 浏览器侧任何字体/缓存/重映射都救不了(字符在 tmux 出口就没了)。
// 设好 locale 后,tmux/claude/capture-pane 全链路按 UTF-8 处理;/term 再叠加 `tmux -u` 双保险。
func ensureUTF8Locale() {
	utf8 := func(v string) bool {
		u := strings.ToUpper(v)
		return strings.Contains(u, "UTF-8") || strings.Contains(u, "UTF8")
	}
	if utf8(os.Getenv("LC_ALL")) || utf8(os.Getenv("LC_CTYPE")) || utf8(os.Getenv("LANG")) {
		return // 已是 UTF-8,不动
	}
	_ = os.Setenv("LANG", "en_US.UTF-8")
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
	expose := fs.String("overlay-expose", os.Getenv("CCFLY_OVERLAY_EXPOSE"), "expose local TCP services on the overlay (exit side): 'overlayPort:localPort[@allowCIDR|...][,...]' (env CCFLY_OVERLAY_EXPOSE)")
	forward := fs.String("overlay-forward", os.Getenv("CCFLY_OVERLAY_FORWARD"), "forward loopback ports to other nodes' overlay services (center side): 'localPort:overlayIP:port[,...]' (env CCFLY_OVERLAY_FORWARD)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ccfly connect <host>[/<code>] [--no-serve] [--claude-dir <dir>] [--overlay-expose ...] [--overlay-forward ...]")
		fs.PrintDefaults()
	}
	if len(args) < 1 || args[0] == "" {
		fs.Usage()
		return errors.New("missing <host>[/<code>] — 纯 host(如 cc.hn)走无码网页配对,带 /<code> 走连接码")
	}
	target := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	if err := mesh.SetOverlayExpose(*expose); err != nil {
		return err
	}
	if err := mesh.SetOverlayForward(*forward); err != nil {
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
		// 进程内路径用 Handler()(非 Serve),需在此显式起后台巡检 —— 否则生产默认部署
		// (ccfly connect,经 cc.hn 反代)永远不跑回收/暖缓存。serve/connect 互斥,每进程恰一个。
		go control.RunScanner(ctx)
		mesh.SetLocalControlPort(port)
		log.Printf("ccfly: control service (in-process) on 127.0.0.1:%d", port)
	}
	return mesh.Connect(ctx, target, version)
}

// runInstall installs `ccfly connect` as a persistent OS service (launchd/systemd).
//
// 无码配对的关键点:配对是交互式的(要在浏览器里点「批准」),只能在 install 时跑一次。
// 所以对纯 host 目标,这里先交互式配对一次把设备身份落盘,再安装服务;装好的服务跑
// `connect <host>`,凭已保存身份重连——KeepAlive 重启不会每次重新配对(同一台设备身份)。
// 带 /<code> 的目标无需交互(连接码即凭证),直接装服务即可,行为与既有完全一致。
func runInstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	system := fs.Bool("system", false, "system-wide service (needs sudo; survives logout/reboot)")
	claudeDir := fs.String("claude-dir", "", "Claude projects dir (default ~/.claude/projects)")
	dry := fs.Bool("dry-run", false, "print what would be done; change nothing")
	expose := fs.String("overlay-expose", os.Getenv("CCFLY_OVERLAY_EXPOSE"), "bake an overlay expose into the service: 'overlayPort:localPort[@allowCIDR|...][,...]'")
	forward := fs.String("overlay-forward", os.Getenv("CCFLY_OVERLAY_FORWARD"), "bake an overlay forward into the service: 'localPort:overlayIP:port[,...]'")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ccfly install [<host>[/<code>]] [--system] [--claude-dir <dir>] [--overlay-forward ...] [--overlay-expose ...] [--dry-run]")
		fs.PrintDefaults()
	}
	// flag 可放在 host 前后任意位置:先整体 parse 吃掉 host 前的 flag,取首个位置参数为 host,
	// 再把 host 之后剩余的二次 parse 吃掉 host 后的 flag —— 修复 `install --system <host>` 把
	// "--system" 误当 host(于是 POST https://--system/… 报错)的旧 bug。
	if err := fs.Parse(args); err != nil {
		return err
	}
	target := fs.Arg(0)
	if extra := fs.Args(); len(extra) > 1 {
		if err := fs.Parse(extra[1:]); err != nil {
			return err
		}
	}
	// 没给 host → 交互式问一次(回车默认 cc.hn),不再甩 usage 报错。
	if target == "" {
		fmt.Print("ccfly install: 连接到哪个 host?(回车用 cc.hn): ")
		var line string
		fmt.Scanln(&line)
		if line == "" {
			line = "cc.hn"
		}
		target = line
	}

	// 纯 host(无码)→ 安装前先交互式配对一次(--dry-run 跳过,只展示要写什么)。
	// 已配对则 Pair 幂等直接返回。带 /<code> 的 code 目标保持原样,不做交互。
	// 判定与 mesh 的运行期分发口径一致:剥掉可选的 "scheme://" 前缀后再看是否含 "/"。
	if isNoCodeTarget(target) && !*dry {
		fmt.Println("ccfly install: 先完成一次网页配对,再安装常驻服务…")
		if err := mesh.Pair(ctx, target, version); err != nil {
			return fmt.Errorf("配对失败,未安装服务: %w", err)
		}
	}
	var extra []string
	if strings.TrimSpace(*forward) != "" {
		extra = append(extra, "--overlay-forward", *forward)
	}
	if strings.TrimSpace(*expose) != "" {
		extra = append(extra, "--overlay-expose", *expose)
	}
	return svc.Install(svc.Options{Target: target, System: *system, ClaudeDir: *claudeDir, DryRun: *dry, ExtraArgs: extra})
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
  ccfly connect <host>[/<code>] [--no-serve] [--claude-dir <dir>]
      Enroll with a ccfly-cloud AND run the control service in-process, then hold
      the overlay tunnel open — one command serves + joins. Two ways to enroll:
        • <host>/<code>  连接码流程:用预先生成的连接码直接登记(老用法不变)。
        • <host>(纯 host,如 cc.hn) 无码网页配对:打印并打开授权链接,在网页里
          登录批准后自动接入;之后凭已保存身份重连,不会重复配对。
      --no-serve proxies to a separate "ccfly serve" instead. Loopback hosts use http.
  ccfly install <host>[/<code>] [--system] [--claude-dir <dir>] [--dry-run]
      Install ccfly connect as a persistent service (macOS launchd / Linux
      systemd) so the device stays joined across logout / reboot / sleep.
      纯 host 时会先交互式完成一次网页配对再装服务;装好的服务跑 connect <host>,
      凭已保存身份重连(开机不重复配对)。
      --system = system-wide (sudo, survives logout). Default = user-level.
  ccfly uninstall [--system]
      Remove the service installed by "ccfly install".
  ccfly ls [-a]
      Directory-centric session overview: grouped by cwd, newest first; each
      row ends with a copy-paste takeover command (live -> "tmux a -t <pane>",
      offline -> "ccfly attach <sid8>"). Default shows the 5 most recent per
      directory (live ones always shown); -a = all. ("ccfly list" is an alias.)
  ccfly a [sid|sid8|cc-sid8]
      Attach to a session in tmux ("ccfly attach" is an alias). With no
      argument, opens an interactive picker: choose a project directory,
      then a session (↑↓/jk move · Enter attach · ←/Esc back · r refresh ·
      q quit). If the session is live, mirrors the existing pane; otherwise
      kills any existing claude process for it (deterministic match only)
      and recreates it inside tmux via "claude --resume" — the single-entry
      path that prevents double writers.
  ccfly new [dir]
      Start a brand-new claude in a fresh tmux session (default: current dir).
  ccfly panemap-hook
      (internal) Claude Code SessionStart hook: records "tmux pane -> current
      session id" into ~/.ccfly/panemap.json so the control service resolves
      sessions to panes deterministically. Installed automatically into
      ~/.claude/settings.json on startup; set CCFLY_NO_HOOK=1 to disable.
  ccfly version
  ccfly help

Flags (serve) — env fallbacks in parentheses, flags win:
  --port        TCP port to listen on            (CCFLY_PORT,        default 7699)
  --bind        host/interface to bind           (CCFLY_BIND,        default 127.0.0.1)
  --claude-dir  Claude projects dir to read       (CCFLY_CLAUDE_DIR,  default ~/.claude/projects)

Security: the service does NOT authenticate. It binds loopback by default;
front it with a reverse proxy / hub for any remote exposure.`)
}

// isNoCodeTarget 判定 install/connect 的目标是否走【无码配对】:剥掉可选的 "scheme://"
// 前缀、去掉首尾多余 "/" 后,若 host 之后还跟着非空段(即真有 "/code")就是连接码流程,
// 否则纯 host(如 cc.hn / https://cc.hn)走无码。口径必须与 mesh 包内 hasCode 一致。
func isNoCodeTarget(target string) bool {
	t := target
	if i := strings.Index(t, "://"); i >= 0 {
		t = t[i+3:]
	}
	slash := strings.Index(t, "/")
	if slash < 0 {
		return true
	}
	return strings.Trim(t[slash+1:], "/") == ""
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ensureToolPath resolves the user's interactive login shell PATH and applies it
// as the process PATH so all child processes (tmux, claude, etc.) inherit the
// same environment a user would get in a terminal.  When ccfly runs as a
// launchd/systemd service it inherits only a bare PATH (/usr/bin:/bin/…) that
// omits ~/.local/bin (where `npm i -g` tools like claude land) and
// /opt/homebrew/bin.  The login-shell probe fixes that at startup rather than
// wrapping every individual command.
//
// Fallback: if the probe fails for any reason (non-interactive env, $SHELL
// unset, 5-second timeout) we prepend the well-known static extras so at least
// Homebrew and /usr/local tools resolve.
func ensureToolPath() {
	const fallbackExtras = "/opt/homebrew/bin:/usr/local/bin"
	current := os.Getenv("PATH")
	if current == "" {
		current = "/usr/bin:/bin:/usr/sbin:/sbin"
	}

	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, sh, "-ilc", "echo $PATH")
	cmd.Stderr = nil // suppress interactive shell noise (motd, prompts)
	out, err := cmd.Output()

	if err == nil {
		loginPath := strings.TrimSpace(string(out))
		// Sanity: must look like a PATH (contains at least one '/').
		if strings.Contains(loginPath, "/") {
			os.Setenv("PATH", loginPath)
			log.Printf("ccfly: PATH resolved from login shell (%d entries)", strings.Count(loginPath, ":")+1)
			return
		}
	}
	os.Setenv("PATH", fallbackExtras+":"+current)
	log.Printf("ccfly: PATH fallback (login shell probe failed), prepended %s", fallbackExtras)
}
