// Package svc installs `ccfly connect` as a persistent OS service so a device
// stays joined to the overlay across terminal close / logout / reboot / sleep
// (macOS launchd, Linux systemd). User-level by default; --system for a
// system-wide service (needs root) that survives logout / multi-user, on par
// with a root mesh daemon.
package svc

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Options configures Install / Uninstall.
type Options struct {
	Target    string // "<host>/<code>"(连接码)或纯 "<host>"(无码配对) for `ccfly connect`
	System    bool   // system-wide service (needs root)
	ClaudeDir string // optional override; default <home>/.claude/projects
	DryRun    bool   // print what would happen, change nothing
}

const (
	darwinLabel = "com.ccfly.agent"
	linuxUnit   = "ccfly"
)

// Install sets up the persistent service for the current platform.
func Install(o Options) error {
	switch runtime.GOOS {
	case "darwin":
		return installDarwin(o)
	case "linux":
		return installLinux(o)
	default:
		return fmt.Errorf("ccfly install: unsupported OS %q (macOS/Linux only)", runtime.GOOS)
	}
}

// Uninstall removes the service.
func Uninstall(o Options) error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallDarwin(o)
	case "linux":
		return uninstallLinux(o)
	default:
		return fmt.Errorf("ccfly uninstall: unsupported OS %q", runtime.GOOS)
	}
}

// ── shared helpers ──

// runAs resolves the user the service should run as (and their home). For a
// system service started via sudo we use the invoking user (SUDO_USER); for a
// user service it's the current user.
func runAs(system bool) (name, home string, uid, gid int, err error) {
	var u *user.User
	if system {
		if n := os.Getenv("SUDO_USER"); n != "" && n != "root" {
			u, err = user.Lookup(n)
		} else {
			u, err = user.Current()
		}
	} else {
		u, err = user.Current()
	}
	if err != nil {
		return "", "", -1, -1, err
	}
	uid, _ = strconv.Atoi(u.Uid)
	gid, _ = strconv.Atoi(u.Gid)
	return u.Username, u.HomeDir, uid, gid, nil
}

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
	// Target 可为 "<host>/<code>"(连接码流程)或纯 "<host>"(无码配对:配对已在
	// install 阶段交互式完成、身份落盘,服务只需 `connect <host>` 凭旧身份重连)。
	// 两种都合法,这里只校验非空。
	if strings.TrimSpace(o.Target) == "" {
		return fmt.Errorf("missing <host>[/<code>]")
	}
	if o.System && !o.DryRun && os.Geteuid() != 0 {
		return fmt.Errorf("--system needs root: re-run with sudo")
	}
	return nil
}

