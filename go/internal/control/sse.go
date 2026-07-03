package control

// sse.go — GET /sse/jsonl:把一个会话的 jsonl 以 SSE 增量推给表世界(ccfly-ttyd-ui 的状态源)。
//
// 与 /transcript(渲染成 blocks)不同,这里推**原始 jsonl 行 + 字节 offset**,供客户端自己做
// screen→struct 的 jsonl 侧判断(turn/mode/title/...)与确认式命令的结果读取。
//
//   - id = 该行末尾字节 offset → 浏览器断线自动带 Last-Event-ID 续传。
//   - 跟随会话当前文件:claude 冷启动 / /clear 会换 jsonl,据 fsnotify 目录事件重解析并切文件
//     (否则 /context 这类靠 jsonl 取结果的命令会超时)。
//   - 纯 fsnotify 驱动(无轮询);handler 单 goroutine 顺序处理,天然无重入/不重复发行。
//   - ?path 必须落在 ~/.claude/projects 下(防任意文件读取)。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

type rawLine struct {
	off  int64
	line string
}

// resolveSessionJsonlPane:tmux 会话名(或 cc-<sid8> 形式的「期望名」)→ (jsonl 文件, 承载它的活 pane 名)。
// pane 名是 /clear 跟随的锚:pane 的 tmux 名永不变,而其中跑着的 sid 会被 /clear、里世界 /resume 换掉。
// /sse/jsonl 用返回的 pane 名做后续重解析(而非请求里的 session 名)——选中会话跳到新 sid 后,
// 请求名(cc-<新sid8>)不再对应任何真 tmux,若仍按它重解析,第二次 /clear 起就永远跟不动了。
// 解析优先级:
//   1) 真值表 byName:session 恰是活 pane 名且已被 hook 认领 → 该 pane 的**当前** sid(/clear 后即新会话;
//      这是 switched 的主通路)。名字编码的 sid 是「初始」会话,若先查名字,/clear 后旧 jsonl 仍在 →
//      永远短路返回旧文件,switched 永不触发、前端从不跟跳。
//   2) 名字编码的具体 sid(cc-<sid8>):真值表 bySid 找到正跑它的 pane → 锚到该 pane;否则按历史会话
//      静态渲染(pane="",不跟随)。同一 cwd 下常有多个会话,绝不能用「cwd 最新」一刀切——否则同目录的
//      每个会话都被渲染成最近活动的那一个(用户报的「tmux 里的 session 和真实 session 不一样」正是此因)。
//   2.5) 跟随重连愈合(仅 follow=true):cc-<sid8> 指向的会话**已死**(不是任何 pane 的当前会话),
//        但真值表的 Prev 轨迹记得哪个活 pane 曾跑过它 → 锚回那个 pane、直接续其**当前**会话
//        (followed=true,调用方据此忽略旧文件的 offset)。覆盖「断线窗口里错过 /clear」:
//        服务重启/网络断时 /clear 发生,switched 没人收到,重连带着死 sid 永远卡在旧文件。
//        follow 由客户端声明(SSE 自动重连的 Last-Event-ID / 显式 ?follow=1)——主动点开历史
//        会话不带 follow,仍按 3) 静态渲染,绝不把读历史的人跳走。
//   3) 兜底:把 session 当活 pane 名,取其 cwd 下最近活动 sid(hook 未覆盖的存量进程/自定义 tmux 名)。
func resolveSessionJsonlPane(session string, follow bool) (path, pane string, followed bool) {
	if session == "" {
		return "", "", false
	}
	panes := listTmuxPanes()
	pmap := loadPaneMap()
	own := ownershipFor(panes, pmap)
	if sid, ok := own.byName[session]; ok {
		if p := transcriptPath(sid); p != "" {
			return p, session, false
		}
	}
	// 名字编码 sid 的最快真值来源:真值表 bySid(存完整 sid)。/clear 跟跳后前端立刻用
	// session=cc-<新sid8> 重连,此刻 scanClaudeSessions 的 memo 窗口里常还没有新 jsonl →
	// sidForTmuxName 查不到 → 404;而 EventSource 对非 200 **直接放弃不重试**,流就死了。
	// hook 在 /clear 当刻已登记新 sid → 据前缀直接配真值表,绕开快照时差。
	if strings.HasPrefix(session, "cc-") && len(session) > 3 {
		pfx := session[3:]
		for sid, name := range own.bySid {
			if strings.HasPrefix(sid, pfx) {
				if p := transcriptPath(sid); p != "" {
					return p, name, false
				}
			}
		}
		// 2.5) 到这里 = 该 sid 不是任何活 pane 的当前会话(已死)。跟随方(follow)→ Prev 轨迹愈合。
		if follow {
			if name, cur := paneByFormerSid(panes, pmap, pfx); name != "" {
				if p := transcriptPath(cur); p != "" {
					return p, name, true
				}
			}
		}
	}
	snaps, _ := scanClaudeSessions()
	if sid := sidForTmuxName(session, snaps); sid != "" {
		if p := transcriptPath(sid); p != "" {
			if name, ok := own.bySid[sid]; ok {
				return p, name, false // sid 正跑在某 pane(名字可能 ≠ session):锚到真 pane
			}
			// session 本身是活 pane 名(无 hook 数据的存量进程)→ 仍锚到它;否则历史会话,静态。
			for _, pn := range panes {
				if pn.Name == session {
					return p, session, false
				}
			}
			return p, "", false
		}
	}
	// cc-<sid8> 是 ccfly 受管会话名,其 sid 是权威的:走到这里仍没找到它的 jsonl =
	// 该会话刚新建、transcript 尚未落盘(或已消亡)。绝不回退到「同 cwd 最新会话」——
	// 否则「在此目录新建」会被渲染成同目录里的旧会话(实测 bug)。返回空让前端按
	// 「暂无记录」处理(下次文件出现/重连即解析到真会话)。cwd-最新兜底只服务**非** cc-
	// 的自定义 tmux 名(hook 未覆盖的存量进程)。
	if strings.HasPrefix(session, "cc-") {
		return "", "", false
	}
	cwd := ""
	for _, p := range panes {
		if p.Name == session {
			cwd = p.Cwd
			break
		}
	}
	if cwd == "" {
		return "", "", false
	}
	if cur := newestSidForCwd(cwd, snaps); cur != "" {
		return transcriptPath(cur), session, false
	}
	return "", "", false
}

