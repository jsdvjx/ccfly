package main

// picker.go — `ccfly a`(或无参 `ccfly attach`)的交互式选择器(两级 TUI):
//
//   第 1 级 选项目:首行「＋ 新建会话」(当前目录),其后按 cwd 分组(groupByDir 同口径)、
//           最近活动倒序;行内显示会话数 / live 数 / 最近活动时间。
//   第 2 级 选会话:首行「＋ 在此目录新建」,其后组内时间倒序,●/○ 标 live;显示真 tmux 名
//           (panemap 解析)与标题。
//   Enter：选中「＋新建」→ 在对应目录起全新 claude;选中会话 → live 则 tmux attach 镜像、
//           离线则 takeover 杀残留 + resume 重建(与 `ccfly attach <sid>` 同路径,防双写)。
//
// 权限:底部常驻「权限:<模式>」;p 循环 --permission-mode、y 切 --dangerously-skip-permissions,
//       新建与离线 resume 都按当前选项透传给 claude(live 接管不影响已在跑的 claude)。
//
// 键位:↑↓/jk 移动 · Enter/→ 进入/接管/新建 · ←/Esc 返回 · n 新建 · p 切权限模式 · y 切 skip ·
//       r 刷新 · q 退出。
// 实现:纯 ANSI + raw mode(golang.org/x/term),备用屏(1049)进出不污染滚动缓冲,
// 零 TUI 框架依赖。raw mode 下换行必须 \r\n。

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/term"

	"github.com/jsdvjx/ccfly/go/internal/control"
)

// pickResult — runPicker 的返回:接管已有会话 / 新建会话 / 取消(action 为空)。
type pickResult struct {
	action string // pickAttach | pickNew | ""(取消)
	sid    string // pickAttach:目标完整 sid
	dir    string // pickNew:新建会话的工作目录
	opts   sessionOpts
}

const (
	pickAttach = "attach"
	pickNew    = "new"
)

// runPicker — TUI 主循环。opts 为初始权限选项(来自 CLI flag),可在界面里 p/y 现场调整。
// 返回时终端已恢复,调用方据 action 走 attach / new(exec tmux 接管 TTY)。
func runPicker(opts sessionOpts) (pickResult, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return pickResult{}, fmt.Errorf("需要交互终端;脚本里请用 `ccfly new` / `ccfly attach <sid>`")
	}
	rows, err := control.CLISessions()
	if err != nil {
		return pickResult{}, err
	}
	groups := groupByDir(rows) // 可能为空:此时只能新建
	cwd, _ := os.Getwd()
	if cwd == "" {
		cwd = "."
	}

	old, err := term.MakeRaw(fd)
	if err != nil {
		return pickResult{}, err
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

	level, gi, si, gIdx := 0, 0, 0, 0 // gi 在 [NEW]+groups 上;si 在 [NEW]+rows 上;gIdx 进入的组
	in := bufio.NewReader(os.Stdin)
	for {
		w, h, e := term.GetSize(fd)
		if e != nil || w <= 0 || h <= 0 {
			w, h = 80, 24
		}
		if level == 0 {
			drawProjects(out, groups, gi, opts, cwd, w, h)
		} else {
			drawSessions(out, groups[gIdx], si, opts, w, h)
		}

		key, e := readKey(in)
		if e != nil {
			return pickResult{}, nil
		}
		cur, max := &gi, len(groups)+1 // 含首行「＋新建」
		if level == 1 {
			cur, max = &si, len(groups[gIdx].Rows)+1
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
				if gi == 0 { // 「＋ 新建会话…」→ 目录浏览器选目录
					if dir, ok := pickDir(out, in, fd, cwd, &opts); ok {
						return pickResult{action: pickNew, dir: dir, opts: opts}, nil
					}
					continue // 取消 → 回选择器(下一轮重绘)
				}
				gIdx, level, si = gi-1, 1, 0
			} else {
				if si == 0 { // 「＋ 在此目录新建」→ 直接在该项目目录建(快捷路径)
					return pickResult{action: pickNew, dir: groups[gIdx].Cwd, opts: opts}, nil
				}
				return pickResult{action: pickAttach, sid: groups[gIdx].Rows[si-1].Sid, opts: opts}, nil
			}
		case keyLeft, keyEsc:
			if level == 1 {
				level = 0
			} else {
				return pickResult{}, nil // 顶层 Esc/← = 退出
			}
		case keyNew: // n → 目录浏览器(从当前上下文起步,可任意导航后在某目录新建)
			start := cwd
			if level == 1 {
				start = groups[gIdx].Cwd
			}
			if dir, ok := pickDir(out, in, fd, start, &opts); ok {
				return pickResult{action: pickNew, dir: dir, opts: opts}, nil
			}
		case keyPerm: // 循环 --permission-mode(并清掉 skip,二者互斥)
			opts.skipPerms = false
			opts.permMode = cyclePerm(opts.permMode)
		case keySkip: // 切 --dangerously-skip-permissions
			opts.skipPerms = !opts.skipPerms
		case keyRefresh:
			if rs, e := control.CLISessions(); e == nil {
				groups = groupByDir(rs)
				if gi > len(groups) {
					gi = len(groups)
				}
				if level == 1 {
					if gIdx >= len(groups) {
						level, gIdx, si = 0, 0, 0
					} else if si > len(groups[gIdx].Rows) {
						si = len(groups[gIdx].Rows)
					}
				}
			}
		case keyQuit:
			return pickResult{}, nil
		}
	}
}

