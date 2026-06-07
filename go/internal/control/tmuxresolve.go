package control

// tmuxresolve.go — 把「会话 id」解析到「真正在跑它的 tmux 会话名」,让绑定能扛住 /clear。
//
// 背景(bug):ccfly 把 Claude 的 session id 绑到 tmux 会话名 cc-<sid[:8]>(见 sessions.go
// 的 defaultTmuxName,与 @ccfly/react 的 tmuxName 同构)。但在同一个 tmux pane / 同一个
// claude 进程里跑 /clear,claude 会切到一个**全新的 session id**:
//   - 旧 id X 的 jsonl 冻结,新 id Y 的 jsonl 开始增长(同一个 cwd);
//   - 那个 pane 的 tmux 名字仍是 cc-<X[:8]>(tmux 名一旦建好就改不了);
//   - /sessions 现在把最近活动的 Y 排在前面,据 Y 推出 cc-<Y[:8]>——它并不存在 → live:false;
//   - 终端 attach 拿 cc-<Y[:8]> 去 `tmux new-session -A` 会**新开一个空会话**(孤儿),
//     而非接上真正在跑的 cc-<X[:8]>;sendkeys/capture/state 也都打到错的/不存在的 tmux。
//
// 一个长命 claude 进程在一个 pane 里可经多次 /clear 走过很多个 session id;tmux 名始终是
// 最初那个。所以「id ↔ tmux 名」不能再用纯函数 cc-<sid[:8]> 推,得在查询期解析。
//
// 解析信号(实测,见调研):/clear 会在**新** jsonl 开头记一条 <command-name>/clear,且新旧
// 会话**同一个 cwd**(jsonl 内的 cwd 字段)。而一个 pane 的 cwd = tmux 的 pane_current_path。
// 于是稳定的运行期信号是 **cwd + 最近活动**:在 cwd C 里跑着 claude 的那个 tmux,跑的就是
// 「cwd 为 C 的所有 Claude 会话里最新的那个」。据此:
//
//   resolveTmuxName(sid):
//     1) 若 cc-<sid[:8]> 这个 tmux 真在跑 → 直接用它(常态 / 无 /clear,零开销正确)。
//     2) 否则(post-/clear:cc-<sid[:8]> 不在跑),按 sid 的 cwd 找一个「在跑 claude 且
//        pane_current_path == 该 cwd」的 tmux,且该 tmux 自己的派生名不是某个仍在跑的精确匹配
//        (即它是个 /clear 过、名字已陈旧的 pane)→ 解析到它。
//     3) 都不命中 → 回落 cc-<sid[:8]>(行为同旧逻辑,不会更糟)。
//
//   liveSessionIDs(snaps):谁该显示 live。一个会话 live 当且仅当:它有精确同名 tmux 在跑,
//     或它经 resolveTmuxName 落到了某个在跑的 tmux 上(post-/clear 的「当前」会话)。
//
// 这样 /sessions(live)、/term(attach)、/sendkeys、/capture、/state、/start 全部经同一个
// resolveTmuxName 落到同一个真 tmux,既不开孤儿会话,也不破坏无 /clear 的常态。

import (
	"os/exec"
	"strings"
)

// tmuxPane 是一个在跑的 tmux 会话(取其首个 pane 的关键字段)。
type tmuxPane struct {
	Name     string // tmux 会话名(如 cc-43326311)
	Cwd      string // pane_current_path:该会话当前工作目录
	CurCmd   string // pane_current_command:前台命令(claude 在跑时为其版本号串,落 shell 时是 zsh/bash)
	StartCmd string // pane_start_command:会话创建时的启动命令(常含 `claude --resume <sid>`)
}

// listTmuxPanes 一次 `tmux list-panes -a` 取所有会话的首 pane 字段。
// tmux 不在跑 / 无会话(exit≠0)→ 空切片(不报错):上层据此回落,行为同「没有任何 live」。
//
// 用 list-panes -a(每会话首 pane 一行)而非 list-sessions:需要 pane_current_path /
// pane_current_command / pane_start_command 这些 pane 级字段。同名会话只取首行(够用:
// ccfly 起的会话都是单 window 单 pane)。
func listTmuxPanes() []tmuxPane {
	// 制表符(\t)分隔字段,避免路径/启动命令里的空格把字段切错(SplitN 按 \t 切)。
	out, err := exec.Command("tmux", "list-panes", "-a", "-F",
		"#{session_name}\t#{pane_current_path}\t#{pane_current_command}\t#{pane_start_command}").Output()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var panes []tmuxPane
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 4)
		if len(f) < 1 || f[0] == "" {
			continue
		}
		name := f[0]
		if seen[name] { // 多 pane 会话只认首 pane(ccfly 会话都是单 pane)
			continue
		}
		seen[name] = true
		p := tmuxPane{Name: name}
		if len(f) > 1 {
			p.Cwd = f[1]
		}
		if len(f) > 2 {
			p.CurCmd = f[2]
		}
		if len(f) > 3 {
			p.StartCmd = f[3]
		}
		panes = append(panes, p)
	}
	return panes
}

