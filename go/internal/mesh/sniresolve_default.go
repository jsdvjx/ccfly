//go:build !darwin

package mesh

// sniresolve_default.go — 非 macOS 平台的原生解析直接用 net.DefaultResolver:
//   - Windows:Go 恒走系统 API(GetAddrInfoW 系,非纯 Go resolver),hosts 优先级与
//     fake-ip DNS 都按真实应用同一路径生效。
//   - Linux:纯 Go resolver 读 /etc/resolv.conf,与 glibc 应用读同一个文件;系统解析
//     被重写(resolv.conf 被 VPN/代理覆盖)在这里等价可见。

import (
	"context"
	"net"
	"time"
)

func resolveHostNative(host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupHost(ctx, host)
}
