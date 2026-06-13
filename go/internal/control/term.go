package control

// term.go — ccfly 自带的「网页终端」WebSocket 端点(/term),与 ttyd 1.7.x 帧协议兼容。
//
// 目标:让 @ccfly/react 的 LiveTerm 直接连 ccfly 自己,不再依赖外部 ttyd。
//
// 它在一个 PTY 里跑 `tmux new-session -A -s <session>`(attach-or-create):同名会话已在跑则
// attach = 与本地/其它端实时镜像(输入互通、不新开实例);否则新建。可选 cwd / cmd 透传给 tmux。
//
// 帧协议(与 ttyd 完全对齐,故前端 ttyd.ts 几乎不改):
//   - 握手:客户端首帧是「无命令字节前缀」的 JSON 文本 {AuthToken, columns, rows}
//     (ttyd 见首字符 '{' 即认作鉴权帧)→ 解析 columns/rows 作初始 PTY 尺寸;AuthToken 忽略
//     (ccfly 默认绑回环、鉴权交反代)。
//   - server→client:'0' + PTY 输出字节(OUTPUT)。
//   - client→server:'0'+bytes = INPUT(写入 PTY);'1'+JSON{columns,rows} = RESIZE(pty.Setsize)。
//     其余命令字节('2' pause 等)忽略。
//   命令字节是 ASCII '0'/'1'(0x30/0x31),与现有 ttyd.ts 一致。
//
// 生命周期:WS 关闭/出错 或 PTY 读到 EOF(tmux attach 进程退出)→ 关连接、关 PTY、kill attach
// 进程,绝不留僵尸 goroutine。用 r.Context().Done() 收尾。
//
// 安全:同本服务其它端点——自身不鉴权,默认绑回环;远端暴露交反代。AuthToken 不校验。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// ttyd 命令字节(ASCII)。
const (
	cmdOutput = '0' // server→client:OUTPUT;client→server:INPUT
	cmdResize = '1' // client→server:RESIZE_TERMINAL
	cmdPing   = '9' // ccfly 扩展:client→server PING,server 原样回 PONG(端到端心跳,拒止半开假活)
)

// termHandshake — 客户端首帧(无前缀 JSON)。AuthToken 忽略。
type termHandshake struct {
	AuthToken string `json:"AuthToken"`
	Columns   uint16 `json:"columns"`
	Rows      uint16 `json:"rows"`
}

// termResize — client→server 的 '1'+JSON resize 帧。
type termResize struct {
	Columns uint16 `json:"columns"`
	Rows    uint16 `json:"rows"`
}

// handleTerm — GET /term?session=<tmux 会话名>[&cwd=][&cmd=]
// 升级为 WebSocket(子协议接受 'tty' 也接受无子协议),在 PTY 里跑 tmux new-session -A,
// 双向桥接 ttyd 帧 ↔ PTY。
func handleTerm(w http.ResponseWriter, r *http.Request) {
	sess := strings.TrimSpace(r.URL.Query().Get("session"))
	if sess == "" {
		ctrlErr(w, 400, "session required")
		return
	}
	// 扛 /clear:前端 /clear 后按「新 sid」算出 cc-<Y[:8]> 连来,解析到真正在跑的 cc-<X[:8]>,
	// 于是 new-session -A 接上真会话(镜像现场),而非新开一个空的孤儿会话。须在起 tmux 前解析。
	sess = resolveSessionParam(sess)
	cwd := r.URL.Query().Get("cwd")
	cmd := r.URL.Query().Get("cmd")

	// 自动起 claude:前端不传 cmd —— 若解析出的 tmux 尚未在跑,new-session -A 会新建它,此时让它
	// 直接 `claude --resume <sid>`(在会话原始 cwd),而非裸 shell。已在跑 → -A 忽略此 cmd 只 attach
	// (= 已在跑 claude 就只接上,/clear 镜像现场原样保留)。任何不确定 → 不注入,保持今日裸壳行为。
	if cmd == "" {
		if snaps, e := scanClaudeSessions(); e == nil { // 已缓存,廉价
			if c, cw, ok := claudeResumeCmd(sess, snaps); ok {
				cmd = c
				if cwd == "" {
					cwd = cw
				}
			}
		}
	}

	// 升级:接受 'tty' 子协议(ttyd 用),也接受无子协议(前端可不带)。
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{"tty"},
		OriginPatterns:  []string{"*"}, // 鉴权交反代;此服务默认绑回环。
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return // Accept 失败已写过响应
	}
	// 兜底关连接(正常路径下面会用更明确的状态码再关一次;重复关无害)。
	defer c.CloseNow()

	// 用一个可取消的 context 统一收尾:WS 关、PTY EOF、请求取消任一发生即触发全员退出。
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	if err := serveTerm(ctx, cancel, c, sess, cwd, cmd); err != nil {
		// 正常关闭(EOF / 客户端走人)不当错误;其余给个 going-away。
		_ = c.Close(websocket.StatusGoingAway, "term closed")
		return
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
}

