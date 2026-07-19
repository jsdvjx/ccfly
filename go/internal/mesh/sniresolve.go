package mesh

// sniresolve.go — 真实应用路径检测的第一步:平台原生解析 canary,验证系统解析确实被 ccfly
// 接管(结果全部落 loopback),并识别 FakeDNS/TUN 抢解析(198.18.0.0/15 fake-ip、其他 fake-ip
// 或公网地址)。仅探测 127.0.0.1 不足以发现这些劫持 —— 必须看「真实应用会拿到什么地址」。
//
// 各平台的原生解析实现:
//   - darwin:sniresolve_darwin.go,借 dscacheutil 走 libinfo(CGO_ENABLED=0 的 Go resolver
//     只读 /etc/resolv.conf,看不到 /etc/resolver/<域> 的 scoped 配置)。
//   - windows/linux 等:sniresolve_default.go,net.DefaultResolver。Windows 上 Go 恒走系统
//     API(GetAddrInfoW,hosts 优先级生效);Linux 纯 Go resolver 与 glibc 应用读同一个
//     /etc/resolv.conf,等价。

import (
	"net/netip"
	"strings"
)

// fakeIPPrefix 是 Clash/mihomo 等 fake-ip 模式的默认地址段(仅用于诊断文案;判定规则是
// 「任何非 loopback」= 解析被抢,见 classifyResolved)。
var fakeIPPrefix = netip.MustParsePrefix("198.18.0.0/15")

// nativeResolveHost 走平台原生解析路径(可变,便于测试替换)。生产实现 = resolveHostNative。
var nativeResolveHost = resolveHostNative

// classifyResolved 校验系统原生解析结果:全部属于 loopback(127.0.0.0/8 或 ::1)才算被 ccfly
// 接管。fake=true 表示出现 fake-ip/公网等非 loopback 地址(解析被 FakeDNS/TUN 抢走)。
func classifyResolved(addrs []string) (ok bool, fake bool) {
	if len(addrs) == 0 {
		return false, false
	}
	for _, raw := range addrs {
		ip, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil || !ip.IsLoopback() {
			return false, true
		}
	}
	return true, false
}

// firstNonLoopback 返回第一个非 loopback 地址及其形态描述(诊断文案用);没有则返回空。
func firstNonLoopback(addrs []string) (addr string, kind string) {
	for _, raw := range addrs {
		ip, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil {
			return raw, "非法地址"
		}
		if ip.IsLoopback() {
			continue
		}
		if fakeIPPrefix.Contains(ip) {
			return raw, "fake-ip"
		}
		return raw, "公网地址"
	}
	return "", ""
}
