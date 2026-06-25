// Command ccfly-hostd is the ccfly host agent. It runs on a multi-core VM, joins
// the cc.hn overlay (like ccfly-mesh), and exposes a small spawn/stop control API
// ON THE OVERLAY — reachable only from the cloud gateway (source-prefix 100.64.0.1
// via the expose bridge). The cloud drives it over the overlay to `docker run`
// per-user ccfly instance containers on this VM.
//
// It deliberately carries no Claude / tmux / control-service code: it only joins
// the mesh and shells out to `docker`. Intended to run under the `host` capability
// profile (MeshJoin + OverlayBridge + Install; claude/proxy/uisync off) — build it
// with -ldflags "-X github.com/ccfly/ccfly/go/internal/profile.defaultMode=host",
// or set /etc/ccfly/profile.json {"mode":"host"}.
//
// Usage:
//
//	ccfly-hostd [flags] <host>[/<code>]                     # join overlay + serve spawn API
//	ccfly-hostd install [flags] <host>[/<code>] [--system]  # join once, then install a persistent service
//	ccfly-hostd uninstall [--system]                        # remove that service
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ccfly/ccfly/go/internal/hostagent"
	"github.com/ccfly/ccfly/go/internal/mesh"
	"github.com/ccfly/ccfly/go/internal/profile"
	"github.com/ccfly/ccfly/go/internal/svc"
)

var version = "dev"

// overlayAgentPort 是 host-agent 在 overlay 上监听 spawn API 的端口(区别于实例 control 的 7699,
// 必须与 cloud 侧 hostspawn dial 的端口一致)。
const overlayAgentPort = 7700

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "version", "-version", "--version":
			fmt.Println("ccfly-hostd", version)
			return
		case "help", "-h", "--help":
			usage()
			return
		}
	}
	// 能力闸门:host-agent 需要接入能力(MeshJoin)。host 档 / full 满足;instance / restricted 拒。
	if !profile.Current().MeshJoin {
		fmt.Fprintf(os.Stderr, "ccfly-hostd: 当前能力档(profile=%s)下组网功能已禁用\n", profile.Current().Mode)
		os.Exit(1)
	}
	if len(args) > 0 {
		switch args[0] {
		case "install":
			exit(runInstall(args[1:]))
		case "uninstall":
			exit(runUninstall(args[1:]))
		}
	}
	exit(runConnect(args)) // default: join overlay + serve spawn API
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ccfly-hostd:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func usage() {
	fmt.Fprint(os.Stderr, `ccfly-hostd — ccfly 主机代理(VM 上跑;接入 overlay,经 overlay 受 cloud 指挥 docker run 实例容器)

Usage:
  ccfly-hostd [flags] <host>[/<code>]
      接入 overlay 并在 overlay 上提供 spawn/stop API(仅 cloud 网关 100.64.0.1 可达)。
        • <host>         无码网页配对(打开浏览器登录批准)
        • <host>/<code>  连接码(非交互)
  ccfly-hostd install [flags] <host>[/<code>] [--system]
      先完成一次接入,再安装常驻服务(launchd/systemd)。--system = 系统级(sudo)。
  ccfly-hostd uninstall [--system]
      移除常驻服务。

Env:
  CCFLY_HOST_AGENT_TOKEN  spawn API 的 Bearer 令牌(cloud 调用时须带;留空=仅靠 overlay 源 IP 白名单)
  CCFLY_HOST_DOCKER       docker 可执行名(默认 "docker")

注:本机须已装 docker,且运行 ccfly-hostd 的用户能访问 docker daemon(在 docker 组,或 system 级 + root)。
`)
}

// runConnect 起本地 host-agent HTTP(回环),用 expose 桥把它暴露到 overlay:7700(仅放行 cloud
// 网关),再接入 overlay 持隧道(阻塞直到信号)。
func runConnect(args []string) error {
	fs := flag.NewFlagSet("ccfly-hostd", flag.ExitOnError)
	fs.Usage = usage
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 || fs.Arg(0) == "" {
		usage()
		os.Exit(2)
	}
	target := fs.Arg(0)

	// 1) 本地起 host-agent 控制 HTTP(回环 ephemeral 端口)。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start host-agent: %w", err)
	}
	agentPort := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: hostagent.Handler(hostagent.Config{Token: hostagent.LoadToken()})}
	go func() { _ = srv.Serve(ln) }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); _ = srv.Close() }()

	// 2) netstack 模式(expose 桥只在 netstack 生效)+ 关 control 反代(否则与 expose 在 overlayIP:7700 端口冲突)。
	mesh.SetKernelMode(false)
	mesh.SetControlProxyEnabled(false)
	// 3) expose 桥:overlay:7700 → 127.0.0.1:<agentPort>,源前缀仅放行 cloud 网关 100.64.0.1。
	if err := mesh.SetOverlayExpose(fmt.Sprintf("%d:%d@100.64.0.1/32", overlayAgentPort, agentPort)); err != nil {
		return fmt.Errorf("overlay-expose: %w", err)
	}
	fmt.Printf("ccfly-hostd: host-agent on 127.0.0.1:%d, exposed to overlay :%d (cloud-only)\n", agentPort, overlayAgentPort)
	// 4) 接入 overlay 并持隧道。
	return mesh.Connect(ctx, target, version)
}

// runInstall 先完成一次接入(无码则网页配对),再安装常驻服务跑 `ccfly-hostd <host>`。
func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	system := fs.Bool("system", false, "system-wide service (needs sudo; survives logout/reboot)")
	dry := fs.Bool("dry-run", false, "print what would be done; change nothing")
	fs.Usage = usage
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
	if isNoCode(target) && !*dry {
		fmt.Println("ccfly-hostd install: 先完成一次接入配对,再安装常驻服务…")
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err := mesh.Pair(ctx, target, version)
		stop()
		if err != nil {
			return fmt.Errorf("配对失败,未安装服务: %w", err)
		}
	}
	prof := hostProfile()
	prof.Args = []string{target}
	return svc.Install(svc.Options{Target: target, System: *system, DryRun: *dry, Profile: &prof})
}

func runUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	system := fs.Bool("system", false, "remove the system-wide service (needs sudo)")
	dry := fs.Bool("dry-run", false, "print what would be done; change nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prof := hostProfile()
	return svc.Uninstall(svc.Options{System: *system, DryRun: *dry, Profile: &prof})
}

func hostProfile() svc.Profile {
	return svc.Profile{
		DarwinLabel: "com.ccfly.hostd",
		LinuxUnit:   "ccfly-hostd",
		BinName:     "ccfly-hostd",
		Description: "ccfly host agent (docker spawner)",
		NeedsTmux:   false,
	}
}

// isNoCode 判定 target 是否为纯 host(无码网页配对)。口径同 ccfly-mesh / mesh 内部。
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
