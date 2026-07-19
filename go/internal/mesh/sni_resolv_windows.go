//go:build windows

package mesh

// sni_resolv_windows.go — Windows 用 **hosts 文件**把 intercept 主机名钉到 loopback（127.0.0.1/::1），
// 不用 NRPT、不用本地 :53 DNS。原因见 sni_hosts.go：DNS 型代理（Clash/sing-box fake-ip）占 :53 会让
// NRPT 方案整体回滚；hosts 优先级高于 fake-ip、loopback 不进 TUN → 与代理共存。代价=无通配，逐主机名
// 精确钉（sniPinnedHosts 静态维护）。局部块替换，绝不动用户已有条目。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolverNeedsLocalDNS 报告本平台是否需要 ccfly 起本地 :53 DNS（把 NRPT/resolv.conf 的查询导过来）。
// Windows 走 hosts 直钉，不需要 :53（也因此躲开 Clash 的 :53 冲突）。
func resolverNeedsLocalDNS() bool { return false }

// hostsFilePath 返回系统 hosts 路径（尊重 %SystemRoot%，默认 C:\Windows）。
func hostsFilePath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "drivers", "etc", "hosts")
}

// flushDNS 刷新 Windows DNS 解析缓存，让 hosts 改动立即生效（best-effort）。
func flushDNS() { _ = exec.Command("ipconfig", "/flushdns").Run() }

// pointResolver 把 sniPinnedHosts 精确主机名写进 hosts 的 ccfly 托管块（局部替换）。
// intercept/upstream 在 hosts 方案里不用（hosts 无通配、无上游转发；钉哪些主机由静态 sniPinnedHosts 决定）。
// 需管理员（写 %SystemRoot%\System32\drivers\etc\hosts）。
func pointResolver(intercept []string, upstream string, pinned []string) error {
	_, _ = intercept, upstream
	if len(pinned) == 0 {
		pinned = sniPinnedHosts
	}
	p := hostsFilePath()
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	out := applyCcflyHostsBlock(string(b), pinned)
	if strings.TrimRight(out, "\r\n") == strings.TrimRight(string(b), "\r\n") {
		return nil // 内容等价（含块），免写盘/免刷缓存
	}
	if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
		return err // 通常是非管理员（hosts 只读）
	}
	flushDNS()
	return nil
}

// restoreResolver 删除 hosts 里的 ccfly 托管块，恢复用户原文。幂等（无块=no-op）。
func restoreResolver() error {
	p := hostsFilePath()
	b, err := os.ReadFile(p)
	if err != nil {
		return nil // 文件不存在 = 没写过
	}
	cleaned := strings.TrimRight(stripCcflyHostsBlock(string(b)), "\r\n")
	if cleaned != "" {
		cleaned += "\r\n"
	}
	if cleaned == string(b) {
		return nil // 无块
	}
	if err := os.WriteFile(p, []byte(cleaned), 0o644); err != nil {
		return err
	}
	flushDNS()
	return nil
}
