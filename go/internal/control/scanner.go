package control

// scanner.go — 后台巡检 goroutine(由 Serve 或 connect 进程内路径启动,随 ctx 结束而停)。
//
// 两件事:
//  (1) REAP:回收 cc-* 命名空间里「已掉回 shell」的孤儿会话(claude 退出/从未起)。层层保守闸,
//      绝不碰非 cc- 会话、绝不杀刚建的/有人 attach 的/在跑 claude 的/以 claude 启动的会话。
//  (2) 预热:每拍调一次 scanClaudeSessions()(走缓存),既消费又刷新 memo,让轮询端点骑热缓存。
//
// 巡检也顺手复用 liveSessionIDs 保持 tmux⇄当前sid 口径一致,但**不**改 /sessions 的内联计算
// (后者经 Goal A 已廉价);本扫描器只负责 REAP + 暖缓存,是最小风险选择。

import (
	"context"
	"strings"
	"time"
)

const (
	scanInterval = 25 * time.Second // 20~30s 区间:够快回收、又不抖动
	reapGraceSec = 60               // 新建 <60s 会话豁免(可能 /term/start 刚建、claude 正在起)
	reapStrikes  = 2                // 连续 N 次「无 claude」才真杀(抗 /clear 重启等瞬时误判)
)

// missStreak 只被 RunScanner 这单一 goroutine 读写 → 无需加锁。
var missStreak = map[string]int{}

// RunScanner 周期性巡检 + 暖缓存,随 ctx 结束而停。每进程起一个:
// `ccfly serve` 经 Serve 起;`ccfly connect`(默认/生产路径,进程内跑 Handler())经 main 起。
func RunScanner(ctx context.Context) {
	t := time.NewTicker(scanInterval)
	defer t.Stop()
	scanOnce(ctx) // 起步即跑一拍:预热缓存,别等满一个 tick
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			scanOnce(ctx)
		}
	}
}

func scanOnce(ctx context.Context) {
	// 每拍重申 pane↔sid 真值表的 SessionStart hook(自愈,常态零写入,见 InstallSessionHook):
	// 临时/测试实例可能把 hook 命令改写成自己的路径,其二进制被删后每次 SessionStart 都会报错
	// (错误块注入 TUI、panemap 停更)——长命的 serve/connect 进程在此把它修回来,≤1 拍收敛。
	InstallSessionHook()
	// 先做慢操作(扫盘,可能数百 ms),panes 放到 reap 前最后一刻取 —— 缩小 TOCTOU 窗口:
	// 否则 reap 据「扫盘前」的陈旧 panes 判定,期间 /term 刚拉起 claude 的会话会被误杀。
	_, _ = scanClaudeSessions() // 走 Goal A 缓存:预热 + 刷新 memo,轮询端点受益
	panes := listTmuxPanes()
	reapOrphans(ctx, panes)
}

// reapOrphans 回收 cc-* 孤儿壳。受保护的清账;其余 claude-less 候选累计 strike,够数才杀。
func reapOrphans(ctx context.Context, panes []tmuxPane) {
	now := time.Now().Unix()
	alive := make(map[string]bool, len(panes))
	for _, p := range panes {
		alive[p.Name] = true
		if reapProtected(p, now) {
			delete(missStreak, p.Name) // 确定不该杀 → 清空 strike
			continue
		}
		// 候选孤儿:cc-* + 前台非 claude + 无 attach + 过宽限 + 非 claude 启动。累计 strike,够数才杀。
		missStreak[p.Name]++
		if missStreak[p.Name] >= reapStrikes {
			// "="+name 精确名匹配,杜绝前缀误杀(如 cc-a 误中 cc-ab);kill 受 ctx 约束,关服即取消。
			_ = tmuxCmd("kill-session", "-t", "="+p.Name).Run() // 统一走 tmuxCmd:环境注入/进程属性一致(盲区#2)
			delete(missStreak, p.Name)
		}
	}
	// GC:已消失的会话清掉 strike,防 map 泄漏。
	for name := range missStreak {
		if !alive[name] {
			delete(missStreak, name)
		}
	}
}

// reapProtected 判定一个会话是否「确定不该回收」(单一事实来源,reapOrphans 与 shouldReap 共用)。
func reapProtected(p tmuxPane, now int64) bool {
	switch {
	case !strings.HasPrefix(p.Name, "cc-"): // 闸0:绝不碰非 ccfly 命名空间
		return true
	case paneRunsClaude(p): // 闸1:在跑 claude → 永不杀
		return true
	case p.Attached > 0: // 闸2:有客户端 attach → 有人在用,不杀
		return true
	case strings.Contains(p.StartCmd, "claude"): // 闸3:以 claude 启动(可能临时 shell 出长命令)→ 不杀
		return true
	case p.Created == 0 || now-p.Created < reapGraceSec: // 闸4:新建宽限内 / 创建时刻未知(失败安全)→ 不杀
		return true
	default:
		return false
	}
}

// shouldReap 是纯判定(便于单测):非保护态 + 连续(含本拍)达 reapStrikes 拍 claude-less。
// strike 由调用方维护;strike+1>=reapStrikes 表示「含本拍已连续够数」。
func shouldReap(p tmuxPane, now int64, strike int) bool {
	if reapProtected(p, now) {
		return false
	}
	return strike+1 >= reapStrikes
}
