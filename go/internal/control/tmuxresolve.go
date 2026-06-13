package control

// tmuxresolve.go — 把「会话 id」解析到「真正在跑它的 tmux 会话名」,扛住 /clear、里世界
// /resume 切换、/compact 等一切「pane 不变、sid 变了」的时刻。
//
// 背景(bug):ccfly 把 Claude 的 session id 绑到 tmux 会话名 cc-<sid[:8]>(见 sessions.go
// 的 defaultTmuxName,与 @ccfly/react 的 tmuxName 同构)。但 pane 里跑着的 claude 会换
// session id(/clear、在里世界 /resume 到别的会话、/compact……),而 tmux 名一旦建好就改不了:
//   - 旧 id X 的 jsonl 冻结,新 id Y 的 jsonl 开始增长(同一个 cwd);
//   - 那个 pane 的 tmux 名字仍是 cc-<X[:8]>;
//   - 据 Y 推出的 cc-<Y[:8]> 并不存在 → attach 开孤儿、sendkeys/capture/state 打不中。
//
// 解析分层(resolveTmuxName,从确定到兜底):
//   0) 真值表(panemap.go):SessionStart hook 在会话开始时登记「pane → 当前 sid」。查到 →
//      直接用,确定性正确,同 cwd 多 pane 也绝不串。
//   1) 精确同名在跑 → 常态命中(无 sid 漂移,零开销正确)。
//   2) fail-closed 启发式(hook 未覆盖的存量会话):按 sid 的 cwd 找「在跑 claude 且
//      pane_current_path == 该 cwd、且未被真值表认领给别的会话」的 pane,**有且仅有一个**
//      且 sid 是该 cwd 下(排除已认领者后)最近活动的会话 → 解析到它。
//      同 cwd 出现 ≥2 个候选 pane → **不猜**,回落本名(发不出 = 409/offline,可恢复;
//      旧版取 list 顺序第一个 = 抽奖,猜错即把消息打进别人的对话,不可恢复——本文件曾经的
//      头号恶性 bug,详见 panemap.go 头注)。
//   3) 都不命中 → 回落 cc-<sid[:8]>(行为同旧逻辑,不会更糟)。
//
//   liveSessionIDs:谁该显示 live。每个在跑 tmux 当前只跑一个会话:真值表认领的 sid 优先,
//     否则取解析到该 tmux 的会话里最近活动的那个;其余(被取代的旧 id)offline。
//
//   resolveSessionTarget:控制端点入口,额外回报 stale——请求的 cc-<sid8> 名虽在跑,但真值表
//     说那个 pane 已是**另一个**会话(/clear 后名字残留)。调用方据此 409 拒发,关死「看着 A 的
//     transcript、消息打进 B」的反向错发。
//
// 这样 /sessions(live)、/term(attach)、/sendkeys、/capture、/state、/start 全部经同一
// 套解析落到同一个真 tmux,既不开孤儿会话,也绝不把键打进别的会话。

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// tmuxPane 是一个在跑的 tmux 会话(取其首个 pane 的关键字段)。
type tmuxPane struct {
	Name     string // tmux 会话名(如 cc-43326311)
	Cwd      string // pane_current_path:该会话当前工作目录
	CurCmd   string // pane_current_command:前台命令(claude 在跑时为其版本号串,落 shell 时是 zsh/bash)
	StartCmd string // pane_start_command:会话创建时的启动命令(常含 `claude --resume <sid>`)
	Created  int64  // #{session_created}(epoch 秒):reap 宽限期判定(后台巡检用)
	Attached int    // #{session_attached}(连接客户端数):有人 attach 着就别杀(后台巡检用)
	WinW     int    // #{window_width}:当前窗口列数(chat 隐藏终端自适应、不改 tmux 尺寸用)
	WinH     int    // #{window_height}:当前窗口行数
	PaneID   string // #{pane_id}(如 "%5"):panemap 真值表的键(hook 端用 $TMUX_PANE 登记)
	// StatusLines:该会话 tmux 状态栏占的行数(status 选项:off→0,on→1,2..5→N)。
	// 客户端视口 = 窗口行数 + 状态栏行数;/sessions 的 rows 必须按「客户端视口」报,
	// 否则按窗口行数连入的 web 隐藏终端会矮一行,pane 最底行(idle footer 锚点)被裁,
	// 表世界读屏检测整体失效(实案:状态卡死「生成中」)。
	StatusLines int
}

