//go:build windows

package control

import "os/exec"

// setTmuxSysProcAttr — Windows:**不设任何 CreationFlags**。实测教训:
//   - DETACHED_PROCESS → psmux server 无控制台,其会话窗口里 claude 秒退(pane 剩裸 shell);
//   - 仅 CREATE_NEW_PROCESS_GROUP → psmux server 起后即死("no server running")。
// 终端关闭的 CTRL_CLOSE 连坐由 _termpty 桥进程隔离(termbridge_windows.go),这里保持裸 spawn。
func setTmuxSysProcAttr(*exec.Cmd) {}
