//go:build !windows

package main

import "errors"

// runTermBridge — unix 无需桥进程(creack/pty 直接进程内),此命令不应被调用。
func runTermBridge([]string) error { return errors.New("_termpty is windows-only") }