// listTmuxPanes 一次 `tmux list-panes -a` 取所有会话的首 pane 字段。
// tmux 不在跑 / 无会话(exit≠0)→ 空切片(不报错):上层据此回落,行为同「没有任何 live」。
//
// 用 list-panes -a(每会话首 pane 一行)而非 list-sessions:需要 pane_current_path /
// pane_current_command / pane_start_command 这些 pane 级字段。同名会话只取首行(够用:
// ccfly 起的会话都是单 window 单 pane)。
func listTmuxPanes() []tmuxPane {
	// 制表符(\t)分隔字段,避免路径/启动命令里的空格把字段切错(SplitN 按 \t 切)。
	// 末两段 session_created/session_attached 供后台巡检(scanner.go)判宽限期 + attach 数;
	// 前 4 段索引不变,旧调用方/测试(struct 字面量)零影响。
	// 末段 #{status}:tmux ≥3.0 的格式串可直接引用选项,按会话上下文展开(尊重会话级覆盖)。
	out, err := exec.Command("tmux", "list-panes", "-a", "-F",
		"#{session_name}\t#{pane_current_path}\t#{pane_current_command}\t#{pane_start_command}\t#{session_created}\t#{session_attached}\t#{window_width}\t#{window_height}\t#{pane_id}\t#{status}").Output()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var panes []tmuxPane
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 10)
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
		if len(f) > 4 {
			p.Created, _ = strconv.ParseInt(f[4], 10, 64) // 解析失败 → 0(交由巡检按「无宽限信息」处理)
		}
		if len(f) > 5 {
			p.Attached, _ = strconv.Atoi(f[5]) // 解析失败 → 0
		}
		if len(f) > 6 {
			p.WinW, _ = strconv.Atoi(f[6])
		}
		if len(f) > 7 {
			p.WinH, _ = strconv.Atoi(f[7])
		}
		if len(f) > 8 {
			p.PaneID = f[8]
		}
		if len(f) > 9 {
			p.StatusLines = statusLineCount(f[9])
		}
		panes = append(panes, p)
	}
	return panes
}

