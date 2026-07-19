// Package svc installs a ccfly binary as a persistent OS service so a device
// stays joined to the overlay across terminal close / logout / reboot / sleep
// (macOS launchd, Linux systemd). User-level by default; --system for a
// system-wide service (needs root) that survives logout / multi-user, on par
// with a root mesh daemon.
//
// The service identity + command are described by a Profile. With no Profile
// the default `ccfly connect <target> --claude-dir <dir>` (tmux-requiring)
// agent is installed; ccfly-mesh supplies its own Profile for a lean, separate,
// tmux-free mesh-only service.
package svc

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jsdvjx/ccfly/go/internal/profile"
	"github.com/jsdvjx/ccfly/go/internal/tmuxbin"
)

// Options configures Install / Uninstall.
type Options struct {
	Target    string // "<host>/<code>"(连接码)或纯 "<host>"(无码配对) for `ccfly connect`
	System    bool   // system-wide service (needs root)
	ClaudeDir string // optional override; default <home>/.claude/projects
	DryRun    bool   // print what would happen, change nothing

	// ExtraArgs are appended to the default ccfly agent's connect command (e.g.
	// --overlay-forward ...). Ignored when Profile is set.
	ExtraArgs []string

	// Profile overrides the service identity + command. When nil the default
	// ccfly agent profile is used (preserving existing behavior).
	Profile *Profile
}

// IsAdmin 报告当前进程是否已持有管理员/超级用户权限(Windows=提权 token 属于
// Administrators 组,UAC 过滤 token 算否;Unix=euid 0)。Windows 的 install/uninstall
// 用它做前置闸门,cmd/ccfly 在交互式配对前也用它提前拦截。
func IsAdmin() bool { return isRoot() }

// Profile describes one installable service: its identity (names) and the exact
// command to run. Args is the full argv AFTER the binary path.
type Profile struct {
	DarwinLabel string   // launchd Label, e.g. com.ccfly.agent
	LinuxUnit   string   // systemd unit base name, e.g. ccfly
	BinName     string   // installed binary file name, e.g. ccfly
	Description string   // systemd unit Description / human text
	Args        []string // argv after the binary
	NeedsTmux   bool     // require tmux on PATH (fail install if missing)
}

const (
	darwinLabel = "com.ccfly.agent"
	linuxUnit   = "ccfly"
)

// resolve returns the effective Profile: an explicit o.Profile, or the default
// ccfly agent (`connect <target> --claude-dir <dir>`, tmux-requiring).
func (o Options) resolve(home string) Profile {
	if o.Profile != nil {
		return *o.Profile
	}
	args := []string{"connect", o.Target, "--claude-dir", claudeDirOf(o, home)}
	args = append(args, o.ExtraArgs...)
	return Profile{
		DarwinLabel: darwinLabel,
		LinuxUnit:   linuxUnit,
		BinName:     "ccfly",
		Description: "ccfly overlay agent",
		Args:        args,
		NeedsTmux:   runtime.GOOS != "windows",
	}
}

// Install sets up the persistent service for the current platform.
func Install(o Options) error {
	if !profile.Current().Install {
		return fmt.Errorf("当前能力档(profile=%s):不可安装常驻服务", profile.Current().Mode)
	}
	switch runtime.GOOS {
	case "darwin":
		return installDarwin(o)
	case "linux":
		return installLinux(o)
	case "windows":
		return installWindows(o)
	default:
		return fmt.Errorf("ccfly install: unsupported OS %q", runtime.GOOS)
	}
}

// Uninstall removes the service.
func Uninstall(o Options) error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallDarwin(o)
	case "linux":
		return uninstallLinux(o)
	case "windows":
		return uninstallWindows(o)
	default:
		return fmt.Errorf("ccfly uninstall: unsupported OS %q", runtime.GOOS)
	}
}

// InstallSNIHelper 安装 macOS SNI 的 root 特权 helper LaunchDaemon(承接 :443/:53 + /etc/resolver,让非 root
// 的 agent 也能 arm SNI 出口)。仅 darwin 有意义且需 root;其余平台或非 root → (false,nil) no-op。
// installed 报告是否真的装了(供调用方决定提示文案)。
func InstallSNIHelper(dryRun bool) (installed bool, err error) {
	if runtime.GOOS != "darwin" {
		return false, nil
	}
	return installSNIHelperDarwin(dryRun)
}

// UninstallSNIHelper 摘掉 macOS SNI root helper。非 darwin → no-op。
func UninstallSNIHelper(dryRun bool) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return uninstallSNIHelperDarwin(dryRun)
}

