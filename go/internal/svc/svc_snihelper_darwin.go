//go:build darwin

package svc

// svc_snihelper_darwin.go — 安装 macOS SNI 的 root 特权 helper LaunchDaemon(com.ccfly.sni-helper)。
//
// 与主 agent 单元的关键区别:**不写 UserName 键 → 真以 root 跑**(主 agent 单元即便 --system 也会写
// UserName 降到用户,才能共用 tmux/~/.claude 镜像会话;而 helper 只需 root 绑 :443 与写 /etc/hosts,
// 不碰会话)。装它必须已是 root(sudo),非 root 时返回 (false,nil) 让调用方提示改用 sudo。

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const sniHelperLabel = "com.ccfly.sni-helper"

func sniHelperPaths() (bin, plist, logPath string) {
	return "/usr/local/bin/ccfly",
		"/Library/LaunchDaemons/" + sniHelperLabel + ".plist",
		"/var/log/ccfly-sni-helper.log"
}

// installSNIHelperDarwin 装 root helper 守护。需 root:非 root → (false,nil)(不是错误,调用方据此提示)。
func installSNIHelperDarwin(dryRun bool) (bool, error) {
	if !dryRun && !isRoot() {
		return false, nil // 装不了 root daemon;主流程会提示「sudo 重跑」
	}
	bin, plistPath, logPath := sniHelperPaths()
	self, err := selfPath()
	if err != nil {
		return false, err
	}
	agentUID, err := sniHelperAgentUID()
	if err != nil {
		return false, err
	}
	// 注意:无 UserName 键 = 以 root 运行。
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array>
    <string>%s</string>
    <string>sni-helper</string>
  </array>
  <key>EnvironmentVariables</key><dict>
    <key>CCFLY_SNI_HELPER_UID</key><string>%d</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, sniHelperLabel, xmlEsc(bin), agentUID, xmlEsc(logPath), xmlEsc(logPath))

	if dryRun {
		fmt.Printf("# sni-helper bin   -> %s\n# sni-helper plist -> %s (root, 无 UserName)\n\n%s", bin, plistPath, plist)
		return true, nil
	}
	// 确保 root 拥有的 ccfly 在 /usr/local/bin(system agent 装时已铺好;缺则补一份)。
	if _, statErr := os.Stat(bin); statErr != nil {
		if err := copyExe(self, bin, 0o755); err != nil {
			return false, fmt.Errorf("install sni-helper binary: %w", err)
		}
	}
	_ = os.Chown(bin, 0, 0)
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return false, fmt.Errorf("write sni-helper plist: %w", err)
	}
	_ = run("launchctl", "unload", plistPath) // best-effort if already loaded
	if err := run("launchctl", "load", "-w", plistPath); err != nil {
		return false, fmt.Errorf("launchctl load sni-helper: %w", err)
	}
	return true, nil
}

func sniHelperAgentUID() (int, error) {
	if raw := strings.TrimSpace(os.Getenv("SUDO_UID")); raw != "" {
		if uid, err := strconv.Atoi(raw); err == nil && uid > 0 {
			return uid, nil
		}
	}
	if uid := os.Getuid(); uid > 0 { // dry-run or direct per-user invocation
		return uid, nil
	}
	if fi, err := os.Stat("/dev/console"); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid > 0 {
			return int(st.Uid), nil
		}
	}
	return 0, fmt.Errorf("cannot determine the non-root agent UID; run install from the logged-in user with sudo")
}

// uninstallSNIHelperDarwin 摘掉 root helper 守护。非 root 静默跳过(装不了也拆不了)。
func uninstallSNIHelperDarwin(dryRun bool) error {
	_, plistPath, _ := sniHelperPaths()
	if dryRun {
		fmt.Printf("# would unload + rm %s\n", plistPath)
		return nil
	}
	if !isRoot() {
		return nil
	}
	_ = run("launchctl", "unload", plistPath)
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
