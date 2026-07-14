//go:build !linux && !darwin && !windows

package mesh

// sni_resolv_other.go — 其余小众平台(freebsd 等)的 resolver 指向占位:本版不改系统解析(no-op)。
// DNS/:443 仍会起,但不自动把系统解析指向本地 → intercept 不生效;失败安全(不 brick)。三大平台
// (linux/darwin/windows)各有实现,见对应 sni_resolv_*.go。

// resolverNeedsLocalDNS 在未支持平台保持与历史一致(仍起本地 :53,虽无 resolver 指向而收不到查询;
// 无害)。
func resolverNeedsLocalDNS() bool { return true }

// pointResolver 在未支持的平台不改系统解析(no-op)。
func pointResolver(intercept []string, upstream string) error { return nil }

// restoreResolver 在未支持的平台 no-op。
func restoreResolver() error { return nil }