// bootoutUserAgent 摘掉可能残留的同名用户级 LaunchAgent(~/Library/LaunchAgents/<label>.plist),
// 装 macOS system 服务前调用,避免二者用同一设备身份双实例互顶 /mesh(30s flap)。best-effort:先在该
// 用户 GUI 域 bootout(以 root 跑须指定 gui/<uid>,否则加载在 system 域、摘不到用户 agent),再删 plist。
// 仅 darwin install 路径调用;函数本身只用跨平台原语,故放共享区。
func bootoutUserAgent(home, label string, uid int) {
	userPlist := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	if _, err := os.Stat(userPlist); err != nil {
		return // 无用户级残留
	}
	_ = run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, label)) // 新式;摘不到就忽略
	_ = run("launchctl", "unload", userPlist)                             // 老式兜底
	_ = os.Remove(userPlist)
	fmt.Printf("✓ 已移除残留的用户级 LaunchAgent %s(避免与 system 服务双实例互顶)\n", label)
}

// ── shared helpers ──

func claudeDirOf(o Options, home string) string {
	if o.ClaudeDir != "" {
		return o.ClaudeDir
	}
	return filepath.Join(home, ".claude", "projects")
}

func selfPath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, e := filepath.EvalSymlinks(p); e == nil {
		p = r
	}
	return p, nil
}

