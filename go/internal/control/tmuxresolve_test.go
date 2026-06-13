package control

// tmuxresolve_test.go — /clear 改绑场景的回归。
//
// 复现 bug:一个 claude 进程在一个 tmux pane(cc-<X[:8]>)里跑,用户 /clear,claude 切到新
// session id Y(同一 cwd);tmux 名仍是 cc-<X[:8]>(/clear 不新建 tmux)。期望:
//   - Y(当前会话)解析到 cc-<X[:8]> 并 live=true;
//   - X(已被取代)解析到 cc-<X[:8]> 但 live=false(attach 它看到的是 Y,X 已不可达);
//   - 无 /clear 的常态(精确同名在跑)零改动:照旧 live、照旧用本名。
//
// 纯函数注入 panes/snaps(不依赖真实 tmux),稳定可在 CI 跑。

import "testing"

// 8 位前缀即 cc- 名所取的部分;后缀任意,凑成合法 uuid 形态。
const (
	sidOld = "aaaaaaaa-1111-2222-3333-444444444444" // /clear 前
	sidNew = "bbbbbbbb-5555-6666-7777-888888888888" // /clear 后(更晚活动)
	sidLoc = "cccccccc-9999-0000-1111-222222222222" // 另一个 cwd 的常态会话
)

func snap(sid, cwd, lastTs string) claudeSnapshot {
	return claudeSnapshot{SessionID: sid, Cwd: cwd, LastTs: lastTs}
}

func clausePane(name, cwd, curCmd string) tmuxPane {
	return tmuxPane{Name: name, Cwd: cwd, CurCmd: curCmd}
}

// noOwn:无真值表(hook 未覆盖)的空 ownership——存量行为的回归用例都用它。
func noOwn() paneOwnership { return ownershipFor(nil, nil) }

// TestResolveClearRebinding 核心:/clear 后新 id 解析到陈旧名的真 pane,旧 id 不再 live。
func TestResolveClearRebinding(t *testing.T) {
	cwd := "/Users/u/proj"
	snaps := []claudeSnapshot{
		snap(sidOld, cwd, "2026-06-06T10:00:00Z"),
		snap(sidNew, cwd, "2026-06-06T10:05:00Z"), // 更晚 = 当前
	}
	// 真实 /clear 现场:只有「旧名」pane 在跑 claude;新名 cc-bbbbbbbb 并不存在。
	panes := []tmuxPane{
		clausePane(defaultTmuxName(sidOld), cwd, "2.1.167"), // cc-aaaaaaaa,前台 claude
	}

	// 新 id 应解析到陈旧名的真 pane(否则 attach 会新开孤儿)。
	if got := resolveTmuxName(sidNew, panes, snaps, noOwn()); got != defaultTmuxName(sidOld) {
		t.Fatalf("/clear 后新 id 应解析到旧名真 pane %q,得 %q", defaultTmuxName(sidOld), got)
	}

	live := liveSessionIDs(panes, snaps, noOwn())
	if !live[sidNew] {
		t.Fatalf("当前(新)会话应 live=true")
	}
	if live[sidOld] {
		t.Fatalf("被 /clear 取代的旧会话应 live=false(它的名字虽在跑,但跑的是新会话)")
	}
}

// TestResolveNormalNoClear 常态:精确同名在跑 → 用本名,live=true,零改动。
func TestResolveNormalNoClear(t *testing.T) {
	cwd := "/Users/u/other"
	snaps := []claudeSnapshot{snap(sidLoc, cwd, "2026-06-06T11:00:00Z")}
	panes := []tmuxPane{clausePane(defaultTmuxName(sidLoc), cwd, "2.1.167")}

	if got := resolveTmuxName(sidLoc, panes, snaps, noOwn()); got != defaultTmuxName(sidLoc) {
		t.Fatalf("常态应用本名 %q,得 %q", defaultTmuxName(sidLoc), got)
	}
	if live := liveSessionIDs(panes, snaps, noOwn()); !live[sidLoc] {
		t.Fatalf("常态精确同名在跑应 live=true")
	}
}

// TestResolveOfflineNoPane 离线:无任何 pane → 回落本名、live=false(不误判、不改绑)。
func TestResolveOfflineNoPane(t *testing.T) {
	cwd := "/Users/u/dead"
	snaps := []claudeSnapshot{snap(sidOld, cwd, "2026-06-06T09:00:00Z")}
	var panes []tmuxPane // tmux 全无

	if got := resolveTmuxName(sidOld, panes, snaps, noOwn()); got != defaultTmuxName(sidOld) {
		t.Fatalf("无 pane 应回落本名 %q,得 %q", defaultTmuxName(sidOld), got)
	}
	if live := liveSessionIDs(panes, snaps, noOwn()); live[sidOld] {
		t.Fatalf("无 pane 的会话应 live=false")
	}
}

