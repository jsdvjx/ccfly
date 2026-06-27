package control

// attn.go — 会话「待办类型」(attn_kind)派生:把活跃会话当前屏幕的阻塞框分类成
// permission / plan / choice,供落地页 /sessions 与云端 syncer 标注「这个会话在等你」。
//
// 为什么需要它(而不是复用 jsonl 的 state):权限/选择框是 claude TUI 的纯终端控件,**不写
// jsonl**——claudescan 的 classify() 只看 jsonl,故权限确认时它只会报 working/awaiting_input,
// 永远分不出「在等你点确认」。唯一权威是屏幕(detectState),所以这里对活 pane 抓屏判定。
//
// 范围(Phase A,刻意收窄):只认 Kind=="select" 的阻塞框(permission/plan/choice)——这是
// jsonl 拿不到、且用户明确要的「权限需要确认」一类。其余态(思考中=busy、回合结束等输入=
// awaiting_input/idle)已由 classify() 覆盖,不在此重复。Kind=="input" 的「空闲输入框」即
// 「该你了」,与 awaiting_input 同义,Phase A 不另立 attn_kind(留给 Phase B 的 hook 细分)。
//
// 成本:抓屏是每 pane 一次 tmux 子进程(亚 100ms)。本文件用 2s memo 把「5s 轮询 /sessions +
// 20s syncer」多端调用收敛成至多每 2s 抓一轮,且只抓 live 会话(无 pane 的会话无屏可抓)。
//
// 局限(Phase A 已知,Phase B 修):纯屏幕轮询分不清「claude 主动请求权限」与「用户自己开的
// /model、/config 菜单」——后者会被暂时标成 choice。Phase A 仅用于角标/排序(不推送),误标
// 无害且短暂;Phase B 接 Notification hook(notification_type=permission_prompt 才触发)后,
// 由 hook 当闸门,从根本上不再把用户主动开的菜单当成「在等你」。

import (
	"os/exec"
	"strings"
	"sync"
	"time"
)

// attn_kind 取值(空串=无待办):
//   permission — 工具/命令授权确认框(Do you want to proceed? / 含「don't ask again」)
//   plan       — 计划待批准(ExitPlanMode:含「keep planning」/「auto-accept edits」)
//   choice     — 其余编号选择框(AskUserQuestion 等)
const (
	attnPermission = "permission"
	attnPlan       = "plan"
	attnChoice     = "choice"
)

// attnKindFromState 把一帧屏幕状态分类成 attn_kind。只对 Kind=="select" 产出,其余返回 ""。
// 判定优先级:plan > permission > choice(plan 的选项里也常有「No, keep planning」这类否定项,
// 必须先于 permission 命中,否则会被 permission 的「否定项」启发式误吞)。
func attnKindFromState(st ctrlState) string {
	if st.Kind != "select" {
		return ""
	}
	title := strings.ToLower(st.Title)
	var labels []string
	for _, o := range st.Options {
		labels = append(labels, strings.ToLower(o.Label))
	}
	anyLabel := func(subs ...string) bool {
		for _, l := range labels {
			for _, sub := range subs {
				if strings.Contains(l, sub) {
					return true
				}
			}
		}
		return false
	}

	// plan:ExitPlanMode 的确认框。选项稳定含「auto-accept edits」/「keep planning」。
	if anyLabel("auto-accept edits", "keep planning") {
		return attnPlan
	}
	// permission:工具/命令授权框。标题问句 或 选项含「don't ask again」(权限框独有的口径)。
	if strings.Contains(title, "do you want") ||
		strings.Contains(title, "wants to") ||
		strings.Contains(title, "permission") ||
		strings.Contains(title, "proceed") ||
		strings.Contains(title, "claude needs") ||
		anyLabel("don't ask again", "don’t ask again", "yes, and don't ask", "yes, and don’t ask") {
		return attnPermission
	}
	// 其余编号选择框(AskUserQuestion 等)。
	return attnChoice
}

// ── attn_kind 派生(对活 pane 抓屏)+ 2s memo ───────────────────────────────────
var (
	attnMu   sync.Mutex
	attnMemo map[string]string
	attnAt   time.Time
)

// attnTTL:memo 有效期。略小于云端 syncer 的 20s、远小于会话「等你确认」的真实停留时长,
// 既把 5s 轮询 + 20s syncer 的多端调用收敛成至多每 2s 一轮抓屏,又不放陈旧。
const attnTTL = 2 * time.Second

// AttnKinds 返回 sid → attn_kind(只含非空项;无待办/离线会话不出现)。memoized。
// 自包含(自己 list-panes / 扫会话),供 syncer 直接调用;落地页 handleSessions 亦复用此 memo。
func AttnKinds() map[string]string {
	attnMu.Lock()
	if attnMemo != nil && time.Since(attnAt) < attnTTL {
		out := copyStrMap(attnMemo)
		attnMu.Unlock()
		return out
	}
	attnMu.Unlock()

	snaps, err := scanClaudeSessions() // 走 claudescan 的 800ms memo,廉价
	if err != nil {
		return map[string]string{}
	}
	panes := listTmuxPanes()
	own := ownershipFor(panes, loadPaneMap())
	live := liveSessionIDs(panes, snaps, own)
	byName := make(map[string]tmuxPane, len(panes))
	for _, p := range panes {
		byName[p.Name] = p
	}

	out := map[string]string{}
	for _, s := range snaps {
		if !live[s.SessionID] {
			continue // 无 live pane 的会话:无屏可抓,无待办可言
		}
		name := resolveTmuxName(s.SessionID, panes, snaps, own)
		if _, ok := byName[name]; !ok {
			continue
		}
		raw, err := captureScreen(name)
		if err != nil {
			continue // 抓屏失败(pane 刚销毁等):跳过,本会话本轮无 attn_kind
		}
		if k := attnKindFromState(detectState(raw)); k != "" {
			out[s.SessionID] = k
		}
	}

	attnMu.Lock()
	attnMemo, attnAt = out, time.Now()
	attnMu.Unlock()
	return copyStrMap(out)
}

// captureScreen 抓「当前可见屏」(无 -S,与 handleState 同口径;-e 保留 ANSI 供 detectState)。
func captureScreen(name string) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-t", name, "-p", "-e").CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func copyStrMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
