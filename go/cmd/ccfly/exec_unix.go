//go:build !windows

package main

import "syscall"

func execProcess(path string, argv []string, envv []string) error {
	return syscall.Exec(path, argv, envv)
}