// tmuxSessionLive 判断某个 tmux 会话名当前是否在跑(`tmux has-session`)。
// 用于 /start 的幂等短路:解析后名字已在跑就别再 new-session(否则 tmux 报 duplicate)。
func tmuxSessionLive(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	return exec.Command("tmux", "has-session", "-t", "="+name).Run() == nil
}

// paneRunsClaude 粗判一个 pane 是否「正在跑 claude」(而非已掉回 shell)。
// claude 在前台时 pane_current_command 实测是其版本号串(如 "2.1.167"),不是 shell 名;
// 反之 zsh/bash/sh/fish/tmux 等视作没在跑 claude。启动命令含 claude 也作为补充证据。
func paneRunsClaude(p tmuxPane) bool {
	switch p.CurCmd {
	case "", "zsh", "bash", "sh", "fish", "dash", "tmux", "login":
		// 前台是 shell/空 → 没在跑 claude(可能 /clear 后还没起、或已退出)。
		// 但若启动命令明确是 claude 且前台仍是 claude 的某子进程,这里保守判否,
		// 交由精确同名匹配兜底(常态会话名仍精确命中,不依赖本函数)。
		return false
	}
	return true
}

// liveTmuxNames 一次取所有在跑 tmux 会话名 → set(供精确同名判定)。
// 与旧 liveTmuxSessions 等价,但复用 listTmuxPanes 的一次调用结果,避免重复 fork tmux。
func liveTmuxNames(panes []tmuxPane) map[string]bool {
	set := make(map[string]bool, len(panes))
	for _, p := range panes {
		set[p.Name] = true
	}
	return set
}

// sidCwd 取某 session id 的 cwd(其 jsonl 内第一个 cwd 字段;与 scanClaudeSessions 同口径)。
// 取不到(无该会话 / 无 cwd)→ ""。
func sidCwd(sid string, snaps []claudeSnapshot) string {
	for _, s := range snaps {
		if s.SessionID == sid {
			return s.Cwd
		}
	}
	return ""
}

// newestSidForCwd 返回 cwd 为 c 的所有 Claude 会话里「最近活动」的 session id(按 LastTs 比)。
// 这正是「在 c 里跑着的那个 claude pane 当前所处的 session」(/clear 后新 id 总是更晚)。
// 无匹配 → ""。
func newestSidForCwd(c string, snaps []claudeSnapshot) string {
	if c == "" {
		return ""
	}
	best, bestMs := "", int64(-1)
	for _, s := range snaps {
		if s.Cwd != c {
			continue
		}
		ms := tsToMs(s.LastTs)
		if ms > bestMs {
			bestMs, best = ms, s.SessionID
		}
	}
	return best
}

// resolveTmuxName 把请求里的 session id 解析到「真正在跑它的 tmux 会话名」。
//   - 常态(无 /clear):cc-<sid[:8]> 精确在跑 → 直接用,零额外解析。
//   - post-/clear:cc-<sid[:8]> 不在跑,但 sid 是其 cwd 下最新会话 → 落到那个「在 cwd 里跑
//     claude、名字已陈旧」的 tmux(它就是 /clear 前那个 pane,现在跑着 sid)。
//   - 都不命中:回落 cc-<sid[:8]>(行为同旧逻辑;attach 时仍是 new-session -A,不会更糟)。
//
// panes/snaps 由调用方一次性取好传入(handleSessions 已各取一次,避免每会话重复 fork/扫盘)。
func resolveTmuxName(sid string, panes []tmuxPane, snaps []claudeSnapshot) string {
	want := defaultTmuxName(sid)
	// 1) 精确同名在跑 → 常态命中。
	for _, p := range panes {
		if p.Name == want {
			return want
		}
	}
	// 2) post-/clear:按 cwd + 最近活动解析。仅当 sid 确是其 cwd 下「最新」会话才改绑,
	//    避免把历史旧会话错绑到当前 pane。
	cwd := sidCwd(sid, snaps)
	if cwd == "" {
		return want
	}
	if newestSidForCwd(cwd, snaps) != sid {
		return want // sid 不是该 cwd 的当前会话 → 不属于任何在跑 pane,保持原名(离线)
	}
	// 找一个「在该 cwd 跑 claude」的 pane:它就是跑 sid 的那个(名字虽是 /clear 前的陈旧 id)。
	for _, p := range panes {
		if p.Cwd == cwd && paneRunsClaude(p) {
			return p.Name
		}
	}
	return want
}

