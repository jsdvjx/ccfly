//go:build !darwin

package svc

// svc_snihelper_other.go — 非 macOS 的 SNI helper 安装桩。只有 macOS 需要 root helper 双进程承接 :443
// (见 svc_snihelper_darwin.go);其余平台 InstallSNIHelper 经 GOOS 判后不会走到实版,此处仅保证编译。

func installSNIHelperDarwin(dryRun bool) (bool, error) { return false, nil }

func uninstallSNIHelperDarwin(dryRun bool) error { return nil }
