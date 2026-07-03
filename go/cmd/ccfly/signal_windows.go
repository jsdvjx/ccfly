//go:build windows

package main

import (
	"context"
	"os"
	"os/signal"

	"golang.org/x/sys/windows"
)

var signalTerm = os.Interrupt

// signalContext — Windows:交互终端里跑(stdin 是控制台)→ Ctrl-C 正常取消;
// 服务态(计划任务/无控制台 stdin)→ **忽略 console interrupt**。否则 /term 的 ConPTY 关闭时
// psmux 侧扩散出的 CTRL 事件会被 Go 当成 Ctrl-C,把 connect 服务整个「优雅退出」
// (实测:app 关一次终端,设备就离线)。服务态的停止手段是 taskkill(TerminateProcess),不受影响。
func signalContext() (context.Context, context.CancelFunc) {
	if interactiveStdin() {
		return signal.NotifyContext(context.Background(), os.Interrupt, signalTerm)
	}
	signal.Ignore(os.Interrupt)
	return context.WithCancel(context.Background())
}

func interactiveStdin() bool {
	h, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return false
	}
	var mode uint32
	return windows.GetConsoleMode(h, &mode) == nil
}
