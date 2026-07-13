//go:build windows

package mesh

// sni_resolv_windows.go — Windows 用 NRPT(Name Resolution Policy Table)把 intercept 域的 DNS
// **scoped** 路由到本地(127.0.0.1),不动全局 DNS。每个 apex 域一条 `.<域>` 命名空间规则(匹配域+子域),
// 带 ccfly 注释便于卸载精确清理。需管理员(Add/Remove-DnsClientNrptRule 走 powershell)。

import (
	"os/exec"
	"strings"
)

const nrptComment = "ccfly-sni" // 规则注释,restoreResolver 据此精确清理

// pointResolver 为每个 intercept 域加一条 NRPT 规则(`.<域>` 匹配该域及子域),DNS 指向 127.0.0.1。
// upstream 在 Windows 不用(scoped:只有 intercept 域进本地 DNS,其余走系统默认)。
func pointResolver(intercept []string, upstream string) error {
	_ = upstream
	var b strings.Builder
	// 先清掉可能残留的旧 ccfly 规则(重复调用幂等),再逐域添加。
	b.WriteString("Get-DnsClientNrptRule | Where-Object {$_.Comment -eq '" + nrptComment + "'} | Remove-DnsClientNrptRule -Force -ErrorAction SilentlyContinue;")
	for _, d := range intercept {
		if d == "" {
			continue
		}
		b.WriteString("Add-DnsClientNrptRule -Namespace '." + d + "' -NameServers '127.0.0.1' -Comment '" + nrptComment + "';")
	}
	return runPowerShell(b.String())
}

// restoreResolver 删除所有 ccfly 注释的 NRPT 规则。幂等。
func restoreResolver() error {
	return runPowerShell("Get-DnsClientNrptRule | Where-Object {$_.Comment -eq '" + nrptComment + "'} | Remove-DnsClientNrptRule -Force -ErrorAction SilentlyContinue")
}

func runPowerShell(script string) error {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	return cmd.Run()
}