// resolveSessionJsonl:同上,只要文件(/jsonl/before 等一次性读取用,无跟随语义)。
func resolveSessionJsonl(session string) string {
	p, _, _ := resolveSessionJsonlPane(session, false)
	return p
}

// confineProjects:显式 path 必须在 ~/.claude/projects 下,否则空。
func confineProjects(p string) string {
	root := claudeProjectsDir()
	clean := filepath.Clean(p)
	if clean == root || strings.HasPrefix(clean, root+string(os.PathSeparator)) {
		return clean
	}
	return ""
}

// rawJsonlSince:从 since 字节读完整行,返回 [(末尾offset, 行)] + 新游标。
// 末尾半截行(正在追加写)不消费、不推进游标,留待下次 since=cursor 续读(同 transcript.go 口径)。
func rawJsonlSince(path string, since int64) ([]rawLine, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, since
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, since
	}
	if since > st.Size() { // 文件变小(轮转/重写)→ 从头
		since = 0
	}
	if since > 0 {
		if _, e := f.Seek(since, 0); e != nil {
			return nil, since
		}
	}
	cursor := since
	r := bufio.NewReader(f)
	var out []rawLine
	for {
		b, e := r.ReadBytes('\n')
		if len(b) > 0 && b[len(b)-1] == '\n' {
			cursor += int64(len(b))
			line := strings.TrimRight(string(b), "\r\n")
			if strings.TrimSpace(line) != "" {
				out = append(out, rawLine{cursor, line})
			}
		}
		if e != nil {
			break // EOF/错误:停;半行未消费
		}
	}
	return out, cursor
}

