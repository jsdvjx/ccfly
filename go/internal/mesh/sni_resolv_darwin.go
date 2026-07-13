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

// restoreResolver 删除所有 ccfly 标记的 /etc/resolver 文件(标记比记路径更稳,能清理重启遗留)。幂等。
func restoreResolver() error {
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