// serveTerm — 实际的桥接逻辑。返回 nil 表示干净结束。
func serveTerm(ctx context.Context, cancel context.CancelFunc, c *websocket.Conn, sess, cwd, cmd string) error {
	// 1) 读握手首帧(无前缀 JSON {AuthToken,columns,rows})。给个上限超时,避免挂死。
	hsCtx, hsCancel := context.WithTimeout(ctx, 30*time.Second)
	typ, data, err := c.Read(hsCtx)
	hsCancel()
	if err != nil {
		return err
	}
	cols, rows := uint16(80), uint16(24)
	// 首帧应是文本 JSON;宽容处理:能解析出尺寸就用,否则用默认。
	if typ == websocket.MessageText || (len(data) > 0 && data[0] == '{') {
		var hs termHandshake
		if json.Unmarshal(data, &hs) == nil {
			if hs.Columns > 0 {
				cols = hs.Columns
			}
			if hs.Rows > 0 {
				rows = hs.Rows
			}
		}
	}

	// tmux 默认 window-size=latest:最近 attach 的客户端尺寸即窗口尺寸 —— 手机/隐藏终端这类小(甚至 1 行)
	// 客户端 attach 会把整个窗口拖小,极端时压成 1 行,claude 便无法渲染输入框/接收 paste+回车(发不出消息)。
	// 改全局 largest:窗口取**最大**客户端尺寸,小客户端不再缩它(本地大终端在,就保持本地的大小)。幂等,失败忽略。
	_ = exec.Command("tmux", "set-option", "-g", "window-size", "largest").Run()

	// 2) 起 tmux(在 PTY 里),attach-or-create 同名会话 → 与本地/其它端实时镜像。
	// -u:强制把客户端当 UTF-8(否则最小环境下 tmux 客户端 utf8=0,会把中文/符号降级成 '_')。
	args := []string{"-u", "new-session", "-A", "-s", sess}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if cmd != "" {
		args = append(args, cmd)
	}
	tmux := exec.CommandContext(ctx, "tmux", args...)
	// 让 web 客户端的 tmux 输出落进 xterm 本地 scrollback(原生滚动),不抢替代屏幕。
	tmux.Env = append(envWithout("TERM"), "TERM=screen-256color")

	ptmx, err := pty.StartWithSize(tmux, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	// 收尾:关 PTY + 杀 tmux attach 进程(client/attach 进程结束即可,不动里世界会话)。
	defer func() {
		_ = ptmx.Close()
		if tmux.Process != nil {
			_ = tmux.Process.Kill()
		}
		_ = tmux.Wait()
	}()

	var wg sync.WaitGroup

	// WS 写互斥:输出泵(3a)与 PONG 回写(3b)分属两个 goroutine,串行化写帧。
	var wmu sync.Mutex
	wsWrite := func(frame []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return c.Write(ctx, websocket.MessageBinary, frame)
	}

	// 3a) PTY → WS:边读边发 '0'+bytes(OUTPUT)。读到 EOF/错误即取消全员。
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				frame := make([]byte, 1+n)
				frame[0] = cmdOutput
				copy(frame[1:], buf[:n])
				if werr := wsWrite(frame); werr != nil {
					return
				}
			}
			if rerr != nil {
				return // EOF(tmux 退出)或读错 → 结束
			}
		}
	}()

	// 3b) WS → PTY:'0'+bytes 写入 PTY(INPUT);'1'+JSON resize;其余忽略。
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			_, msg, rerr := c.Read(ctx)
			if rerr != nil {
				return // 客户端关闭 / ctx 取消
			}
			if len(msg) == 0 {
				continue
			}
			switch msg[0] {
			case cmdOutput: // INPUT('0')
				if len(msg) > 1 {
					if _, werr := ptmx.Write(msg[1:]); werr != nil {
						return
					}
				}
			case cmdResize: // RESIZE('1')+JSON{columns,rows}
				var rz termResize
				if json.Unmarshal(msg[1:], &rz) == nil && rz.Columns > 0 && rz.Rows > 0 {
					_ = pty.Setsize(ptmx, &pty.Winsize{Cols: rz.Columns, Rows: rz.Rows})
				}
			case cmdPing: // PING('9')→ 原样回 PONG:浏览器据此判端到端链路活性
				if werr := wsWrite(msg); werr != nil {
					return
				}
			default:
				// '2'(pause)等:忽略。
			}
		}
	}()

	<-ctx.Done()
	// 触发两个 goroutine 退出:关 PTY 让阻塞的 Read 返回;CloseNow 让 c.Read 返回。
	_ = ptmx.Close()
	c.CloseNow()
	wg.Wait()

	// ctx.Err() 为 Canceled(我们主动收尾)视作正常;DeadlineExceeded 等向上抛。
	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// envWithout — 返回去掉指定 key 的当前进程环境(用于覆盖 TERM)。
func envWithout(key string) []string {
	prefix := key + "="
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return out
}
