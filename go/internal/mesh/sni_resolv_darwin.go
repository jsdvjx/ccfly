//go:build darwin

package mesh

// sni_resolv_darwin.go — macOS 用 /etc/resolver/<域> 把 intercept 域的 DNS **scoped** 路由到本地
// (127.0.0.1:53),不动全局 resolver、天然泛解析整个域及子域。每个 apex 域一个文件,内容含
// `nameserver 127.0.0.1` + ccfly 标记行(便于卸载时精确清理,含重启后的孤儿)。需 root(写 /etc/resolver)。

import (
	"os"
	"path/filepath"
	"strings"
)

const resolverMarker = "# ccfly-sni" // 标记我们创建的文件,restoreResolver 据此精确清理

// resolverDir 是 macOS scoped resolver 目录;var 便于测试覆盖到临时目录(生产恒 /etc/resolver)。
var resolverDir = "/etc/resolver"

// resolverNeedsLocalDNS 报告本平台是否需要本地 :53 DNS。macOS scoped resolver 把 intercept 域的查询
// 导到 127.0.0.1:53,故需要本地 DNS。
func resolverNeedsLocalDNS() bool { return true }

// pointResolver 为每个 intercept 域写 /etc/resolver/<域>,把该域(含子域)的 DNS 路由到本地。
// upstream 在 macOS 不用(scoped:只有 intercept 域进本地 DNS,其余走系统默认,无需本地转发上游)。
func pointResolver(intercept []string, upstream string) error {
	_ = upstream
	if err := os.MkdirAll(resolverDir, 0o755); err != nil {
		return err
	}
	content := resolverMarker + "\nnameserver 127.0.0.1\n"
	for _, d := range intercept {
		if d == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(resolverDir, d), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// restoreResolver 清掉 macOS 的两类 SNI 系统解析改动,给 `ccfly uninstall` 的 CleanupResolver 兜底
// (服务被硬杀不走 teardown;且现役 arm 走 /etc/hosts,legacy arm 曾用 /etc/resolver):
//
//	① /etc/hosts 的 ccfly 托管块(现役双进程 helper 路径写的);
//	② 所有 ccfly 标记的 /etc/resolver 文件(旧 scoped-resolver 路径的遗留,标记法能清重启孤儿)。
//
// 两者都需 root——uninstall 是 `sudo ccfly uninstall`,本函数即以 root 执行。幂等。
func restoreResolver() error {
	_ = restoreUnixHosts(unixHostsPath) // ① 现役:剥 /etc/hosts 托管块(见 snihelper_darwin.go)
	entries, err := os.ReadDir(resolverDir)
	if err != nil {
		return nil // 目录不存在 = 没建过
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(resolverDir, e.Name())
		b, rerr := os.ReadFile(p)
		if rerr == nil && strings.HasPrefix(string(b), resolverMarker) {
			_ = os.Remove(p)
		}
	}
	return nil
}
