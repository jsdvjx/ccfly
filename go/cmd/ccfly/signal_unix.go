//go:build !windows

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

var signalTerm = syscall.SIGTERM

// signalContext — unix:行为不变,Interrupt/SIGTERM 取消根 ctx。
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, signalTerm)
}
