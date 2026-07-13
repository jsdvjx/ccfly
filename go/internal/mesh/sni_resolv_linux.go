//go:build linux

package mesh

// sni_resolv_linux.go — Linux 把系统解析指向本地 SNI DNS(127.0.0.1:53),并保留真上游做 fail-open。
// 备份原 /etc/resolv.conf,卸载时恢复。仅 Linux;其他平台见 sni_resolv_other.go(no-op)。

import (
	"os"
)

const (
	resolvPath   = "/etc/resolv.conf"
	resolvBackup = "/etc/resolv.conf.ccfly-sni-bak"
)

// pointResolvConf 把 resolv.conf 改成「127.0.0.1 优先 + 真上游次级(fail-open,本地 DNS 挂了不 brick)」。
// 首次调用备份原文件;options 收紧超时/尝试,减少本地 :53 未就绪时的等待。
func pointResolvConf(upstream string) error {
	if _, err := os.Stat(resolvBackup); os.IsNotExist(err) {
		orig, rerr := os.ReadFile(resolvPath)
		if rerr != nil {
			orig = []byte{} // 原文件缺失也继续(容器常见)
		}
		if werr := os.WriteFile(resolvBackup, orig, 0o644); werr != nil {
			return werr
		}
	}
	content := "# managed by ccfly SNI (原文件备份在 " + resolvBackup + ")\n" +
		"nameserver 127.0.0.1\n" +
		"nameserver " + upstream + "\n" +
		"options timeout:2 attempts:2\n"
	return os.WriteFile(resolvPath, []byte(content), 0o644)
}

// restoreResolvConf 从备份恢复 resolv.conf 并删备份。幂等(无备份=已恢复)。
func restoreResolvConf() error {
	orig, err := os.ReadFile(resolvBackup)
	if err != nil {
		return nil // 无备份 = 没改过或已恢复
	}
	if err := os.WriteFile(resolvPath, orig, 0o644); err != nil {
		return err
	}
	return os.Remove(resolvBackup)
}