// toolPATH locates tmux (ccfly needs it for terminal / session control) and
// returns a PATH string with tmux's own dir first — so the installed service
// resolves tmux even under launchd/systemd's minimal PATH (which omits
// /opt/homebrew/bin). It errors if tmux isn't installed at all, so `ccfly
// install` fails fast instead of leaving a service that dies at runtime.
func toolPATH() (pathEnv, tmuxPath string, err error) {
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
	if tmuxPath == "" {
		return "", "", fmt.Errorf("tmux 未找到 —— 先安装(如 `brew install tmux`);ccfly 的终端/会话控制依赖 tmux")
	}
	dirs := []string{filepath.Dir(tmuxPath)}
	seen := map[string]bool{dirs[0]: true}
	for _, d := range std {
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	return strings.Join(dirs, ":"), tmuxPath, nil
}

// ── macOS (launchd) ──

func installDarwin(o Options) error {
	if err := validate(o); err != nil {
		return err
	}
	svcPATH, tmuxPath, err := toolPATH()
	if err != nil {
		return fmt.Errorf("ccfly install: %w", err)
	}
	fmt.Printf("✓ tmux: %s\n", tmuxPath)
	name, home, uid, gid, err := runAs(o.System)
	if err != nil {
		return err
	}
	self, err := selfPath()
	if err != nil {
		return err
	}
	claude := claudeDirOf(o, home)
	logPath := filepath.Join(home, ".ccfly", "ccfly.log")

	var binPath, plistPath, userElem string
	if o.System {
		binPath = "/usr/local/bin/ccfly"
		plistPath = "/Library/LaunchDaemons/" + darwinLabel + ".plist"
		userElem = "  <key>UserName</key><string>" + name + "</string>\n"
	} else {
		binPath = filepath.Join(home, ".ccfly", "bin", "ccfly")
		plistPath = filepath.Join(home, "Library", "LaunchAgents", darwinLabel+".plist")
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
%s  <key>ProgramArguments</key><array>
    <string>%s</string>
    <string>connect</string>
    <string>%s</string>
    <string>--claude-dir</string>
    <string>%s</string>
  </array>
  <key>EnvironmentVariables</key><dict><key>HOME</key><string>%s</string><key>PATH</key><string>%s</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, darwinLabel, userElem, binPath, o.Target, claude, home, svcPATH, logPath, logPath)

	if o.DryRun {
		fmt.Printf("# bin   -> %s (copy of %s)\n# plist -> %s\n# run as %s, HOME=%s\n\n%s", binPath, self, plistPath, name, home, plist)
		return nil
	}

	if err := copyExe(self, binPath, 0o755); err != nil {
		return fmt.Errorf("install binary: %w", err)
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
	fmt.Printf("✓ installed launchd %s service %s\n  bin: %s\n  log: %s\n  uninstall: ccfly uninstall%s\n",
		domain, darwinLabel, binPath, logPath, systemFlag(o.System))
	return nil
}

func uninstallDarwin(o Options) error {
	if o.System && os.Geteuid() != 0 {
		return fmt.Errorf("--system needs root: re-run with sudo")
	}
	_, home, _, _, err := runAs(o.System)
	if err != nil {
		return err
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", darwinLabel+".plist")
	if o.System {
		plistPath = "/Library/LaunchDaemons/" + darwinLabel + ".plist"
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
	svcPATH, tmuxPath, err := toolPATH()
	if err != nil {
		return fmt.Errorf("ccfly install: %w", err)
	}
	fmt.Printf("✓ tmux: %s\n", tmuxPath)
	name, home, _, _, err := runAs(o.System)
	if err != nil {
		return err
	}
	self, err := selfPath()
	if err != nil {
		return err
	}
	claude := claudeDirOf(o, home)

	var binPath, unitPath, userLine string
	if o.System {
		binPath = "/usr/local/bin/ccfly"
		unitPath = "/etc/systemd/system/" + linuxUnit + ".service"
		userLine = "User=" + name + "\n"
	} else {
		binPath = filepath.Join(home, ".ccfly", "bin", "ccfly")
		unitPath = filepath.Join(home, ".config", "systemd", "user", linuxUnit+".service")
	}

	unit := fmt.Sprintf(`[Unit]
Description=ccfly overlay agent
After=network-online.target
Wants=network-online.target

[Service]
%sEnvironment=HOME=%s
Environment=PATH=%s
ExecStart=%s connect %s --claude-dir %s
Restart=always
RestartSec=3

[Install]
WantedBy=%s
`, userLine, home, svcPATH, binPath, o.Target, claude, wantedBy(o.System))

	if o.DryRun {
		fmt.Printf("# bin  -> %s (copy of %s)\n# unit -> %s\n\n%s", binPath, self, unitPath, unit)
		return nil
	}

	if err := copyExe(self, binPath, 0o755); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	if o.System {
		_ = run("systemctl", "daemon-reload")
		if err := run("systemctl", "enable", "--now", linuxUnit); err != nil {
			return fmt.Errorf("systemctl enable: %w", err)
		}
	} else {
		_ = run("systemctl", "--user", "daemon-reload")
		if err := run("systemctl", "--user", "enable", "--now", linuxUnit); err != nil {
			return fmt.Errorf("systemctl --user enable: %w", err)
		}
		// survive logout (best-effort; may need root on some distros)
		_ = run("loginctl", "enable-linger", name)
	}
	fmt.Printf("✓ installed systemd %s service %s\n  bin: %s\n  logs: journalctl%s -u %s -f\n  uninstall: ccfly uninstall%s\n",
		domainOf(o.System), linuxUnit, binPath, userJournalFlag(o.System), linuxUnit, systemFlag(o.System))
	return nil
}

func uninstallLinux(o Options) error {
	name, _, _, _, err := runAs(o.System)
	if err != nil {
		return err
	}
	if o.DryRun {
		fmt.Printf("# would disable + remove systemd unit %s (%s)\n", linuxUnit, domainOf(o.System))
		return nil
	}
	if o.System {
		if os.Geteuid() != 0 {
			return fmt.Errorf("--system needs root: re-run with sudo")
		}
		_ = run("systemctl", "disable", "--now", linuxUnit)
		_ = os.Remove("/etc/systemd/system/" + linuxUnit + ".service")
		_ = run("systemctl", "daemon-reload")
	} else {
		_ = run("systemctl", "--user", "disable", "--now", linuxUnit)
		home, _ := os.UserHomeDir()
		_ = os.Remove(filepath.Join(home, ".config", "systemd", "user", linuxUnit+".service"))
		_ = run("systemctl", "--user", "daemon-reload")
	}
	_ = name
	fmt.Printf("✓ removed systemd %s service %s\n", domainOf(o.System), linuxUnit)
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

func chownTree(root string, uid, gid int) error {
	return filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		_ = os.Chown(p, uid, gid)
		return nil
	})
}
