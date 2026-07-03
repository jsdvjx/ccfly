//go:build !windows

package svc

import (
	"fmt"
	"os/exec"
)

// killStaleProcesses 杀掉本机所有游离的 `ccfly connect` 进程(不含服务管理器托管的:
// darwin/linux 的 launchd/systemd 会在 install 流程里自行 bootout/stop 再拉起)。
// 多实例用同一设备身份互相顶 /mesh 连接会导致 30s 规律断连,install 前清场。
// pkill -f 'ccfly connect' 不会误杀安装进程自己(其命令行是 `ccfly install`)。
func killStaleProcesses() {
	if err := exec.Command("pkill", "-f", "ccfly connect").Run(); err == nil {
		fmt.Printf("✓ 已终止旧的 ccfly connect 进程(避免多实例互顶 mesh 连接)\n")
	}
}
