//go:build !windows

package control

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// termProc 是 /term 的平台 PTY 抽象:unix=creack/pty(fork+pty),windows=ConPTY(见 termpty_windows.go)。
// 两端形状一致:Read/Write 桥接字节流,Resize 调整窗口,Kill 结束 attach 进程(不动里世界 tmux 会话)。
type termProc struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func startTermProc(name string, args []string, env []string, cols, rows uint16) (*termProc, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	return &termProc{ptmx: f, cmd: cmd}, nil
}

func (t *termProc) Read(p []byte) (int, error)  { return t.ptmx.Read(p) }
func (t *termProc) Write(p []byte) (int, error) { return t.ptmx.Write(p) }
func (t *termProc) ClosePTY()                   { _ = t.ptmx.Close() }

func (t *termProc) Resize(cols, rows uint16) {
	_ = pty.Setsize(t.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// Kill 结束 attach 进程并回收(client 侧断开即可,里世界会话不受影响)。
func (t *termProc) Kill() {
	_ = t.ptmx.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	_ = t.cmd.Wait()
}
