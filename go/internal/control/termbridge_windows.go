//go:build windows

package control

// termbridge_windows.go — `ccfly _termpty` 子命令:在**独立子进程**里跑 ConPTY 桥。
//
// 为什么要独立进程:ConPTY(伪终端)关闭时,Windows 会向挂在相关控制台上的进程投递
// CTRL_CLOSE 类事件 —— 该事件即使被忽略,处理完后进程也会被系统终止(实测:/term WS 一关,
// 承载 ConPTY 的 ccfly connect 服务整个「干净退出 exit 0」,设备离线)。把 ConPTY 挪进
// 一次性子进程后,最坏也只是桥进程陪葬,服务进程与 mesh 隧道不受影响。
//
// 协议(stdin,父→子,帧式):[1B type][4B BE len][payload]
//   type 0 = INPUT(payload 原样写入伪终端)
//   type 1 = RESIZE(payload 8B:4B BE cols + 4B BE rows)
// stdout(子→父):伪终端原始输出字节流。子进程随 ConPTY EOF / stdin EOF 退出。

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/UserExistsError/conpty"
)

// RunTermBridge 是 `ccfly _termpty <cols> <rows> -- <cmd> [args...]` 的实现。
func RunTermBridge(args []string) error {
	if len(args) < 4 || args[2] != "--" {
		return fmt.Errorf("usage: _termpty <cols> <rows> -- <cmd> [args...]")
	}
	cols, _ := strconv.Atoi(args[0])
	rows, _ := strconv.Atoi(args[1])
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	cmdline := winEscapeArgs(args[3:])
	cp, err := conpty.Start(cmdline, conpty.ConPtyDimensions(cols, rows), conpty.ConPtyEnv(os.Environ()))
	if err != nil {
		return err
	}
	defer cp.Close()

	// stdin 帧解码 → 伪终端(INPUT/RESIZE)
	go func() {
		hdr := make([]byte, 5)
		for {
			if _, e := io.ReadFull(os.Stdin, hdr); e != nil {
				cp.Close() // 父进程关了 stdin:结束桥
				return
			}
			n := binary.BigEndian.Uint32(hdr[1:5])
			payload := make([]byte, n)
			if _, e := io.ReadFull(os.Stdin, payload); e != nil {
				cp.Close()
				return
			}
			switch hdr[0] {
			case 0:
				if _, e := cp.Write(payload); e != nil {
					return
				}
			case 1:
				if len(payload) == 8 {
					c := int(binary.BigEndian.Uint32(payload[0:4]))
					r := int(binary.BigEndian.Uint32(payload[4:8]))
					_ = cp.Resize(c, r)
				}
			}
		}
	}()

	// 伪终端输出 → stdout(原样)
	_, _ = io.Copy(os.Stdout, cp)
	return nil
}
