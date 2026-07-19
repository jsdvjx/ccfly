//go:build windows

package mesh

// sni_resolv_windows.go — Windows 把系统解析指向本地 SNI DNS(127.0.0.1:53)。
//
// 2026-07-19 三端统一:与 Linux 同构——内嵌 CoreDNS on :53 + 系统 DNS 指过来,不再用 hosts 精确钉。
// 统一后域名清单由 DNS 策略服务自持 OSS(dnspolicy.go),通配 apex 覆盖所有子域(告别 hosts 无通配、
// 逐主机名维护的时代)。
//
// 方案:每个活动网卡 首选 DNS=127.0.0.1、次级=清单首个上游(fail-open:本地 DNS 挂了仍经次级解析,
// 不 brick);原配置备份到 %ProgramData%\ccfly\dns-backup.json,卸载按备份恢复(DHCP 接口改回自动)。
// 需要管理员(改网卡 DNS;ccfly 的 Windows 任务本就 HighestAvailable 注册)。
//
// 已知取舍:DNS 型代理(Clash/sing-box fake-ip)占 :53 时 CoreDNS 起不来 → setup 回滚、不改网卡
// DNS,fail-open 不 brick(与 Linux/macOS 一致);snistatus 的 last_error 可见原因。

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolverNeedsLocalDNS 报告本平台是否需要 ccfly 起本地 :53 DNS。统一方案下 Windows 需要。
func resolverNeedsLocalDNS() bool { return true }

// dnsBackupPath 是网卡 DNS 备份文件(供 restoreResolver 精确恢复)。
func dnsBackupPath() string {
	root := os.Getenv("ProgramData")
	if root == "" {
		root = `C:\ProgramData`
	}
	return filepath.Join(root, "ccfly", "dns-backup.json")
}

// ifaceDNS 是一块网卡的 IPv4 DNS 现状。
type ifaceDNS struct {
	Alias   string   `json:"alias"`
	DHCP    bool     `json:"dhcp"`
	Servers []string `json:"servers"`
}

// 外部命令做成 var,便于单测打桩(无 Windows 真机)。
var (
	runPS = func(script string) ([]byte, error) {
		return exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	}
	runNetsh = func(args ...string) error {
		return exec.Command("netsh", args...).Run()
	}
	flushDNS = func() { _ = exec.Command("ipconfig", "/flushdns").Run() }
)

// listUpIfaces 返回当前 Up 且配有 IPv4 DNS 的网卡(含 DHCP 与否)。
func listUpIfaces() ([]ifaceDNS, error) {
	out, err := runPS(`
$up = @{};
Get-NetAdapter -ErrorAction SilentlyContinue | Where-Object Status -eq 'Up' | ForEach-Object { $up[$_.Name] = $true }
$dhcp = @{};
Get-NetIPInterface -AddressFamily IPv4 -ErrorAction SilentlyContinue | ForEach-Object { $dhcp[$_.InterfaceAlias] = ($_.Dhcp -eq 'Enabled') }
$r = Get-DnsClientServerAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
  Where-Object { $up[$_.InterfaceAlias] -and $_.ServerAddresses.Count -gt 0 } |
  ForEach-Object { @{ alias = $_.InterfaceAlias; dhcp = [bool]$dhcp[$_.InterfaceAlias]; servers = @($_.ServerAddresses) } }
if ($null -eq $r) { '[]' } else { ConvertTo-Json @($r) -Compress }
`)
	if err != nil {
		return nil, fmt.Errorf("query interfaces: %w", err)
	}
	var ifaces []ifaceDNS
	if err := json.Unmarshal(bytesTrimSpace(out), &ifaces); err != nil {
		return nil, fmt.Errorf("parse interfaces: %w", err)
	}
	return ifaces, nil
}

// pointResolver 备份当前各活动网卡 DNS 后,首选设 127.0.0.1、次级设 upstream(fail-open)。
// intercept/pinned 不用(通配拦截由本地 CoreDNS 内部按 OSS 清单过滤,网卡 DNS 全局指过来)。
func pointResolver(intercept []string, upstream string, pinned []string) error {
	_, _ = intercept, pinned
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return fmt.Errorf("no secondary upstream given (fail-open requires one)")
	}
	ifaces, err := listUpIfaces()
	if err != nil {
		return err
	}
	if len(ifaces) == 0 {
		return fmt.Errorf("no active interface with DNS found")
	}
	backup := dnsBackupPath()
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
			return err
		}
		b, err := json.MarshalIndent(ifaces, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(backup, b, 0o600); err != nil {
			return err
		}
	}
	for _, it := range ifaces {
		if err := runNetsh("interface", "ip", "set", "dns", "name="+it.Alias, "static", "127.0.0.1", "primary"); err != nil {
			return fmt.Errorf("set primary dns on %q: %w", it.Alias, err)
		}
		if err := runNetsh("interface", "ip", "add", "dns", "name="+it.Alias, upstream, "index=2"); err != nil {
			return fmt.Errorf("add secondary dns on %q: %w", it.Alias, err)
		}
	}
	flushDNS()
	return nil
}

// restoreResolver 按备份恢复各网卡 DNS(DHCP 接口改回自动获取)并删备份。幂等(无备份=no-op)。
func restoreResolver() error {
	backup := dnsBackupPath()
	b, err := os.ReadFile(backup)
	if err != nil {
		return nil // 无备份 = 没改过
	}
	var ifaces []ifaceDNS
	if err := json.Unmarshal(b, &ifaces); err != nil {
		return fmt.Errorf("parse dns backup: %w", err)
	}
	var firstErr error
	for _, it := range ifaces {
		if it.DHCP || len(it.Servers) == 0 {
			err = runNetsh("interface", "ip", "set", "dns", "name="+it.Alias, "dhcp")
		} else {
			err = runNetsh("interface", "ip", "set", "dns", "name="+it.Alias, "static", it.Servers[0], "primary")
			for i, srv := range it.Servers[1:] {
				if err == nil {
					err = runNetsh("interface", "ip", "add", "dns", "name="+it.Alias, srv, fmt.Sprintf("index=%d", i+2))
				}
			}
		}
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("restore dns on %q: %w", it.Alias, err)
		}
	}
	if firstErr != nil {
		return firstErr // 备份保留,下次再试
	}
	flushDNS()
	return os.Remove(backup)
}

func bytesTrimSpace(b []byte) []byte { return []byte(strings.TrimSpace(string(b))) }
