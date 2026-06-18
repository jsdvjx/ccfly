package control

// takeover.go — 「接管」:把一个不在 tmux 里跑(或已无人控)的 claude 会话搬进 tmux,
// 先确定性地杀掉已有 claude 进程,杜绝同一 jsonl 双写。
//
// 双写病根:同一 session 同时存在两个 claude 进程(如 Ghostty 裸跑一份 + web 端
// `claude --resume` 又起一份),两边都 append 同一份 jsonl → 消息交错、状态互踩。
// 故接管的次序必须是:定位旧进程 → 杀死 → 才允许 /term 的 new-session -A 重建。
//
// 定位手段(只认确定性证据,fail-closed,与 panemap 同哲学;绝不按 cwd/最近活动「猜」):
//   1. <claudeDir>/sessions/<pid>.json —— Claude Code 自维护的 pid→sessionId 注册表
//      (含 procStart;须 pid 存活 + 进程启动时刻一致才采信,防 pid 复用误杀)。
//   2. 进程 argv 含完整 sid(uuid 无歧义,覆盖 `claude --resume <sid>` 启动的存量进程);
//      仅认 argv0 为 claude 的进程(排除 tmux 客户端等转述者)。
// 找不到进程但会话仍在活跃写入(AgeSec 小)→ 409 拒绝,绝不冒险。
//
// POST /takeover?session=<sid|cc-sid8> → {"sid":..., "killed":[pid...]}
// 成功后由前端连 /term:tmux 不存在 → new-session -A 自动新建并 claude --resume(见 term.go)。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// activeAgeSec:会话「最近仍在写」的判定阈值(秒)。进程定位失败 + 末次活动比这还新 → 拒绝接管。
const activeAgeSec = 120

// TakeoverResult 是 Takeover 的结果(端点与 CLI 共用)。
type TakeoverResult struct {
	Sid    string `json:"sid"`
	Killed []int  `json:"killed"`
}

// handleTakeover — POST /takeover?session=<sid|cc-sid8>。
func handleTakeover(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("session"))
	if q == "" {
		ctrlErr(w, 400, "session required")
		return
	}
	res, err := Takeover(q)
	if err != nil {
		var te *takeoverErr
		if ok := asTakeoverErr(err, &te); ok {
			ctrlErr(w, te.status, te.msg)
			return
		}
		ctrlErr(w, 500, err.Error())
		return
	}
	ctrlJSON(w, 200, res)
}

type takeoverErr struct {
	status int
	msg    string
}

func (e *takeoverErr) Error() string { return e.msg }

func asTakeoverErr(err error, out **takeoverErr) bool {
	te, ok := err.(*takeoverErr)
	if ok {
		*out = te
	}
	return ok
}

// Takeover 定位并终止 sid 的既有 claude 进程(CLI attach 与 /takeover 共用)。
// 入参可为完整 session id 或 cc-<sid8> 形式的 tmux 名。
func Takeover(sidOrName string) (TakeoverResult, error) {
	snaps, err := scanClaudeSessions()
	if err != nil {
		return TakeoverResult{}, err
	}
	sid := resolveSid(sidOrName, snaps)
	if sid == "" {
		return TakeoverResult{}, &takeoverErr{404, "unknown session: " + sidOrName}
	}

	// 已有活 pane 在控这个 sid(panemap/名字匹配判 live)→ 没什么可接管的;
	// 杀掉 pane 里的 claude 再重建反而破坏现场,拒绝。
	panes := listTmuxPanes()
	if liveSessionIDs(panes, snaps, ownershipFor(panes, loadPaneMap()))[sid] {
		return TakeoverResult{}, &takeoverErr{409, "session already has a live tmux pane"}
	}

	pids := locateClaudePids(sid)
	if len(pids) == 0 {
		// 没定位到进程:若 jsonl 仍在新鲜增长,说明有个找不到的写入者 → 接管=必双写,拒绝。
		for _, s := range snaps {
			if s.SessionID == sid && s.AgeSec >= 0 && s.AgeSec < activeAgeSec && s.State == "working" {
				return TakeoverResult{}, &takeoverErr{409,
					"session appears active but its process cannot be located; quit it manually first"}
			}
		}
		return TakeoverResult{Sid: sid, Killed: []int{}}, nil
	}

	killed, err := killWithGrace(pids, 3*time.Second, 2*time.Second)
	if err != nil {
		return TakeoverResult{Sid: sid, Killed: killed}, &takeoverErr{500, err.Error()}
	}
	return TakeoverResult{Sid: sid, Killed: killed}, nil
}

// resolveSid:完整 uuid 原样(须存在于快照);cc-<sid8> / 裸 sid 前缀 → 唯一匹配才采信。
func resolveSid(q string, snaps []claudeSnapshot) string {
	p := strings.TrimPrefix(q, "cc-")
	var hit string
	for _, s := range snaps {
		if s.SessionID == q {
			return q
		}
		if strings.HasPrefix(s.SessionID, p) {
			if hit != "" && hit != s.SessionID {
				return "" // 前缀撞车 → 不猜
			}
			hit = s.SessionID
		}
	}
	return hit
}

