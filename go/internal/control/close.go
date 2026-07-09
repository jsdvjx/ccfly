package control

// close.go — POST /close:主动「关闭」(停掉)一批指定会话的常驻 tmux 会话,释放 CPU/内存。
//
// 动机:低配机被多个大会话拖卡的根因 = 每开一个会话就在 tmux 里留一个永不退的 `claude --resume`
// 常驻进程(堆几十个吃满 CPU/内存逼 swap 抖动 → 整机卡)。本端点让前端「一键关掉除保留外的全部」。
//
// 与既有操作语义严格区分(别混淆):
//   - /reload   杀 tmux 再 claude --resume **重建** —— 又把进程拉起来了,不释放资源。
//   - /takeover 杀会话既有 claude 进程以**交出控制权**(随后 /term 重建),不是为了释放资源。
//   - 云端 DELETE /api/sessions/{id} 只**软删云端记录**(列表隐藏、归档保留),根本不碰设备进程。
//   - /close(本端点)只 `tmux kill-session`:claude 进程随之退出、CPU/内存立即释放,
//     **对话历史 jsonl 原样保留**,之后仍可 `ccfly a <sid>` / resume。这才是「关闭以释放资源」。
//
// 语义:前端传来【明确要关的 sid 列表】(= 该设备运行中列表 − 用户勾选保留的)。发「显式要关的清单」
// 而非「保留清单」是刻意的:弹窗打开后新冒出来的会话不在清单里 → 天然幸免,只关用户亲眼看到并选中的
// (杜绝「保留清单」下的竞态误杀)。后端逐个:
//   - 非运行中(该 sid 不是任何在跑 tmux 的当前占用者)→ 跳过,reason=not_live(无进程可关,幂等)。
//     用 liveSessionIDs 判定,与 /sessions 的 live 同口径:扛 /clear(旧 sid 名字残留但已易主的,
//     判为非 live → 不会误杀现在占用该 pane 的新会话)。
//   - force=false 时的守卫(防误关「正在用/正在干活/等你决策」的会话,丢活/丢待决):
//       · attached>0(有客户端在连)          → 跳过 reason=attached
//       · busy(屏幕 "esc to interrupt" 生成中)→ 跳过 reason=generating
//       · select(权限/计划/选择菜单,等你操作)→ 跳过 reason=awaiting_input
//   - 否则 `tmux kill-session -t =<name>`(="+name 精确名匹配,杜绝前缀误杀 cc-a→cc-ab;同
//     scanner.go reap 口径),记入 closed;kill 报错记 reason=kill_failed。
// force=true 跳过上述三道守卫(用户在回执看到「N 个正在运行,已跳过」后点「强制关闭」再重发)。
//
// 安全模型同本服务其它端点:自身不鉴权、默认绑回环,远端暴露交云端网关(requireAuth + 设备归属)把关。

import (
	"encoding/json"
	"net/http"
	"strings"
)

// closeReq 是 POST /close 的请求体。
type closeReq struct {
	Sessions []string `json:"sessions"` // 要关闭的会话 id(full sid)列表
	Force    bool     `json:"force"`    // true=跳过 attached/busy/select 守卫(强制关)
}

// closeSkip 是回执里「被跳过」的一条(未关闭)及其原因。
type closeSkip struct {
	Sid    string `json:"sid"`
	Reason string `json:"reason"` // not_live | attached | generating | awaiting_input | kill_failed
}

// handleClose — POST /close {sessions:[sid...], force?}:关闭(kill tmux)指定会话释放资源,留 jsonl。
// 回执 {closed:[sid...], skipped:[{sid,reason}...]}:前端据此提示「已关 X 个,跳过 Y 个(附原因)」,
// 并可对 generating/attached/awaiting_input 的那批给「强制关闭」二次入口(force:true 重发)。
func handleClose(w http.ResponseWriter, r *http.Request) {
	var req closeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ctrlErr(w, 400, "bad json")
		return
	}
	if len(req.Sessions) == 0 {
		ctrlErr(w, 400, "sessions required")
		return
	}

	// 一次性取快照 + pane + 真值表,复用 handleSessions 的解析口径(避免每会话重复 fork/扫盘)。
	snaps, err := scanClaudeSessions()
	if err != nil {
		ctrlErr(w, 500, err.Error())
		return
	}
	panes := listTmuxPanes()
	own := ownershipFor(panes, loadPaneMap())
	live := liveSessionIDs(panes, snaps, own) // sid → 是否为其 tmux 的当前占用者(扛 /clear)
	byName := make(map[string]tmuxPane, len(panes))
	for _, p := range panes {
		byName[p.Name] = p
	}

	closed := []string{}
	skipped := []closeSkip{}
	seen := map[string]bool{} // 去重:同一 sid 传两次只处理一次(不重复 kill/报)
	for _, sid := range req.Sessions {
		sid = strings.TrimSpace(sid)
		if sid == "" || seen[sid] {
			continue
		}
		seen[sid] = true

		// 非当前占用者(离线 / 被 /clear 取代)→ 没有属于它的活进程可关。幂等跳过,绝不误杀 pane 的现主。
		if !live[sid] {
			skipped = append(skipped, closeSkip{Sid: sid, Reason: "not_live"})
			continue
		}
		name := resolveTmuxName(sid, panes, snaps, own)
		p, ok := byName[name]
		if !ok { // 结构上不应发生(live[sid] 已保证解析到在跑 pane);防御性跳过。
			skipped = append(skipped, closeSkip{Sid: sid, Reason: "not_live"})
			continue
		}
		if !req.Force {
			if reason := closeGuardReason(name, p.Attached); reason != "" {
				skipped = append(skipped, closeSkip{Sid: sid, Reason: reason})
				continue
			}
		}
		// "="+name 精确名匹配,杜绝前缀误杀(cc-a 误中 cc-ab);同 scanner.go:70 reap 口径。
		if e := tmuxCmd("kill-session", "-t", "="+name).Run(); e != nil {
			skipped = append(skipped, closeSkip{Sid: sid, Reason: "kill_failed"})
			continue
		}
		closed = append(closed, sid)
	}
	ctrlJSON(w, 200, map[string]any{"closed": closed, "skipped": skipped})
}

// closeGuardReason 返回 force=false 时应跳过关闭该在跑会话的原因(""=可关)。
// attached 直接由 pane 字段判(免抓屏);busy/select 抓一次实时屏跑 detectState 判(与 /state 同口径)。
// 抓屏失败(pane 恰好没了)→ 不拦,交由 kill-session 幂等处理(它对不存在的会话也只是报错→kill_failed)。
func closeGuardReason(name string, attached int) string {
	if attached > 0 {
		return "attached" // 有客户端在连 = 有人正看/正用,别关
	}
	out, err := tmuxCmd("capture-pane", "-t", name, "-p", "-e").CombinedOutput()
	if err != nil {
		return ""
	}
	switch detectState(string(out)).Kind {
	case "busy":
		return "generating" // 正在生成,关了丢活
	case "select":
		return "awaiting_input" // 停在权限/计划/选择菜单等你操作,关了丢待决(这类状态不落 jsonl,resume 拿不回)
	}
	return ""
}
