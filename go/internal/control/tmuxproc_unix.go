//go:build !windows

package control

import "os/exec"

// setTmuxSysProcAttr — unix:无控制台信号串门问题,无需特殊进程属性。
func setTmuxSysProcAttr(*exec.Cmd) {}