// cyclePerm 按 permModes 顺序循环到下一个;空("")视作 default 起点。
func cyclePerm(cur string) string {
	idx := 0
	for i, m := range permModes {
		if m == cur {
			idx = i
			break
		}
	}
	return permModes[(idx+1)%len(permModes)]
}

// ── 目录浏览器(新建会话选目录)────────────────────────────────────────────────

func stdinIsTTY() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// browseDir 是 `ccfly new` 无参时的独立入口:自建 raw mode + 备用屏,浏览目录选一个。
// 非交互终端 → 报错(调用方据此回退到 cwd)。
func browseDir(start string, opts *sessionOpts) (string, bool, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", false, fmt.Errorf("需要交互终端;用 `ccfly new <dir>` 直接指定目录")
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return "", false, err
	}
	out := bufio.NewWriter(os.Stdout)
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
	out.Flush()
	defer func() {
		fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")
		out.Flush()
		_ = term.Restore(fd, old)
	}()
	dir, ok := pickDir(out, bufio.NewReader(os.Stdin), fd, start, opts)
	return dir, ok, nil
}

// pickDir 文件系统目录浏览器(假定 raw mode + 备用屏已开,供 picker 内嵌或 browseDir 调)。
// 键:↑↓/jk 移动 · Enter/→ 进目录(在 .. 上 = 上级)· ←/h 上级 · n 在当前目录新建 · Esc/q 取消。
// opts 指针:浏览时也能 p/y 改权限,变更回写给调用方。
func pickDir(out *bufio.Writer, in *bufio.Reader, fd int, start string, opts *sessionOpts) (string, bool) {
	cur := start
	if cur == "" {
		cur = "."
	}
	if abs, err := filepath.Abs(cur); err == nil {
		cur = abs
	}
	sel := 0
	for {
		entries := subdirs(cur)
		w, h, e := term.GetSize(fd)
		if e != nil || w <= 0 || h <= 0 {
			w, h = 80, 24
		}
		drawDirBrowser(out, cur, entries, sel, *opts, w, h)
		key, e := readKey(in)
		if e != nil {
			return "", false
		}
		total := len(entries) + 1 // 含首行 ".."
		switch key {
		case keyUp:
			if sel > 0 {
				sel--
			}
		case keyDown:
			if sel < total-1 {
				sel++
			}
		case keyEnter, keyRight:
			if sel == 0 {
				cur, sel = filepath.Dir(cur), 0 // ".." → 上级
			} else {
				cur, sel = filepath.Join(cur, entries[sel-1]), 0
			}
		case keyLeft:
			cur, sel = filepath.Dir(cur), 0
		case keyNew:
			return cur, true // n → 在当前目录新建
		case keyPerm:
			opts.skipPerms = false
			opts.permMode = cyclePerm(opts.permMode)
		case keySkip:
			opts.skipPerms = !opts.skipPerms
		case keyEsc, keyQuit:
			return "", false
		}
	}
}