func handleSseJsonl(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	session := q.Get("session")
	pane := ""       // /clear 跟随的锚(空 = 静态文件/显式 path,不跟随)
	followed := false // 2.5) 愈合重定向:请求的死 sid 已被替换,本流直接续 pane 的当前会话
	// follow 声明 =「我在跟随这条流,不是来读历史的」:浏览器 SSE 自动重连必带 Last-Event-ID;
	// 客户端整体重开(EventSource CLOSED 后兜底 open)显式带 ?follow=1。两者都允许 2.5) 愈合。
	follow := q.Get("follow") == "1" || r.Header.Get("Last-Event-ID") != ""
	var path string
	if p := q.Get("path"); p != "" {
		if path = confineProjects(p); path == "" {
			ctrlErr(w, 403, "path not allowed")
			return
		}
	} else {
		path, pane, followed = resolveSessionJsonlPane(session, follow)
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		ctrlErr(w, 500, "stream unsupported")
		return
	}
	writeSSEHead := func() {
		h := w.Header()
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
	}
	if path == "" {
		// 全新会话竞速:cc- 名的 tmux 活着,但 claude 在第一条消息前**不写 jsonl**。若此刻 404,
		// EventSource 对非 200 直接放弃 → 前端输入框卡「连接中」永远发不出第一条消息 → transcript
		// 永远不会出现(死锁;Windows 实测)。改为 200 挂住:先发 waiting meta,轮询到文件出现再进正常流。
		if !strings.HasPrefix(session, "cc-") || !tmuxSessionLive(session) {
			ctrlErr(w, 404, "no jsonl for session")
			return
		}
		writeSSEHead()
		fmt.Fprint(w, "retry: 1000\n")
		fmt.Fprint(w, "event: meta\ndata: {\"waiting\":true}\n\n")
		fl.Flush()
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for path == "" {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				path, pane, followed = resolveSessionJsonlPane(session, follow)
			}
		}
	} else {
		writeSSEHead()
	}

	since := int64(0)
	if followed {
		// 愈合重定向:客户端带的 offset/缓存全是**旧文件**的,对新文件无意义 → 一律从头
		// (下方 ?tail 仍生效 → 尾窗起步);meta.fresh=true 让前端清空重建并跟跳新会话。
	} else if lid := r.Header.Get("Last-Event-ID"); lid != "" {
		// 断线自动重连:浏览器带上最后收到的 offset(对当前文件恒有效),优先。
		if n, e := strconv.ParseInt(lid, 10, 64); e == nil {
			since = n
		}
	} else if sq := q.Get("since"); sq != "" && q.Get("sincePath") == path {
		// 前端重开会话:带上次本地缓存的 offset + 文件路径;仅当解析到的仍是同一文件才据此发增量
		// (否则 /clear 换了文件,旧 offset 对新文件无意义 → 落到 since=0 从头发,meta.fresh=true 让前端弃缓存)。
		if n, e := strconv.ParseInt(sq, 10, 64); e == nil && n >= 0 {
			since = n
		}
	}

	// 按需「最新优先」首拉:未续传(since==0)且带 ?tail=N 时,反向定位最后 N 行的行首字节作为起点,
	// 只发尾窗;since 随后在 drain 里推进到 EOF,fsnotify 实时追加照旧。headStart=尾窗最旧行的行首字节,
	// 经 meta 给前端,供向上翻页(/jsonl/before?before=headStart)。续传(Last-Event-ID/sincePath)时不 tail。
	resuming := since > 0
	headStart := int64(0)
	if !resuming {
		if tq := q.Get("tail"); tq != "" {
			if n, e := strconv.Atoi(tq); e == nil && n > 0 {
				if st, e2 := os.Stat(path); e2 == nil && st.Size() > 0 {
					if start := startByteForLastN(path, st.Size(), n, transcriptWindowBytes); start > 0 {
						since = start
						headStart = start
					}
				}
			}
		}
	}

	current := path
	// fresh:本次是否「整段重建」——从文件头 或 从尾窗头(tail)发,前端据此清空重收;续传则 false(用缓存做基底)。
	// headStart>0 表示这是个尾窗、其上还有更老的行,前端可向上翻页。
	meta := func(switched bool) {
		b, _ := json.Marshal(map[string]any{
			"path":      current,
			"switched":  switched,
			"fresh":     switched || !resuming,
			"headStart": headStart,
			"hasMore":   headStart > 0,
		})
		fmt.Fprintf(w, "event: meta\ndata: %s\n\n", b)
	}
	drain := func() {
		lines, next := rawJsonlSince(current, since)
		since = next
		for _, ln := range lines {
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ln.off, ln.line)
		}
		if len(lines) > 0 {
			fl.Flush()
		}
	}

	fmt.Fprint(w, "retry: 1000\n")
	meta(followed) // 愈合重定向时首个 meta 即标 switched:前端按「换了文件」清空重建并跟跳
	fl.Flush()
	drain()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	defer watcher.Close()
	dir := filepath.Dir(current)
	_ = watcher.Add(dir)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(ev.Name, ".jsonl") {
				continue
			}
			if ev.Name == current {
				if ev.Op&(fsnotify.Write|fsnotify.Create) != 0 {
					drain()
				}
				continue
			}
			// 别的 jsonl 有动静 → 会话可能换了文件(冷启动/clear),按**pane 锚**重解析并切
			// (pane 名稳定;请求里的 session 名在首跳后已陈旧,按它重解析会卡死在旧文件)。
			if pane != "" {
				// 按 pane 名重解析走优先级 1(byName 真值表直查),无需 follow 愈合。
				np, _, _ := resolveSessionJsonlPane(pane, false)
				if np != "" && np != current {
					if nd := filepath.Dir(np); nd != dir {
						_ = watcher.Remove(dir)
						_ = watcher.Add(nd)
						dir = nd
					}
					current = np
					since = 0
					headStart = 0 // 新文件从头发,无尾窗 → 无更老可翻
					meta(true)
					drain()
				}
			}
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

