package main

// picker.go — `ccfly a`(或无参 `ccfly attach`)的交互式接管选择器(两级 TUI):
//
//   第 1 级 选项目:按 cwd 分组(groupByDir 同口径),最近活动倒序;行内显示
//           会话数 / live 数 / 最近活动时间。
//   第 2 级 选会话:组内时间倒序,●/○ 标 live;显示真 tmux 名(panemap 解析)与标题。
//   Enter 接管:live → tmux attach 镜像现场;离线 → takeover 杀残留进程 + resume 重建
//   (与 `ccfly attach <sid>` 完全同一条路径,防双写语义一致)。
//
// 键位:↑↓/jk 移动 · Enter/→ 进入或接管 · ←/Esc 返回 · r 刷新 · q 退出。
// 实现:纯 ANSI + raw mode(golang.org/x/term),备用屏(1049)进出不污染滚动缓冲,
// 零 TUI 框架依赖。raw mode 下换行必须 \r\n。

import (
	"bufio"
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/ccfly/ccfly/go/internal/control"
)

// runPicker — TUI 主循环。返回 (选中的 sid, 是否选中);q/Esc 退出 → ("", nil)。
// 选中后由调用方在**恢复终端之后**走 attach(exec tmux 接管 TTY)。
func runPicker() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("需要交互终端;脚本里请用 `ccfly attach <sid>`")
	}
	rows, err := control.CLISessions()
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("没有会话;`ccfly new [dir]` 起一个")
	}
	groups := groupByDir(rows)

	old, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	out := bufio.NewWriter(os.Stdout)
	enter := func() { fmt.Fprint(out, "\x1b[?1049h\x1b[?25l"); out.Flush() } // 备用屏 + 藏光标
	leave := func() {
		fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")
		out.Flush()
		_ = term.Restore(fd, old)
	}
	enter()
	defer leave()

	level, gi, si := 0, 0, 0
	in := bufio.NewReader(os.Stdin)
	for {
		w, h, e := term.GetSize(fd)
		if e != nil || w <= 0 || h <= 0 {
			w, h = 80, 24
		}
		if level == 0 {
			drawProjects(out, groups, gi, w, h)
		} else {
			drawSessions(out, groups[gi], si, w, h)
		}

		key, e := readKey(in)
		if e != nil {
			return "", nil
		}
		cur, max := &gi, len(groups)
		if level == 1 {
			cur, max = &si, len(groups[gi].Rows)
		}
		switch key {
		case keyUp:
			if *cur > 0 {
				*cur--
			}
		case keyDown:
			if *cur < max-1 {
				*cur++
			}
		case keyEnter, keyRight:
			if level == 0 {
				level, si = 1, 0
			} else {
				return groups[gi].Rows[si].Sid, nil
			}
		case keyLeft, keyEsc:
			if level == 1 {
				level = 0
			} else {
				return "", nil // 顶层 Esc/← = 退出
			}
		case keyRefresh:
			if rs, e := control.CLISessions(); e == nil && len(rs) > 0 {
				groups = groupByDir(rs)
				if gi >= len(groups) {
					gi = len(groups) - 1
				}
				if level == 1 && si >= len(groups[gi].Rows) {
					si = len(groups[gi].Rows) - 1
				}
			}
		case keyQuit:
			return "", nil
		}
	}
}

// ── 键盘 ────────────────────────────────────────────────────────────────────

type key int

const (
	keyNone key = iota
	keyUp
	keyDown
	keyLeft
	keyRight
	keyEnter
	keyEsc
	keyQuit
	keyRefresh
)

// readKey — 读一个按键(解 CSI 方向键;Esc 单击与转义序列区分:后续紧跟 '[' 才是序列)。
func readKey(in *bufio.Reader) (key, error) {
	b, err := in.ReadByte()
	if err != nil {
		return keyNone, err
	}
	switch b {
	case 'q', 3: // q / Ctrl-C
		return keyQuit, nil
	case 'r':
		return keyRefresh, nil
	case 'k':
		return keyUp, nil
	case 'j':
		return keyDown, nil
	case 'h':
		return keyLeft, nil
	case 'l':
		return keyRight, nil
	case '\r', '\n':
		return keyEnter, nil
	case 0x1b: // Esc 或 CSI 序列
		if in.Buffered() == 0 {
			return keyEsc, nil
		}
		n, _ := in.ReadByte()
		if n != '[' {
			return keyEsc, nil
		}
		f, _ := in.ReadByte()
		switch f {
		case 'A':
			return keyUp, nil
		case 'B':
			return keyDown, nil
		case 'C':
			return keyRight, nil
		case 'D':
			return keyLeft, nil
		}
		return keyNone, nil
	}
	return keyNone, nil
}

// ── 渲染 ────────────────────────────────────────────────────────────────────

