//go:build !windows

package mesh

import "syscall"

func tryFlockExclusive(fd uintptr) error {
	return syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
}

// tryGlobalSingleton — unix:文件锁已够(单用户部署形态),无需命名互斥。
func tryGlobalSingleton() error { return nil }
