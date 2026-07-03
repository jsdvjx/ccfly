package mesh

// singleton.go — `ccfly connect` 单例锁。同一设备身份跑多个 connect 实例会互相
// 顶掉对方的 /mesh 连接(云端「新连接替换旧连接」),表现为 mesh 每 ~30s 规律
// down/up、隧道永远不稳、设备代理不通。故 connect 全局唯一:第二个实例直接报错退出。
//
// 实现:~/.ccfly/connect.lock 上的 OS 级文件锁(unix flock / windows LockFileEx),
// 进程存活期间持有,进程退出(含 crash/kill)由内核自动释放 —— 不会像 PID 文件那样留死锁。

import (
	"fmt"
	"os"
	"path/filepath"
)

var singletonLockFile *os.File // 持有引用防 GC 关闭 fd;锁随进程生命周期

// AcquireSingleton takes the global connect lock, or returns an error telling
// the user another instance is already running. Call once at connect startup.
func AcquireSingleton() error {
	// Windows:先过整机级命名互斥(跨用户 profile;文件锁按 profile 隔离挡不住 SSH 用户与
	// 桌面用户各起一个的场景)。unix 为 no-op。
	if err := tryGlobalSingleton(); err != nil {
		return err
	}
	dir, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "connect.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := tryFlockExclusive(f.Fd()); err != nil {
		f.Close()
		return fmt.Errorf("已有另一个 ccfly connect 实例在本机运行(锁 %s 被占用)。\n"+
			"  多实例会互相顶掉 mesh 连接导致反复断连 —— 请先退出/杀掉旧实例再启动。\n"+
			"  Windows: tasklist | findstr ccfly && taskkill /f /im ccfly.exe\n"+
			"  macOS/Linux: pgrep -fl ccfly && pkill -f 'ccfly connect'", path)
	}
	// best-effort 写 PID 便于诊断(锁本身不依赖内容)
	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Sync()
	singletonLockFile = f
	return nil
}