// statusLineCount 把 tmux status 选项值换算成状态栏行数:off→0,on→1,"2".."5"→N。
// 未知值按 1 处理(status 非 off 时至少占一行,宁可多报一行也别再裁掉 pane 底行)。
func statusLineCount(v string) int {
	switch v = strings.TrimSpace(v); v {
	case "off", "0", "":
		return 0
	case "on", "1":
		return 1
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 2 && n <= 5 {
		return n
	}
	return 1
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
// 无匹配 → ""。(sse.go 的 jsonl 兜底仍用;解析改绑用 ownership 感知的 newestUnownedSidForCwd。)
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

// newestUnownedSidForCwd 同 newestSidForCwd,但跳过已被真值表认领到某个活 pane 的会话——
// 它们「有家可归」,不参与启发式兜底的「该 cwd 当前会话」竞争(否则一个 hook 已覆盖、
// 恰好最近很活跃的会话会把未覆盖的存量会话挤成「非最新」,令后者无谓地解析失败)。
func newestUnownedSidForCwd(c string, snaps []claudeSnapshot, own paneOwnership) string {
	if c == "" {
		return ""
	}
	best, bestMs := "", int64(-1)
	for _, s := range snaps {
		if s.Cwd != c {
			continue
		}
		if _, owned := own.bySid[s.SessionID]; owned {
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
// 分层见文件头注:0) panemap 真值表(确定性) 1) 精确同名(常态) 2) fail-closed 启发式
// (cwd 内唯一候选才改绑) 3) 回落本名(行为同旧逻辑;attach 时仍是 new-session -A,不会更糟)。
//
// panes/snaps/own 由调用方一次性取好传入(handleSessions 已各取一次,避免每会话重复 fork/扫盘)。
func resolveTmuxName(sid string, panes []tmuxPane, snaps []claudeSnapshot, own paneOwnership) string {
	// 0) 真值表:hook 登记过「某活 pane 当前就是 sid」→ 直接用,同 cwd 多 pane 也绝不串。
	if name, ok := own.bySid[sid]; ok {
		return name
	}
	want := defaultTmuxName(sid)
	// 1) 精确同名在跑 → 常态命中。
	for _, p := range panes {
		if p.Name == want {
			return want
		}
	}
	// 2) fail-closed 启发式(hook 未覆盖的存量会话):仅当 sid 是其 cwd 下(排除已认领者后)
	//    最近活动的会话,且该 cwd 恰有**一个**未被认领、在跑 claude 的 pane,才改绑到它。
	//    ≥2 个候选 → 不猜(回落本名→发不出,可恢复;猜错→消息进别人的对话,不可恢复)。
	cwd := sidCwd(sid, snaps)
	if cwd == "" {
		return want
	}
	if newestUnownedSidForCwd(cwd, snaps, own) != sid {
		return want // sid 不是该 cwd 的当前会话 → 不属于任何在跑 pane,保持原名(离线)
	}
	cand, n := "", 0
	for _, p := range panes {
		if p.Cwd != cwd || !paneRunsClaude(p) {
			continue
		}
		if _, owned := own.byName[p.Name]; owned {
			continue // 真值表已认领给别的会话(sid 自己的话第 0 层就返回了)
		}
		cand, n = p.Name, n+1
	}
	if n == 1 {
		return cand
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

// claudeResumeCmd 为一个(已被 resolveSessionParam 解析过的)tmux 名构造「该会话该跑的」
// claude --resume 命令 + 其原始 cwd:会话不存在时 new-session -A 用它直接拉起 claude(而非裸壳),
// 已在跑则被 -A 忽略只 attach(绝不重启正在跑的 claude)。
// tmux 名 cc-<X8> 唯一对应会话 X → resume 的就是 X(用其冻结的原始 cwd)。**绝不**用
// newestSidForCwd 改写 sid:同一 project 目录常有多个会话,改写会把点开的 X 偷换成该 cwd 最新的 Y
// → 打开错会话。/clear 镜像不靠这里:那种情形 sess 已是在跑 pane,new-session -A 会 attach 并
// 「忽略」本 cmd,故无需在此改 resume 目标。
// 任一环节缺数据(非 cc- 名 / 查不到 / 无 cwd / cwd 已删 / claude 不在 PATH)→ ok=false,
// 调用方回落今日的裸壳行为(绝不更糟、不破坏自定义 tmuxName 部署)。
func claudeResumeCmd(tmuxName string, snaps []claudeSnapshot) (cmd, cwd string, ok bool) {
	sid := sidForTmuxName(tmuxName, snaps)
	if sid == "" {
		return "", "", false // 非 cc- 名 / 查不到 → 不强加 claude
	}
	cwd = sidCwd(sid, snaps) // X 的冻结(原始)cwd —— --resume 必须在此目录跑,否则 No conversation found
	if cwd == "" {
		return "", "", false // 无原始 cwd → 不敢拼 --resume
	}
	if fi, e := os.Stat(cwd); e != nil || !fi.IsDir() {
		return "", "", false // cwd 已不存在 → new-session -c 会失败 → 回落裸壳
	}
	if _, e := exec.LookPath("claude"); e != nil {
		return "", "", false // claude 不在 PATH → 回落裸壳(绝不让会话起不来)
	}
	// sid 是磁盘 jsonl 派生的 uuid(无空格/元字符);tmux 自行按词把 cmd 串拆成 argv 直接 exec(非经 shell),故单串安全。
	return "claude --resume " + sid, cwd, true
}

// resolveSessionParam 是控制端点(/term、/capture 等只读/attach 路径)用的解析入口:
// 输入前端传来的 tmux 会话名(默认形如 cc-<sid[:8]>),输出「真正该操作的 tmux 会话名」。
// 写路径(/sendkeys、/state、/start)用 resolveSessionTarget 以拿到 stale 标志。
func resolveSessionParam(name string) string {
	sess, _ := resolveSessionTarget(name)
	return sess
}

// staleExactTarget 判定「请求的 cc- 名虽精确在跑,但已易主」:真值表说该 pane 当前跑的是
// **另一个** sid(/clear、里世界 /resume 之后名字残留)。此时按名编码的那个会话实际已不可达,
// 往这个 pane 发键 = 打进别人的对话。仅对默认 cc-<sid8> 命名生效(自定义 tmuxName 部署无从
// 推断请求者意图,不判;真值表查不到也不判——无数据时保持旧行为)。纯函数,便于单测。
func staleExactTarget(name string, own paneOwnership) bool {
	if !strings.HasPrefix(name, "cc-") {
		return false
	}
	ownerSid, ok := own.byName[name]
	return ok && defaultTmuxName(ownerSid) != name
}

// resolveSessionTarget 解析 + 易主检测:返回「真正该操作的 tmux 会话名」与 stale。
// stale=true 表示请求所指的会话**确定**已不在解析出的 tmux 里(真值表证实易主)——
// 控制端点据此 409 拒发,绝不把键打进别的会话。任何环节缺数据 → (原样 name, false)
// (行为同旧逻辑,绝不更糟、不破坏自定义 tmuxName 的部署)。
//
// 它自己取一次 list-panes + 读一次真值表(+ 必要时扫一次会话),端点各自单次调用,廉价。
func resolveSessionTarget(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return name, false
	}
	panes := listTmuxPanes()
	own := ownershipFor(panes, loadPaneMap())
	// 名字已精确在跑 → 常态快路径(省一次扫盘);但先过易主检测。
	for _, p := range panes {
		if p.Name == name {
			return name, staleExactTarget(name, own)
		}
	}
	snaps, err := scanClaudeSessions()
	if err != nil || len(snaps) == 0 {
		return name, false
	}
	sid := sidForTmuxName(name, snaps)
	if sid == "" {
		return name, false // 非 cc- 名 / 查不到:不改绑
	}
	resolved := resolveTmuxName(sid, panes, snaps, own)
	// 双保险:解析落点被真值表认领给了别的会话(结构上不应发生——第 2 层已排除认领 pane,
	// 第 0 层命中则二者一致;留作防御,宁可拒发不可错发)。
	if ownerSid, ok := own.byName[resolved]; ok && ownerSid != sid {
		return resolved, true
	}
	return resolved, false
}

// liveSessionIDs 计算「哪些 session id 该显示 live」。
//
// 规则:每个在跑的 tmux 当前只跑**一个** Claude 会话(那个 pane 当下所处的 session)。
// 真值表认领的 pane,其当前会话就是登记的 sid(确定性);未认领的 pane 退回「解析到该 tmux
// 的会话里取最近活动者」的旧规则。
//
// 为什么要「同 tmux 取一个」:post-/clear 时旧 id X 仍有精确同名 tmux cc-X(/clear 不新建
// tmux,X 的名字残留),新 id Y 也解析到 cc-X。若只看「名字在跑」,X、Y 都会 live;但 X 已被
// /clear 取代——attach cc-X 看到的是 Y 的内容,X 实际不可达。故只把当前的 Y 标 live,X 标
// offline,前端据此不再给已死的 X 发送框/镜像。
func liveSessionIDs(panes []tmuxPane, snaps []claudeSnapshot, own paneOwnership) map[string]bool {
	liveNames := liveTmuxNames(panes)
	// 先把每个会话解析到的(在跑的)tmux 名记下,并按 tmux 名挑「最近活动」的会话。
	resolved := make(map[string]string, len(snaps)) // sid → tmux 名(仅在跑的才记)
	bestSid := map[string]string{}                  // tmux 名 → 当前会话 sid
	bestMs := map[string]int64{}
	snapSet := make(map[string]bool, len(snaps))
	for _, s := range snaps {
		snapSet[s.SessionID] = true
		name := resolveTmuxName(s.SessionID, panes, snaps, own)
		if !liveNames[name] {
			continue
		}
		resolved[s.SessionID] = name
		if ms := tsToMs(s.LastTs); ms >= bestMs[name] {
			bestMs[name] = ms
			bestSid[name] = s.SessionID
		}
	}
	// 真值表压倒 LastTs 比较:被认领 pane 的当前会话就是它登记的 sid(冻结已久的旧 id 即便
	// LastTs 更晚——如错发污染过的——也不再误标 live)。
	for name, sid := range own.byName {
		if snapSet[sid] {
			bestSid[name] = sid
		}
	}
	out := make(map[string]bool, len(snaps))
	for _, s := range snaps {
		name, ok := resolved[s.SessionID]
		out[s.SessionID] = ok && bestSid[name] == s.SessionID
	}
	return out
}
