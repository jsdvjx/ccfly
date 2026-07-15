//go:build !darwin

package mesh

// snihelper_other.go — 非 macOS 平台的 SNI helper 桩。Linux/Windows 单进程即可绑 :443(Linux+root/
// Windows 无 <1024 限制),不需要 root helper 双进程;故 sniUsesHelper=false、setupViaHelper 永不被调。
// RunSNIHelper 仅 macOS 有意义,其余平台报错。

import "errors"

// sniUsesHelper 报告本平台 SNI arm 是否走 root helper 双进程路径。非 darwin=否(走 sni.go 内联直绑)。
func sniUsesHelper() bool { return false }

func sniHelperFrontListenerCount() int { return 0 }

// setupViaHelper 在非 darwin 平台永不被调(sniUsesHelper=false 时 setupLocked 走内联路径);留桩保证编译。
func (m *sniManager) setupViaHelper(cfg *SNIConfig) error {
	return errors.New("sni helper: darwin only")
}

// RunSNIHelper 仅 macOS 支持(其余平台单进程直绑 :443)。
func RunSNIHelper() error { return errors.New("ccfly sni-helper: only supported on macOS") }