// locateClaudePids — 两路确定性定位,去重合并。
func locateClaudePids(sid string) []int {
	seen := map[int]bool{}
	var out []int
	for _, pid := range registryPids(sid) {
		if !seen[pid] {
			seen[pid] = true
			out = append(out, pid)
		}
	}
	for _, pid := range argvPids(sid) {
		if !seen[pid] {
			seen[pid] = true
			out = append(out, pid)
		}
	}
	return out
}

// registryPids 读 <claudeDir>/sessions/<pid>.json(与 projects 同级,随 --claude-dir 走),
// 匹配 sessionId 且 pid 存活且启动时刻一致(防 pid 复用误杀)。
//
// ⚠️ 时区:注册表的 procStart 字段是 **UTC** 渲染("Wed Jun 10 02:29:18 2026"),而 `ps lstart`
// 是 **本地** 渲染("…10:29:18…")—— 两者按同一时区解析比较会差出整个 UTC 偏移(此前 +08
// 环境下差 8h,校验永久失败,当前会话进程永远定位不到)。故基准改用 startedAt(epoch 毫秒,
// 无歧义),ps lstart 按本地解析成 epoch 再比,绕开时区字符串。容差放宽到 5s(startedAt 记录在
// fork 之后,可能比内核进程启动晚一两秒)。
func registryPids(sid string) []int {
	dir := filepath.Join(filepath.Dir(claudeProjectsDir()), "sessions")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var reg struct {
			Pid       int   `json:"pid"`
			SessionID string `json:"sessionId"`
			StartedAt int64  `json:"startedAt"` // epoch 毫秒(无歧义,优先用)
		}
		if json.Unmarshal(b, &reg) != nil || reg.SessionID != sid || reg.Pid <= 0 {
			continue
		}
		if syscall.Kill(reg.Pid, 0) != nil {
			continue // 进程已死:注册表残留
		}
		if reg.StartedAt > 0 {
			lstart, err := processLstart(reg.Pid) // ps lstart 原始串(本地渲染)
			if err != nil || !startMatches(reg.StartedAt, lstart) {
				continue // 启动时刻对不上 → pid 已被复用,绝不误杀
			}
		}
		out = append(out, reg.Pid)
	}
	return out
}

// argvPids 扫 ps:argv0 是 claude 且命令行含完整 sid(uuid,无歧义)。
func argvPids(sid string) []int {
	b, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	var out []int
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, sid) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 1 {
			continue
		}
		if filepath.Base(fields[1]) != "claude" {
			continue // tmux 客户端等转述者的 argv 也含 sid,不杀
		}
		out = append(out, pid)
	}
	return out
}