// subdirs 返回 dir 下的子目录名(含指向目录的符号链接),按名排序;跳过隐藏(.开头)。
func subdirs(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if len(name) > 0 && name[0] == '.' {
			continue // 跳过隐藏目录(需要隐藏目录用 `ccfly new <path>` 直指)
		}
		if e.IsDir() {
			out = append(out, name)
		} else if e.Type()&os.ModeSymlink != 0 {
			if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && fi.IsDir() {
				out = append(out, name)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

func drawDirBrowser(out *bufio.Writer, cur string, entries []string, sel int, opts sessionOpts, w, h int) {
	fmt.Fprint(out, "\x1b[2J\x1b[H")
	title := "新建会话 — 选择目录"
	help := "↑↓ · Enter/→ 进 · ← 上级 · n 在此新建 · q 取消"
	fmt.Fprintf(out, "\x1b[1m%s\x1b[0m\r\n", padBetween(title, help, w))
	fmt.Fprintf(out, "\x1b[90m📁 %s\x1b[0m\r\n\r\n", collapseHome(cur))
	n := h - 6
	if n < 1 {
		n = 1
	}
	total := len(entries) + 1
	off := viewport(sel, total, n)
	for i := off; i < total && i < off+n; i++ {
		if i == 0 {
			drawRow(out, "\x1b[90m..  (上级)\x1b[0m", sel == 0, w)
			continue
		}
		drawRow(out, "\x1b[36m"+entries[i-1]+"/\x1b[0m", i == sel, w)
	}
	drawFooter(out, opts, h)
	out.Flush()
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
	keyNew
	keyPerm
	keySkip
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
	case 'n':
		return keyNew, nil
	case 'p':
		return keyPerm, nil
	case 'y':
		return keySkip, nil
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

func drawProjects(out *bufio.Writer, groups []dirGroup, cursor int, opts sessionOpts, cwd string, w, h int) {
	fmt.Fprint(out, "\x1b[2J\x1b[H")
	title := "ccfly a — 选择项目 / 新建会话"
	help := "↑↓ · Enter 进入/新建 · n 新建 · p/y 权限 · q 退出"
	fmt.Fprintf(out, "\x1b[1m%s\x1b[0m\r\n\r\n", padBetween(title, help, w))
	n := h - 5 // 预留 标题(1)+空行(1)+底部页脚(2)
	if n < 1 {
		n = 1
	}
	total := len(groups) + 1
	off := viewport(cursor, total, n)
	for i := off; i < total && i < off+n; i++ {
		if i == 0 {
			drawRow(out, fmt.Sprintf("\x1b[32m＋ 新建会话…\x1b[0m  \x1b[90m选目录(默认 %s)\x1b[0m", collapseHome(cwd)), cursor == 0, w)
			continue
		}
		g := groups[i-1]
		nLive := 0
		for _, r := range g.Rows {
			if r.Live {
				nLive++
			}
		}
		cwdName := collapseHome(g.Cwd)
		if cwdName == "" {
			cwdName = "(未知目录)"
		}
		meta := fmt.Sprintf("%d 会话", len(g.Rows))
		if nLive > 0 {
			meta = fmt.Sprintf("\x1b[32m%d live\x1b[0m · %s", nLive, meta)
		}
		line := fmt.Sprintf("%s  \x1b[90m%s · %s\x1b[0m", cwdName, meta, fmtAge(g.Rows[0].Age))
		drawRow(out, line, i == cursor, w)
	}
	drawFooter(out, opts, h)
	out.Flush()
}

func drawSessions(out *bufio.Writer, g dirGroup, cursor int, opts sessionOpts, w, h int) {
	fmt.Fprint(out, "\x1b[2J\x1b[H")
	cwd := collapseHome(g.Cwd)
	help := "Enter 接管/新建 · n 新建 · ←/Esc 返回 · p/y 权限 · q 退出"
	fmt.Fprintf(out, "\x1b[1m%s\x1b[0m\r\n\r\n", padBetween(cwd+" — 选择会话 / 新建", help, w))
	n := h - 5
	if n < 1 {
		n = 1
	}
	total := len(g.Rows) + 1
	off := viewport(cursor, total, n)
	for i := off; i < total && i < off+n; i++ {
		if i == 0 {
			drawRow(out, "\x1b[32m＋ 在此目录新建会话\x1b[0m", cursor == 0, w)
			continue
		}
		r := g.Rows[i-1]
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
	drawFooter(out, opts, h)
	out.Flush()
}

// drawFooter — 底部行常驻当前权限选项(新建/resume 都按此透传给 claude)。
func drawFooter(out *bufio.Writer, opts sessionOpts, h int) {
	var mode string
	switch {
	case opts.skipPerms:
		mode = "\x1b[31mskip-all (--dangerously-skip-permissions)\x1b[0m"
	case opts.permMode != "" && opts.permMode != "default":
		mode = "\x1b[33m--permission-mode " + opts.permMode + "\x1b[0m"
	default:
		mode = "\x1b[90m默认\x1b[0m"
	}
	fmt.Fprintf(out, "\x1b[%d;1H\x1b[2K\x1b[90m权限:\x1b[0m %s  \x1b[90m[p 切模式 · y 切 skip]\x1b[0m", h, mode)
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