func copyExe(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func validate(o Options) error {
	if strings.TrimSpace(o.Target) == "" {
		return fmt.Errorf("missing <host>[/<code>]")
	}
	if o.System && !o.DryRun && !isRoot() {
		return fmt.Errorf("--system needs root: re-run with sudo")
	}
	return nil
}

// servicePATH builds the PATH the installed service runs with: the standard
// dirs, with tmux's own dir first when tmux is found (so launchd/systemd's
// minimal PATH still resolves it). When needTmux and tmux is absent it errors,
// so `ccfly install` fails fast rather than leaving a service that dies; a
// mesh-only service (needTmux=false) does not require tmux.
//
// bundleDir 非空且本构建内嵌了 tmux(darwin,见 internal/tmuxbin)时,系统找不到
// tmux 不再报错,改用 bundleDir/tmux 兜底并返回 bundled=true —— 实际释放由调用方
// 在非 DryRun 时做(servicePATH 自身保持只读)。
func servicePATH(needTmux bool, bundleDir string) (pathEnv, tmuxPath string, bundled bool, err error) {
	std := []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	tmuxPath, _ = exec.LookPath("tmux")
	if tmuxPath == "" { // PATH may be minimal (e.g. under sudo) — scan common dirs
		for _, d := range std {
			cand := filepath.Join(d, "tmux")
			if fi, e := os.Stat(cand); e == nil && !fi.IsDir() {
				tmuxPath = cand
				break
			}
		}
	}
	if tmuxPath == "" && needTmux && bundleDir != "" && tmuxbin.Bundled() {
		tmuxPath, bundled = filepath.Join(bundleDir, "tmux"), true
	}
	if tmuxPath == "" {
		if needTmux {
			return "", "", false, fmt.Errorf("tmux 未找到 —— 先安装(macOS: `brew install tmux`,Linux: `apt/yum install tmux`);ccfly 的终端/会话控制依赖 tmux")
		}
		return strings.Join(std, ":"), "", false, nil
	}
	dirs := []string{filepath.Dir(tmuxPath)}
	seen := map[string]bool{dirs[0]: true}
	for _, d := range std {
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	return strings.Join(dirs, ":"), tmuxPath, bundled, nil
}

func xmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// ── macOS (launchd) ──

func installDarwin(o Options) error {
	if err := validate(o); err != nil {
		return err
	}
	name, home, uid, gid, err := runAs(o.System)
	if err != nil {
		return err
	}
	p := o.resolve(home)
	// 内置 tmux 的落点固定在 ~/.ccfly/bin(ccfly 自有工具目录;与运行时兜底
	// ensureBundledTmux 同一位置,system/user 两档一致,chownTree 顺带覆盖属主)。
	bundleDir := filepath.Join(home, ".ccfly", "bin")
	svcPATH, tmuxPath, bundledTmux, err := servicePATH(p.NeedsTmux, bundleDir)
	if err != nil {
		return fmt.Errorf("ccfly install: %w", err)
	}
	switch {
	case bundledTmux:
		fmt.Printf("✓ tmux: %s(系统未装 tmux,使用 ccfly 内置)\n", tmuxPath)
	case tmuxPath != "":
		fmt.Printf("✓ tmux: %s\n", tmuxPath)
	}
	self, err := selfPath()
	if err != nil {
		return err
	}
	logPath := filepath.Join(home, ".ccfly", p.BinName+".log")

	var binPath, plistPath, userElem string
	if o.System {
		binPath = "/usr/local/bin/" + p.BinName
		plistPath = "/Library/LaunchDaemons/" + p.DarwinLabel + ".plist"
		userElem = "  <key>UserName</key><string>" + name + "</string>\n"
	} else {
		binPath = filepath.Join(home, ".ccfly", "bin", p.BinName)
		plistPath = filepath.Join(home, "Library", "LaunchAgents", p.DarwinLabel+".plist")
	}

	var prog strings.Builder
	prog.WriteString("    <string>" + xmlEsc(binPath) + "</string>\n")
	for _, a := range p.Args {
		prog.WriteString("    <string>" + xmlEsc(a) + "</string>\n")
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
%s  <key>ProgramArguments</key><array>
%s  </array>
  <key>EnvironmentVariables</key><dict><key>HOME</key><string>%s</string><key>PATH</key><string>%s</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, p.DarwinLabel, userElem, prog.String(), home, svcPATH, logPath, logPath)

	if o.DryRun {
		fmt.Printf("# bin   -> %s (copy of %s)\n# plist -> %s\n# run as %s, HOME=%s\n\n%s", binPath, self, plistPath, name, home, plist)
		return nil
	}

	killStaleProcesses() // 清掉游离旧实例:多实例互顶 mesh 连接会 30s 规律断连
	if o.System {
		// 装 system 服务前,清掉可能残留的同名**用户级 LaunchAgent**——否则它被 launchd
		// KeepAlive 重启,与新 system 服务用同一设备身份双实例互顶 /mesh(30s flap)。best-effort。
		bootoutUserAgent(home, p.DarwinLabel, uid)
	}

	if err := copyExe(self, binPath, 0o755); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	if bundledTmux {
		if _, err := tmuxbin.EnsureAt(bundleDir); err != nil {
			return fmt.Errorf("install bundled tmux: %w", err)
		}
	}
	// log/state dir must be writable by the run user
	ccflyDir := filepath.Join(home, ".ccfly")
	_ = os.MkdirAll(ccflyDir, 0o755)
	if o.System {
		_ = os.Chown(binPath, 0, 0)
		_ = chownTree(ccflyDir, uid, gid)
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	domain := "system"
	if !o.System {
		domain = "user"
	}
	_ = run("launchctl", "unload", plistPath) // best-effort if already loaded
	if err := run("launchctl", "load", "-w", plistPath); err != nil {
		return fmt.Errorf("launchctl load: %w", err)
	}
	fmt.Printf("✓ installed launchd %s service %s\n  bin: %s\n  log: %s\n  uninstall: %s uninstall%s\n",
		domain, p.DarwinLabel, binPath, logPath, p.BinName, systemFlag(o.System))
	return nil
}

func uninstallDarwin(o Options) error {
	if o.System && !isRoot() {
		return fmt.Errorf("--system needs root: re-run with sudo")
	}
	_, home, _, _, err := runAs(o.System)
	if err != nil {
		return err
	}
	p := o.resolve(home)
	plistPath := filepath.Join(home, "Library", "LaunchAgents", p.DarwinLabel+".plist")
	if o.System {
		plistPath = "/Library/LaunchDaemons/" + p.DarwinLabel + ".plist"
	}
	if o.DryRun {
		fmt.Printf("# would unload + rm %s\n", plistPath)
		return nil
	}
	_ = run("launchctl", "unload", plistPath)
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Printf("✓ removed %s\n", plistPath)
	return nil
}

// ── Linux (systemd) ──

func installLinux(o Options) error {
	if err := validate(o); err != nil {
		return err
	}
	name, home, _, _, err := runAs(o.System)
	if err != nil {
		return err
	}
	p := o.resolve(home)
	// Linux 构建不内嵌 tmux(bundleDir 传空即保持原「fail fast」行为;发行版装 tmux 是常态)。
	svcPATH, tmuxPath, _, err := servicePATH(p.NeedsTmux, "")
	if err != nil {
		return fmt.Errorf("ccfly install: %w", err)
	}
	if tmuxPath != "" {
		fmt.Printf("✓ tmux: %s\n", tmuxPath)
	}
	self, err := selfPath()
	if err != nil {
		return err
	}

	var binPath, unitPath, userLine string
	if o.System {
		binPath = "/usr/local/bin/" + p.BinName
		unitPath = "/etc/systemd/system/" + p.LinuxUnit + ".service"
		userLine = "User=" + name + "\n"
	} else {
		binPath = filepath.Join(home, ".ccfly", "bin", p.BinName)
		unitPath = filepath.Join(home, ".config", "systemd", "user", p.LinuxUnit+".service")
	}

	execStart := binPath
	if len(p.Args) > 0 {
		execStart += " " + strings.Join(p.Args, " ")
	}

	unit := fmt.Sprintf(`[Unit]
Description=%s
After=network-online.target
Wants=network-online.target

[Service]
%sEnvironment=HOME=%s
Environment=PATH=%s
ExecStart=%s
Restart=always
RestartSec=3

[Install]
WantedBy=%s
`, p.Description, userLine, home, svcPATH, execStart, wantedBy(o.System))

	if o.DryRun {
		fmt.Printf("# bin  -> %s (copy of %s)\n# unit -> %s\n\n%s", binPath, self, unitPath, unit)
		return nil
	}

	killStaleProcesses() // 清掉游离旧实例:多实例互顶 mesh 连接会 30s 规律断连

	if err := copyExe(self, binPath, 0o755); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}

	// No systemd (bare container / minimal image): we can't register a unit, but
	// pairing already succeeded, so keep the device online by launching the agent
	// detached — and tell the user how to make it persist (their entrypoint / init).
	// Far better than hard-failing with `systemctl: not found` after pairing.
	if !hasSystemd() {
		logPath := filepath.Join(home, ".ccfly", p.BinName+".log")
		_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
		env := append(os.Environ(), "HOME="+home, "PATH="+svcPATH)
		if err := startDetachedAgent(binPath, p.Args, env, logPath); err != nil {
			return fmt.Errorf("未检测到 systemd,且后台直接启动失败: %w", err)
		}
		fmt.Printf("✓ 未检测到 systemd:已在后台直接启动(容器/精简系统)\n  bin: %s\n  logs: %s\n  ⚠ 重启不会自动拉起——如需持久,请把这条加入容器 entrypoint / 开机脚本:\n    %s %s\n",
			binPath, logPath, binPath, strings.Join(p.Args, " "))
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	if o.System {
		_ = run("systemctl", "daemon-reload")
		if err := run("systemctl", "enable", "--now", p.LinuxUnit); err != nil {
			return fmt.Errorf("systemctl enable: %w", err)
		}
	} else {
		_ = run("systemctl", "--user", "daemon-reload")
		if err := run("systemctl", "--user", "enable", "--now", p.LinuxUnit); err != nil {
			return fmt.Errorf("systemctl --user enable: %w", err)
		}
		// survive logout (best-effort; may need root on some distros)
		_ = run("loginctl", "enable-linger", name)
	}
	fmt.Printf("✓ installed systemd %s service %s\n  bin: %s\n  logs: journalctl%s -u %s -f\n  uninstall: %s uninstall%s\n",
		domainOf(o.System), p.LinuxUnit, binPath, userJournalFlag(o.System), p.LinuxUnit, p.BinName, systemFlag(o.System))
	return nil
}

func uninstallLinux(o Options) error {
	name, home, _, _, err := runAs(o.System)
	if err != nil {
		return err
	}
	p := o.resolve(home)
	if o.DryRun {
		fmt.Printf("# would disable + remove systemd unit %s (%s)\n", p.LinuxUnit, domainOf(o.System))
		return nil
	}
	if o.System {
		if !isRoot() {
			return fmt.Errorf("--system needs root: re-run with sudo")
		}
		_ = run("systemctl", "disable", "--now", p.LinuxUnit)
		_ = os.Remove("/etc/systemd/system/" + p.LinuxUnit + ".service")
		_ = run("systemctl", "daemon-reload")
	} else {
		_ = run("systemctl", "--user", "disable", "--now", p.LinuxUnit)
		_ = os.Remove(filepath.Join(home, ".config", "systemd", "user", p.LinuxUnit+".service"))
		_ = run("systemctl", "--user", "daemon-reload")
	}
	_ = name
	fmt.Printf("✓ removed systemd %s service %s\n", domainOf(o.System), p.LinuxUnit)
	return nil
}

// ── small format helpers ──

func systemFlag(system bool) string {
	if system {
		return " --system"
	}
	return ""
}
func domainOf(system bool) string {
	if system {
		return "system"
	}
	return "user"
}
func wantedBy(system bool) string {
	if system {
		return "multi-user.target"
	}
	return "default.target"
}
func userJournalFlag(system bool) string {
	if system {
		return ""
	}
	return " --user"
}