// sidForTmuxName 把一个 tmux 会话名 cc-<prefix> 反查回 session id(snaps 里 SessionID 前 8 位
// 等于 prefix 的那个)。控制端点(/sendkeys 等)收到的是 tmux 名而非 sid,需先反查再解析。
// 非 cc- 前缀(消费方自定义了 tmuxName)/ 查不到 → ""(调用方据此原样使用该名,不改绑)。
func sidForTmuxName(name string, snaps []claudeSnapshot) string {
	const pfx = "cc-"
	if !strings.HasPrefix(name, pfx) {
		return ""
	}
	prefix := name[len(pfx):]
	if prefix == "" {
		return ""
	}
	for _, s := range snaps {
		id := s.SessionID
		if len(id) >= len(prefix) && id[:len(prefix)] == prefix {
			return id
		}
	}
	return ""
}

// resolveSessionParam 是控制端点(/term、/sendkeys、/capture、/state、/start)用的解析入口:
// 输入前端传来的 tmux 会话名(默认形如 cc-<sid[:8]>),输出「真正该操作的 tmux 会话名」。
//
// 它自己取一次 list-panes + 扫一次会话(端点各自单次调用,廉价),把名字反查回 sid 后交给
// resolveTmuxName 走 cwd+最近活动的 /clear 解析。任何环节缺数据(tmux 没在跑、非 cc- 名、查不到
// sid)→ 原样返回入参 name(行为同旧逻辑,绝不更糟、不破坏自定义 tmuxName 的部署)。
//
// 关键收益:前端 /clear 后仍按「新 sid」算出 cc-<Y[:8]> 发来,这里把它解析到真正在跑的 cc-<X[:8]>,
// 于是 attach 接上真会话(不开孤儿)、sendkeys/capture/state 打中真 pane——全端点同一口径。
func resolveSessionParam(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}
	panes := listTmuxPanes()
	// 名字已精确在跑 → 常态,直接用(还省掉一次扫盘)。
	for _, p := range panes {
		if p.Name == name {
			return name
		}
	}
	snaps, err := scanClaudeSessions()
	if err != nil || len(snaps) == 0 {
		return name
	}
	sid := sidForTmuxName(name, snaps)
	if sid == "" {
		return name // 非 cc- 名 / 查不到:不改绑
	}
	return resolveTmuxName(sid, panes, snaps)
}

// liveSessionIDs 计算「哪些 session id 该显示 live」。
//
// 规则:每个在跑的 tmux 当前只跑**一个** Claude 会话(那个 pane 当下所处的 session)。一个
// session id live 当且仅当:它经 resolveTmuxName 落到某个在跑的 tmux,**且**它是落到该 tmux
// 的所有会话里「最近活动」的那个(= 该 pane 的当前会话)。
//
// 为什么要「同 tmux 取最新」:post-/clear 时旧 id X 仍有精确同名 tmux cc-X(/clear 不新建
// tmux,X 的名字残留),新 id Y 也解析到 cc-X。若只看「名字在跑」,X、Y 都会 live;但 X 已被
// /clear 取代——attach cc-X 看到的是 Y 的内容,X 实际不可达。故只把最新的 Y 标 live,X 标
// offline,前端据此不再给已死的 X 发送框/镜像。
func liveSessionIDs(panes []tmuxPane, snaps []claudeSnapshot) map[string]bool {
	liveNames := liveTmuxNames(panes)
	// 先把每个会话解析到的(在跑的)tmux 名记下,并按 tmux 名挑「最近活动」的会话。
	resolved := make(map[string]string, len(snaps)) // sid → tmux 名(仅在跑的才记)
	bestSid := map[string]string{}                  // tmux 名 → 当前(最新)会话 sid
	bestMs := map[string]int64{}
	for _, s := range snaps {
		name := resolveTmuxName(s.SessionID, panes, snaps)
		if !liveNames[name] {
			continue
		}
		resolved[s.SessionID] = name
		if ms := tsToMs(s.LastTs); ms >= bestMs[name] {
			bestMs[name] = ms
			bestSid[name] = s.SessionID
		}
	}
	out := make(map[string]bool, len(snaps))
	for _, s := range snaps {
		name, ok := resolved[s.SessionID]
		out[s.SessionID] = ok && bestSid[name] == s.SessionID
	}
	return out
}
