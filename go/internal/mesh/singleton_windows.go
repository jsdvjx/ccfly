//go:build windows

package mesh

import (
	"errors"

	"golang.org/x/sys/windows"
)

func tryFlockExclusive(fd uintptr) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(fd),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
}

var singletonMutexHandle windows.Handle // 持有至进程退出,内核自动释放

// tryGlobalSingleton — Windows 增强:命名互斥量跨**所有用户 profile** 互斥。
// 文件锁在 %USERPROFILE%\.ccfly 下,SSH 用户与桌面用户各有一把、互不排斥 —— 2026-07-02 实锤:
// 两个 profile 各起一个 connect,同一设备身份互顶 /mesh 双双拖死。Global\ 命名空间对整机唯一。
func tryGlobalSingleton() error {
	name, err := windows.UTF16PtrFromString(`Global\ccfly-connect-singleton`)
	if err != nil {
		return nil // 名字构造失败:退回文件锁,不阻断
	}
	h, err := windows.CreateMutex(nil, true, name)
	if err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			if h != 0 {
				windows.CloseHandle(h)
			}
			return errors.New("另一个 ccfly connect 实例已在本机运行(可能属于其它用户会话)")
		}
		return nil // 其它错误(权限受限环境等):退回文件锁,不阻断
	}
	singletonMutexHandle = h
	return nil
}