// viewport — 让光标行始终可见:返回 [off, off+n) 窗口起点。
func viewport(cursor, total, n int) int {
	if total <= n {
		return 0
	}
	off := cursor - n/2
	if off < 0 {
		off = 0
	}
	if off > total-n {
		off = total - n
	}
	return off
}

func drawProjects(out *bufio.Writer, groups []dirGroup, cursor, w, h int) {
	fmt.Fprint(out, "\x1b[2J\x1b[H")
	title := "ccfly a — 选择项目"
	help := "↑↓ 选择 · Enter 进入 · r 刷新 · q 退出"
	fmt.Fprintf(out, "\x1b[1m%s\x1b[0m\x1b[90m%s\x1b[0m\r\n\r\n", padBetween(title, help, w), "")
	n := h - 4
	off := viewport(cursor, len(groups), n)
	for i := off; i < len(groups) && i < off+n; i++ {
		g := groups[i]
		nLive := 0
		for _, r := range g.Rows {
			if r.Live {
				nLive++
			}
		}
		cwd := collapseHome(g.Cwd)
		if cwd == "" {
			cwd = "(未知目录)"
		}
		meta := fmt.Sprintf("%d 会话", len(g.Rows))
		if nLive > 0 {
			meta = fmt.Sprintf("\x1b[32m%d live\x1b[0m · %s", nLive, meta)
		}
		line := fmt.Sprintf("%s  \x1b[90m%s · %s\x1b[0m", cwd, meta, fmtAge(g.Rows[0].Age))
		drawRow(out, line, i == cursor, w)
	}
	out.Flush()
}

func drawSessions(out *bufio.Writer, g dirGroup, cursor, w, h int) {
	fmt.Fprint(out, "\x1b[2J\x1b[H")
	cwd := collapseHome(g.Cwd)
	help := "Enter 接管 · ←/Esc 返回 · q 退出"
	fmt.Fprintf(out, "\x1b[1m%s\x1b[0m\r\n\r\n", padBetween(cwd+" — 选择会话", help, w))
	n := h - 4
	off := viewport(cursor, len(g.Rows), n)
	for i := off; i < len(g.Rows) && i < off+n; i++ {
		r := g.Rows[i]
		dot, where := "\x1b[90m○\x1b[0m", "\x1b[90m离线 · 接管将 resume\x1b[0m"
		if r.Live {
			dot = "\x1b[32m●\x1b[0m"
			where = "\x1b[36m" + r.Tmux + "\x1b[0m"
			if r.Tmux == "" {
				where = r.State
			} else if r.State != "" {
				where += " \x1b[90m" + r.State + "\x1b[0m"
			}
		}
		title := r.Title
		if title == "" {
			title = "(无标题)"
		}
		line := fmt.Sprintf("%s %s  %-4s %s  \x1b[90m%s\x1b[0m", dot, r.Sid[:8], fmtAge(r.Age), where, title)
		drawRow(out, line, i == cursor, w)
	}
	out.Flush()
}

// drawRow — 一行列表项:选中行加 "❯ " 前缀 + 反白;按显示宽截断(CJK 记 2 格)。
func drawRow(out *bufio.Writer, line string, selected bool, w int) {
	prefix := "  "
	if selected {
		prefix = "\x1b[1m❯ \x1b[0m"
	}
	fmt.Fprintf(out, "%s%s\x1b[0m\r\n", prefix, truncWidth(line, w-3))
}

// padBetween — 左右两段文本拉满一行(右侧灰):宽度不够时只保留左段。
func padBetween(left, right string, w int) string {
	lw, rw := dispWidth(left), dispWidth(right)
	gap := w - lw - rw - 1
	if gap < 1 {
		return left
	}
	return left + fmt.Sprintf("%*s", gap, "") + "\x1b[90m" + right + "\x1b[0m"
}

// dispWidth — 粗略显示宽度:跳过 ANSI 转义;≥U+1100 的字符按 2 格记(CJK 足够准)。
func dispWidth(s string) int {
	w, esc := 0, false
	for _, r := range s {
		if esc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				esc = false
			}
			continue
		}
		if r == 0x1b {
			esc = true
			continue
		}
		if r >= 0x1100 {
			w += 2
		} else {
			w++
		}
	}
	return w
}

// truncWidth — 按显示宽度截断(保留 ANSI 转义完整;截断处补 …)。
func truncWidth(s string, max int) string {
	if dispWidth(s) <= max {
		return s
	}
	var b []rune
	w, esc := 0, false
	for _, r := range s {
		if esc {
			b = append(b, r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				esc = false
			}
			continue
		}
		if r == 0x1b {
			esc = true
			b = append(b, r)
			continue
		}
		rw := 1
		if r >= 0x1100 {
			rw = 2
		}
		if w+rw > max-1 {
			break
		}
		w += rw
		b = append(b, r)
	}
	return string(b) + "…"
}