// processLstart 取进程的 ps lstart 原始串(本地时区渲染,如 "Wed Jun 10 10:29:18 2026")。
func processLstart(pid int) (string, error) {
	b, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// startMatches 判断 ps lstart(本地渲染串)与注册表 startedAt(epoch 毫秒)是否同一启动时刻(±5s)。
// ps lstart 按 **本地** 时区解析为绝对时刻,与 startedAt 这个无歧义 epoch 比 —— 故不受
// 注册表 procStart 字段是 UTC 渲染这件事影响(那正是早先 8h 偏差、定位永远失败的根因)。
// 纯函数,便于单测锁定时区行为。
func startMatches(startedAtMs int64, lstart string) bool {
	s := strings.Join(strings.Fields(lstart), " ") // 折叠 ps 对个位日期的双空格
	t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", s, time.Local)
	if err != nil {
		return false
	}
	return absDur(time.UnixMilli(startedAtMs).Sub(t)) <= 5*time.Second
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// killWithGrace — SIGTERM → 限时等退出 → 仍活则 SIGKILL → 再限时等。返回实际终结的 pid。
func killWithGrace(pids []int, term, kill time.Duration) ([]int, error) {
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	alive := waitGone(pids, term)
	if len(alive) > 0 {
		for _, pid := range alive {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		alive = waitGone(alive, kill)
	}
	var killed []int
	stillAlive := map[int]bool{}
	for _, pid := range alive {
		stillAlive[pid] = true
	}
	for _, pid := range pids {
		if !stillAlive[pid] {
			killed = append(killed, pid)
		}
	}
	if len(alive) > 0 {
		return killed, fmt.Errorf("process(es) survived SIGKILL: %v", alive)
	}
	return killed, nil
}

// waitGone 轮询直到全部退出或超时,返回仍存活的 pid。
func waitGone(pids []int, d time.Duration) []int {
	deadline := time.Now().Add(d)
	for {
		var alive []int
		for _, pid := range pids {
			if syscall.Kill(pid, 0) == nil {
				alive = append(alive, pid)
			}
		}
		if len(alive) == 0 || time.Now().After(deadline) {
			return alive
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ── CLI 复用的薄导出(避免 cmd/ccfly 触碰本包未导出实现;详见 cmd/ccfly/sessions_cli.go) ──

// CLISessionRow 是 ccfly list 的一行。
type CLISessionRow struct {
	Sid   string
	Live  bool
	State string
	Age   int64
	Cwd   string
	Title string
	// Tmux:该会话实际跑在的 tmux 会话名(panemap 真值表/解析结果),仅 live 时非空。
	// ccfly ls 据此给出可直接执行的 `tmux a -t <名>` 接管命令。
	Tmux string
}

// CLISessions 返回全部会话(按 live 优先、再按新近排序由调用方决定;此处保持扫描序)。
func CLISessions() ([]CLISessionRow, error) {
	snaps, err := scanClaudeSessions()
	if err != nil {
		return nil, err
	}
	panes := listTmuxPanes()
	own := ownershipFor(panes, loadPaneMap())
	live := liveSessionIDs(panes, snaps, own)
	running := make(map[string]bool, len(panes))
	for _, p := range panes {
		running[p.Name] = true
	}
	rows := make([]CLISessionRow, 0, len(snaps))
	for _, s := range snaps {
		tmux := ""
		if live[s.SessionID] {
			if name := resolveTmuxName(s.SessionID, panes, snaps, own); running[name] {
				tmux = name
			}
		}
		rows = append(rows, CLISessionRow{
			Sid: s.SessionID, Live: live[s.SessionID], State: s.State,
			Age: s.AgeSec, Cwd: s.Cwd, Title: s.Title, Tmux: tmux,
		})
	}
	return rows, nil
}

// CLIAttachArgs 给 ccfly attach:返回 tmux 参数(new-session -A,必要时注入 claude --resume)。
// 复用 /term 的 resume 注入逻辑(claudeResumeCmd),行为与 web 端完全一致。
// claudeArgs 是追加到 `claude --resume <sid>` 后的额外参数(如 --permission-mode plan /
// --dangerously-skip-permissions);仅在「离线 resume 重建」时生效——会话已 live 时 -A 只 attach、
// 忽略命令,claude 已在跑改不了权限模式。
func CLIAttachArgs(sid string, claudeArgs []string) []string {
	name := defaultTmuxName(sid)
	skip := false
	for _, a := range claudeArgs {
		if a == "--dangerously-skip-permissions" {
			skip = true
		}
	}
	// 代理环境注入(CCFLY_TMUX_PROXY 配了才有):新建会话默认带好代理 + 局域网 bypass。
	args := append([]string{"-u", "new-session"}, tmuxProxyEnvArgs()...)
	args = append(args, sandboxEnvArgs(skip)...) // root + skip-permissions → IS_SANDBOX=1 放行
	args = append(args, "-A", "-s", name)
	if snaps, err := scanClaudeSessions(); err == nil {
		if cmd, cwd, ok := claudeResumeCmd(name, snaps); ok {
			if len(claudeArgs) > 0 {
				cmd += " " + strings.Join(claudeArgs, " ")
			}
			if cwd != "" {
				args = append(args, "-c", cwd)
			}
			args = append(args, cmd)
		}
	}
	return append(args, tmuxTitleArgs(name)...)
}

// tmuxTitleArgs 返回追加到 new-session 命令串后的 tmux 子命令(`;` 分隔),让本会话把
// 外层终端标题设成会话名(claude 设了 pane 标题时再缀上 ` · <标题>`)。没有它,多个
// `ccfly a` 窗口的标题全是 tmux 默认值,根本分不清哪个窗口跑的哪个会话。
//
//   - 作用域限定到本会话(set-option -t name,非 -g):不动用户自己别处的 tmux 标题。
//   - set-titles on 后 tmux 接管外层标题;claude 自身的 OSC 标题落到 #{pane_title}。
//   - pane_title 默认等于 #{host}(纯主机名),此时只显示 #S 不缀主机名,避免噪音;
//     程序(claude)设了真标题才显示 `#S · <标题>`。
//   - 以 exec 直接调 tmux,`;` 作为独立实参即被识别为命令分隔符,无需 shell 转义。
func tmuxTitleArgs(name string) []string {
	const titleFmt = "#{?#{==:#{pane_title},#{host}},#S,#S · #{pane_title}}"
	return []string{
		";", "set-option", "-t", name, "set-titles", "on",
		";", "set-option", "-t", name, "set-titles-string", titleFmt,
	}
}

// TmuxTitleArgs 导出 tmuxTitleArgs 供 cmd 层的「全新会话」路径(ccfly new)复用,
// 与 attach 路径走同一套标题口径。
func TmuxTitleArgs(name string) []string { return tmuxTitleArgs(name) }