// TestResolveStaleNotCurrent 旧 id 即便其陈旧名 pane 在跑,自己也不该被改绑去蹭(它不是当前会话)。
func TestResolveStaleNotCurrent(t *testing.T) {
	cwd := "/Users/u/proj"
	snaps := []claudeSnapshot{
		snap(sidOld, cwd, "2026-06-06T10:00:00Z"),
		snap(sidNew, cwd, "2026-06-06T10:05:00Z"),
	}
	// 此处旧名恰好不在跑(模拟旧 pane 已被 kill、claude 由别的机制继续);新名也不在跑。
	// sidOld 不是当前会话(sidNew 更晚)→ resolveTmuxName 不应把它绑到任何在跑 pane。
	panes := []tmuxPane{clausePane("cc-zzzzzzzz", cwd, "2.1.167")} // 另一无关名的 pane 在该 cwd 跑

	// sidOld 不是 cwd 的当前会话 → 保持本名(不蹭 cc-zzzzzzzz)。
	if got := resolveTmuxName(sidOld, panes, snaps, noOwn()); got != defaultTmuxName(sidOld) {
		t.Fatalf("非当前会话不应改绑,应回落本名 %q,得 %q", defaultTmuxName(sidOld), got)
	}
	// sidNew 是当前会话 → 解析到该 cwd 在跑 claude 的 pane(cc-zzzzzzzz)。
	if got := resolveTmuxName(sidNew, panes, snaps, noOwn()); got != "cc-zzzzzzzz" {
		t.Fatalf("当前会话应解析到 cwd 内在跑 claude 的 pane cc-zzzzzzzz,得 %q", got)
	}
	live := liveSessionIDs(panes, snaps, noOwn())
	if live[sidOld] || !live[sidNew] {
		t.Fatalf("应仅当前会话 live:得 old=%v new=%v", live[sidOld], live[sidNew])
	}
}

// TestSidForTmuxName 反查:cc-<前8位> → sid;非 cc- 名 / 查不到 → ""。
func TestSidForTmuxName(t *testing.T) {
	snaps := []claudeSnapshot{snap(sidNew, "/c", "2026-06-06T10:00:00Z")}
	if got := sidForTmuxName(defaultTmuxName(sidNew), snaps); got != sidNew {
		t.Fatalf("应反查到 %q,得 %q", sidNew, got)
	}
	if got := sidForTmuxName("demo", snaps); got != "" {
		t.Fatalf("非 cc- 名应返回空,得 %q", got)
	}
	if got := sidForTmuxName("cc-deadbeef", snaps); got != "" {
		t.Fatalf("查不到应返回空,得 %q", got)
	}
}

// TestLiveTwoCwdIndependent 多 cwd 互不串台:各自的当前会话 live,旧会话不 live。
func TestLiveTwoCwdIndependent(t *testing.T) {
	snaps := []claudeSnapshot{
		snap(sidOld, "/a", "2026-06-06T10:00:00Z"),
		snap(sidNew, "/a", "2026-06-06T10:05:00Z"), // /a 的当前
		snap(sidLoc, "/b", "2026-06-06T11:00:00Z"), // /b 的常态(精确同名)
	}
	panes := []tmuxPane{
		clausePane(defaultTmuxName(sidOld), "/a", "2.1.167"), // /a:陈旧名 pane
		clausePane(defaultTmuxName(sidLoc), "/b", "2.1.167"), // /b:精确同名 pane
	}
	live := liveSessionIDs(panes, snaps, noOwn())
	if live[sidOld] {
		t.Fatalf("/a 旧会话不应 live")
	}
	if !live[sidNew] {
		t.Fatalf("/a 当前会话应 live")
	}
	if !live[sidLoc] {
		t.Fatalf("/b 常态会话应 live")
	}
}

// ── 真样(read-only,非破坏):跑在本机默认 tmux socket + 真实 ~/.claude;无 tmux/会话则 Skip。

// TestListTmuxPanesRealParses 验证 listTmuxPanes 能解析真实 tmux 输出(字段非空、名字唯一)。
func TestListTmuxPanesRealParses(t *testing.T) {
	panes := listTmuxPanes()
	if len(panes) == 0 {
		t.Skip("本机无 tmux 会话,跳过真样解析")
	}
	seen := map[string]bool{}
	for _, p := range panes {
		if p.Name == "" {
			t.Fatalf("解析出空会话名:%+v", p)
		}
		if seen[p.Name] {
			t.Fatalf("会话名重复(应每会话一行):%q", p.Name)
		}
		seen[p.Name] = true
	}
	t.Logf("解析到 %d 个 tmux 会话", len(panes))
}

// TestLiveSessionIDsRealConsistent 真样不变量:liveSessionIDs 不 panic;每个**在跑的 tmux**
// 至多一个 live 会话(同一 pane 的「当前会话」唯一)。注意:不能按 cwd 断言唯一——同一 cwd
// 完全可能有多个分别命名的 live tmux(如本机就有 cc-bdf9a1cf 与 cc-4d7f28b3 同在 ccfly-cloud),
// 它们各自精确同名、各自可 attach,都该 live;真正的唯一性约束在「每个 tmux 名」上。
func TestLiveSessionIDsRealConsistent(t *testing.T) {
	snaps, err := scanClaudeSessions()
	if err != nil || len(snaps) == 0 {
		t.Skip("本机无 claude 会话,跳过")
	}
	panes := listTmuxPanes()
	own := ownershipFor(panes, loadPaneMap()) // 真样连真值表一起跑(可能为空,同样合法)
	live := liveSessionIDs(panes, snaps, own)
	liveByTmux := map[string]int{}
	for _, s := range snaps {
		if live[s.SessionID] {
			liveByTmux[resolveTmuxName(s.SessionID, panes, snaps, own)]++
		}
	}
	for name, n := range liveByTmux {
		if n > 1 {
			t.Fatalf("tmux %q 有 %d 个 live 会话(应至多 1:同 pane 的当前会话唯一)", name, n)
		}
	}
	t.Logf("snaps=%d panes=%d live-tmux=%d", len(snaps), len(panes), len(liveByTmux))
}

// TestStatusLineCount status 选项值 → 状态栏行数(客户端视口 = 窗口 + 状态栏)。
func TestStatusLineCount(t *testing.T) {
	cases := map[string]int{"off": 0, "0": 0, "": 0, "on": 1, "1": 1, "2": 2, "5": 5, "weird": 1}
	for in, want := range cases {
		if got := statusLineCount(in); got != want {
			t.Fatalf("statusLineCount(%q)=%d, want %d", in, got, want)
		}
	}
}
