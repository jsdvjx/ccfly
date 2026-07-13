//go:build linux

package mesh

// sni_resolv_linux.go — Linux 把系统解析指向本地 SNI DNS(127.0.0.1:53)。Linux 无 macOS `/etc/resolver`
// 或 Windows NRPT 那种按域 scoped 机制,resolv.conf 是全局的,故改全局 nameserver 并把真上游列为次级
// (fail-open:本地 DNS 挂了仍能经次级解析,不 brick);本地 DNS 内部按 intercept 过滤、其余转上游。
// intercept 参数在 Linux 不用(全局拦截 + 本地 DNS 内部过滤);备份原文件,卸载时恢复。

import "os"

const (
	resolvPath   = "/etc/resolv.conf"
	resolvBackup = "/etc/resolv.conf.ccfly-sni-bak"
)

// pointResolver 把 resolv.conf 改成「127.0.0.1 优先 + 真上游次级(fail-open)」。首次调用备份原文件。
func pointResolver(intercept []string, upstream string) error {
	_ = intercept // Linux 全局拦截,不按域 scoped;本地 DNS 内部按 intercept 过滤
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

// restoreResolver 从备份恢复 resolv.conf 并删备份。幂等(无备份=已恢复)。
func restoreResolver() error {
	orig, err := os.ReadFile(resolvBackup)
	if err != nil {
		return nil // 无备份 = 没改过或已恢复
	}
	if err := os.WriteFile(resolvPath, orig, 0o644); err != nil {
		return err
	}
	return os.Remove(resolvBackup)
}