// rawJsonlRange:读 [start, end) 内的完整行,返回原始行 + 本批最旧行的行首字节(firstStart)。
// 与 rawJsonlSince 同口径(只取以 '\n' 结尾的完整行、跳空行),但**到 end 即止**(不读到 EOF):
// end = 调用方当前最旧已加载行的行首字节,故本批与已加载区严丝合缝、无重叠。
func rawJsonlRange(path string, start, end int64) ([]string, int64) {
	if start < 0 {
		start = 0
	}
	if end <= start {
		return nil, 0
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	if start > 0 {
		if _, e := f.Seek(start, 0); e != nil {
			return nil, 0
		}
	}
	cursor := start
	firstStart := int64(-1)
	r := bufio.NewReader(f)
	var out []string
	for cursor < end {
		b, e := r.ReadBytes('\n')
		if len(b) > 0 && b[len(b)-1] == '\n' {
			ls := cursor
			cursor += int64(len(b))
			if cursor > end {
				break // 行越过 end:属于已加载区,不纳入
			}
			line := strings.TrimRight(string(b), "\r\n")
			if strings.TrimSpace(line) != "" {
				if firstStart < 0 {
					firstStart = ls
				}
				out = append(out, line)
			}
		}
		if e != nil {
			break
		}
	}
	if firstStart < 0 {
		firstStart = 0
	}
	return out, firstStart
}

// handleJsonlBefore — GET /jsonl/before?(session=..|path=..)&before=<byte>:一次性返回 before 字节之前的
// 一窗更老原始 jsonl 行(向上翻页用)。与 /sse/jsonl 同口径的路径解析 + confine 安全闸;无状态、不开 watcher,
// 绝不扰动实时 tail 或 tmux。响应 {path, lines:[原始行], firstStart, hasMore}。
func handleJsonlBefore(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var path string
	if p := q.Get("path"); p != "" {
		if path = confineProjects(p); path == "" {
			ctrlErr(w, 403, "path not allowed")
			return
		}
	} else {
		path = resolveSessionJsonl(q.Get("session"))
	}
	if path == "" {
		ctrlErr(w, 404, "no jsonl for session")
		return
	}
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	var lines []string
	var firstStart int64
	if before > 0 {
		if st, e := os.Stat(path); e == nil && before > st.Size() {
			before = st.Size()
		}
		start := startByteForLastN(path, before, transcriptWindowItems, transcriptWindowBytes)
		lines, firstStart = rawJsonlRange(path, start, before)
	}
	if lines == nil {
		lines = []string{}
	}
	b, _ := json.Marshal(map[string]any{
		"path":       path,
		"lines":      lines,
		"firstStart": firstStart,
		"hasMore":    firstStart > 0,
	})
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}
