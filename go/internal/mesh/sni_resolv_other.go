//go:build !linux

package mesh

// sni_resolv_other.go — 非 Linux 平台的 resolv 指向占位:本版不自动改系统解析(macOS 用 /etc/resolver、
// Windows 用 NRPT,各需独立安装器,待后续)。这些桩让 setupLocked 在 GOOS!=linux 分支不引用它们即可编译;
// 保留符号是为跨平台编译一致(setupLocked 只在 runtime.GOOS=="linux" 时调用它们)。

// pointResolvConf 在非 Linux 平台不改系统解析(no-op)。
func pointResolvConf(upstream string) error { return nil }

// restoreResolvConf 在非 Linux 平台 no-op。
func restoreResolvConf() error { return nil }
