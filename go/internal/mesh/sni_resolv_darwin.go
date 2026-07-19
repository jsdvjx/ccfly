//go:build darwin

package mesh

// sni_resolv_darwin.go — macOS 用 /etc/resolver/<域> 把 intercept 域的 DNS **scoped** 路由到本地
// (127.0.0.1:53),不动全局 resolver、天然泛解析整个域及子域。每个 apex 域一个文件,内容含
// `nameserver 127.0.0.1` + ccfly 标记行(便于卸载时精确清理,含重启后的孤儿)。需 root(写 /etc/resolver)。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
func pointResolver(intercept []string, upstream string, pinned []string) error {
	_, _ = upstream, pinned
	intercept = filterAllowedHosts(intercept)
	if len(intercept) == 0 {
		return fmt.Errorf("no valid scoped resolver domains")
	}
	if err := os.MkdirAll(resolverDir, 0o755); err != nil {
		return err
	}
	content := resolverMarker + "\nnameserver 127.0.0.1\n"
	if sniCoreDNSPort != defaultCoreDNSPort {
		content += "port " + strconv.Itoa(sniCoreDNSPort) + "\n"
	}
	// Never overwrite an existing user/VPN resolver. Refusing the arm is safer
	// than trying to reconstruct contents we did not create after a crash.
	for _, d := range intercept {
		path := filepath.Join(resolverDir, d)
		info, err := os.Lstat(path)
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("refusing to follow resolver symlink %s", path)
		case err == nil:
			b, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if isManagedResolver(b) {
				continue
			}
			return fmt.Errorf("refusing to overwrite existing resolver %s", path)
		case err != nil && !os.IsNotExist(err):
			return err
		}
	}
	for _, d := range intercept {
		if err := os.WriteFile(filepath.Join(resolverDir, d), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// restoreResolver 清掉 macOS 的 SNI 系统解析改动,给 helper 启动与 `ccfly uninstall` 兜底:
//
//	① 所有 ccfly 标记的 /etc/resolver 文件(现役 CoreDNS scoped 路径,标记法能清重启孤儿);
//	② /etc/hosts 的旧版 ccfly 托管块(升级兼容清理)。
//
// 两者都需 root——uninstall 是 `sudo ccfly uninstall`,本函数即以 root 执行。幂等。
func restoreResolver() error {
	var errs []error
	if err := restoreUnixHosts(unixHostsPath); err != nil { // ② 升级兼容:剥旧版 /etc/hosts 托管块
		errs = append(errs, fmt.Errorf("restore legacy hosts block: %w", err))
	}
	entries, err := os.ReadDir(resolverDir)
	if err != nil {
		if !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("read resolver directory: %w", err))
		}
		return errors.Join(errs...)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(resolverDir, e.Name())
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			errs = append(errs, fmt.Errorf("read resolver %s: %w", p, rerr))
			continue
		}
		if isManagedResolver(b) {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("remove resolver %s: %w", p, err))
			}
		}
	}
	return errors.Join(errs...)
}

func isManagedResolver(content []byte) bool {
	firstLine, _, _ := strings.Cut(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	return firstLine == resolverMarker
}

// managedResolverDomains 返回 /etc/resolver 下现役 ccfly 托管文件名(=实际生效的拦截 apex 清单)。
// darwin 上权威配置在 root helper 自持的 dnsPolicyService,agent 侧以此作真实清单观测。读取失败=空。
func managedResolverDomains() []string {
	entries, err := os.ReadDir(resolverDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(resolverDir, e.Name()))
		if err != nil || !isManagedResolver(b) {
			continue
		}
		out = append(out, e.Name())
	}
	return out
}
