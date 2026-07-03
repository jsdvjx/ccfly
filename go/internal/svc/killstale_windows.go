//go:build windows

package svc

import (
	"fmt"
	"os"
	"os/exec"
)

// killStaleProcesses 杀掉本机所有**其它** ccfly.exe 进程(排除安装进程自己)。
// install 前必须清场:① 旧实例与新服务用同一设备身份互相顶 /mesh 连接(30s 规律断连);
// ② Windows 不允许覆盖运行中的 exe,不杀会 copyExe EPERM。best-effort,失败只提示。
func killStaleProcesses() {
	self := os.Getpid()
	cmd := exec.Command("taskkill.exe", "/F",
		"/FI", "IMAGENAME eq ccfly.exe",
		"/FI", fmt.Sprintf("PID ne %d", self))
	out, err := cmd.CombinedOutput()
	if err == nil {
		fmt.Printf("✓ 已终止旧的 ccfly 进程(避免多实例互顶 mesh 连接)\n")
	}
	_ = out // taskkill 无匹配进程时报错退出,属正常(本来就没有旧进程)
}
