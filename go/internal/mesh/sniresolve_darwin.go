//go:build darwin

package mesh

// sniresolve_darwin.go — macOS 的原生解析路径是 libinfo getaddrinfo,只有它认
// /etc/resolver/<域> 的 scoped 配置;本二进制 CGO_ENABLED=0,Go 纯 resolver 只读
// /etc/resolv.conf,看不到 scoped 项,无法代表真实应用(Node/浏览器等)的解析结果。
// 故借系统自带的 dscacheutil(同一 libinfo 路径)拿与真实应用一致的地址。

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// dscacheutilLookPath 可变,便于测试替换;生产恒 "dscacheutil"(PATH 查找)。
var dscacheutilLookPath = "dscacheutil"

func resolveHostNative(host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, dscacheutilLookPath, "-q", "host", "-a", "name", host).Output()
	addrs := parseDscacheutilHosts(out)
	if len(addrs) == 0 {
		if err != nil {
			return nil, fmt.Errorf("native resolve %s: %w", host, err)
		}
		return nil, fmt.Errorf("native resolve %s: no addresses", host)
	}
	return addrs, nil
}

// parseDscacheutilHosts 从 `dscacheutil -q host -a name` 输出提取 ip_address/ipv6_address。
func parseDscacheutilHosts(out []byte) []string {
	var addrs []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || (f[0] != "ip_address:" && f[0] != "ipv6_address:") {
			continue
		}
		if !seen[f[1]] {
			seen[f[1]] = true
			addrs = append(addrs, f[1])
		}
	}
	return addrs
}
