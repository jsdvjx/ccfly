//go:build windows

package control

// termpty_windows.go — /term 的 Windows PTY 抽象:桥接进程模式。
// ConPTY 不在服务进程里创建(会招来 CTRL_CLOSE 连坐,见 termbridge_windows.go),
// 而是 spawn `ccfly _termpty` 子进程(DETACHED_PROCESS,无控制台继承),经 stdio 帧协议桥接。

import (
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

type termProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   io.ReadCloser
	wmu   sync.Mutex
}

func startTermProc(name string, args []string, env []string, cols, rows uint16) (*termProc, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cliArgs := append([]string{"_termpty", strconv.Itoa(int(cols)), strconv.Itoa(int(rows)), "--", name}, args...)
	c := exec.Command(self, cliArgs...)
	c.Env = env
	const detachedProcess = 0x00000008
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP}
	stdin, err := c.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := c.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c.Stderr = nil
	if err := c.Start(); err != nil {
		return nil, err
	}
	return &termProc{cmd: c, stdin: stdin, out: out}, nil
}

func (t *termProc) Read(p []byte) (int, error) { return t.out.Read(p) }

func (t *termProc) writeFrame(typ byte, payload []byte) (int, error) {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	hdr := make([]byte, 5)
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:5], uint32(len(payload)))
	if _, err := t.stdin.Write(hdr); err != nil {
		return 0, err
	}
	return t.stdin.Write(payload)
}

func (t *termProc) Write(p []byte) (int, error) { return t.writeFrame(0, p) }

func (t *termProc) Resize(cols, rows uint16) {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint32(buf[0:4], uint32(cols))
	binary.BigEndian.PutUint32(buf[4:8], uint32(rows))
	_, _ = t.writeFrame(1, buf)
}

func (t *termProc) ClosePTY() {
	_ = t.stdin.Close() // 桥进程 stdin EOF → 自行收尾退出
	_ = t.out.Close()
}

func (t *termProc) Kill() {
	_ = t.stdin.Close()
	_ = t.out.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	_ = t.cmd.Wait()
}

// winEscapeArgs 按 Windows 规则把 argv 拼成单条命令行(conpty.Start 需要)。
func winEscapeArgs(argv []string) string {
	s := ""
	for i, a := range argv {
		if i > 0 {
			s += " "
		}
		s += windows.EscapeArg(a)
	}
	return s
}
