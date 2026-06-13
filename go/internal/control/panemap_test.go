package control

// panemap_test.go — pane↔sid 真值表与 fail-closed 解析的回归。
//
// 核心场景(本仓库曾经的头号恶性 bug):同一 cwd 多个 pane 同时跑 claude 时,旧启发式取
// list 顺序第一个 → webui 消息打进**别的会话**。现在:
//   - 无真值表 → 不猜(回落本名,fail-closed);
//   - 有真值表 → 确定性解析到登记的 pane,且 stale 检测关死「名字残留」的反向错发。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ownPane 构造带 PaneID 的 pane(真值表用例)。
func ownPane(id, name, cwd, curCmd string) tmuxPane {
	return tmuxPane{PaneID: id, Name: name, Cwd: cwd, CurCmd: curCmd}
}

// TestFailClosedMultiPaneSameCwd 报告 bug 的回归:同 cwd 两个 pane 都在跑 claude、目标 sid
// 无精确同名 pane 时,**绝不**改绑到任何一个(旧逻辑取第一个 = 抽奖错发);live 也不亮。
func TestFailClosedMultiPaneSameCwd(t *testing.T) {
	cwd := "/Users/u/proj"
	snaps := []claudeSnapshot{
		snap(sidOld, cwd, "2026-06-06T10:00:00Z"),
		snap(sidNew, cwd, "2026-06-06T10:05:00Z"), // cwd 最新,但有两个候选 pane → 不可判
	}
	panes := []tmuxPane{
		clausePane("cc-zzzzzzzz", cwd, "2.1.167"),
		clausePane("cc-yyyyyyyy", cwd, "2.1.167"),
	}
	if got := resolveTmuxName(sidNew, panes, snaps, noOwn()); got != defaultTmuxName(sidNew) {
		t.Fatalf("同 cwd 多候选 pane 必须 fail-closed 回落本名 %q,得 %q(错发根源!)", defaultTmuxName(sidNew), got)
	}
	if live := liveSessionIDs(panes, snaps, noOwn()); live[sidNew] || live[sidOld] {
		t.Fatalf("不可判时不应标 live:old=%v new=%v", live[sidOld], live[sidNew])
	}
}

// TestOwnershipResolvesForkedPane 真值表确定性:同 cwd 两个 pane,hook 登记 pane %1(名
// cc-aaaaaaaa)当前跑 sidNew → sidNew 解析到 cc-aaaaaaaa(不再受 list 顺序/LastTs 摆布);
// 旧 id sidOld(名字残留的那个)不再 live;另一 pane 的会话不受影响。
func TestOwnershipResolvesForkedPane(t *testing.T) {
	cwd := "/Users/u/proj"
	snaps := []claudeSnapshot{
		snap(sidOld, cwd, "2026-06-06T10:00:00Z"), // pane cc-aaaaaaaa 的初始会话,已被取代
		snap(sidNew, cwd, "2026-06-06T10:05:00Z"), // pane cc-aaaaaaaa 的当前会话(hook 登记)
		snap(sidLoc, cwd, "2026-06-06T11:59:00Z"), // 另一 pane 的常态会话(更活跃也不该抢)
	}
	panes := []tmuxPane{
		ownPane("%2", defaultTmuxName(sidLoc), cwd, "2.1.167"),
		ownPane("%1", defaultTmuxName(sidOld), cwd, "2.1.167"),
	}
	pmap := map[string]paneMapEntry{
		"%1": {Sid: sidNew, Name: defaultTmuxName(sidOld)},
		"%2": {Sid: sidLoc, Name: defaultTmuxName(sidLoc)},
	}
	own := ownershipFor(panes, pmap)

	if got := resolveTmuxName(sidNew, panes, snaps, own); got != defaultTmuxName(sidOld) {
		t.Fatalf("真值表应把 sidNew 解析到登记 pane %q,得 %q", defaultTmuxName(sidOld), got)
	}
	if got := resolveTmuxName(sidLoc, panes, snaps, own); got != defaultTmuxName(sidLoc) {
		t.Fatalf("sidLoc 应解析到自己的 pane,得 %q", got)
	}
	live := liveSessionIDs(panes, snaps, own)
	if !live[sidNew] || !live[sidLoc] {
		t.Fatalf("当前会话应 live:new=%v loc=%v", live[sidNew], live[sidLoc])
	}
	if live[sidOld] {
		t.Fatalf("名字残留的旧会话不应 live(pane 已易主为 sidNew)")
	}
}

// TestStaleExactTarget 易主检测:请求 cc-<旧sid8>、pane 仍同名在跑、但真值表说它已是别的会话
// → stale;登记 sid 与名字一致(常态)→ 非 stale;非 cc- 名 / 无登记 → 非 stale(不破坏
// 自定义 tmuxName 部署、无数据时保持旧行为)。
func TestStaleExactTarget(t *testing.T) {
	staleName := defaultTmuxName(sidOld)
	panes := []tmuxPane{ownPane("%1", staleName, "/c", "2.1.167")}
	owned := ownershipFor(panes, map[string]paneMapEntry{"%1": {Sid: sidNew, Name: staleName}})
	if !staleExactTarget(staleName, owned) {
		t.Fatalf("pane 已易主(登记 %s)却仍按 %s 发键,应判 stale", sidNew[:8], staleName)
	}
	selfOwned := ownershipFor(panes, map[string]paneMapEntry{"%1": {Sid: sidOld, Name: staleName}})
	if staleExactTarget(staleName, selfOwned) {
		t.Fatalf("登记 sid 与名字一致,不应判 stale")
	}
	if staleExactTarget(staleName, noOwn()) {
		t.Fatalf("无真值表数据不应判 stale(保持旧行为)")
	}
	custom := []tmuxPane{ownPane("%1", "mywork", "/c", "2.1.167")}
	customOwn := ownershipFor(custom, map[string]paneMapEntry{"%1": {Sid: sidNew, Name: "mywork"}})
	if staleExactTarget("mywork", customOwn) {
		t.Fatalf("非 cc- 自定义名不应判 stale")
	}
}

// TestOwnershipValidation pane_id 复用防护:条目的 Name 与活 pane 不符 → 不采信;
// 条目指向已死 pane → 不采信。
func TestOwnershipValidation(t *testing.T) {
	panes := []tmuxPane{ownPane("%1", "cc-current", "/c", "2.1.167")}
	pmap := map[string]paneMapEntry{
		"%1": {Sid: sidNew, Name: "cc-renamed-or-reused"}, // 名字对不上(tmux 重启后 %1 被复用)
		"%9": {Sid: sidOld, Name: "cc-dead"},              // pane 已不存在
	}
	own := ownershipFor(panes, pmap)
	if len(own.bySid) != 0 || len(own.byName) != 0 {
		t.Fatalf("不合规条目不得入视图:%+v %+v", own.bySid, own.byName)
	}
}

// TestNewestUnownedSidForCwd 已被真值表认领的会话不参与「cwd 当前会话」竞争。
func TestNewestUnownedSidForCwd(t *testing.T) {
	cwd := "/c"
	snaps := []claudeSnapshot{
		snap(sidOld, cwd, "2026-06-06T10:00:00Z"),
		snap(sidNew, cwd, "2026-06-06T12:00:00Z"), // 最新但已被认领
	}
	panes := []tmuxPane{ownPane("%1", "cc-somepane", cwd, "2.1.167")}
	own := ownershipFor(panes, map[string]paneMapEntry{"%1": {Sid: sidNew, Name: "cc-somepane"}})
	if got := newestUnownedSidForCwd(cwd, snaps, own); got != sidOld {
		t.Fatalf("排除已认领者后应返回 %s,得 %s", sidOld[:8], got)
	}
	if got := newestUnownedSidForCwd(cwd, snaps, noOwn()); got != sidNew {
		t.Fatalf("无认领时应返回最新 %s,得 %s", sidNew[:8], got)
	}
}

// TestPaneMapRoundtrip 真值表读写:CCFLY_PANEMAP 覆盖路径,写入后 loadPaneMap 原样读回;
// 缺失/损坏 → nil。
func TestPaneMapRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panemap.json")
	t.Setenv("CCFLY_PANEMAP", path)

	if m := loadPaneMap(); m != nil {
		t.Fatalf("文件缺失应返回 nil,得 %+v", m)
	}
	want := map[string]paneMapEntry{"%3": {Sid: sidNew, Name: "cc-bbbbbbbb", Cwd: "/c", UpdatedAt: 123}}
	data, _ := json.Marshal(want)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadPaneMap()
	if got["%3"].Sid != sidNew || got["%3"].Name != "cc-bbbbbbbb" {
		t.Fatalf("roundtrip 不符:%+v", got)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if m := loadPaneMap(); m != nil {
		t.Fatalf("损坏文件应返回 nil(落回启发式),得 %+v", m)
	}
}

// TestInstallSessionHookAt settings.json 幂等 upsert + 自愈:新建、二次安装零变化、保留既有键、
// 「二进制健在不抢写 / 二进制已删才修复」、非法 JSON 不动用户配置。
func TestInstallSessionHookAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	// 「健在」与「自己的」二进制都用真实临时文件模拟(自愈判定靠 os.Stat)。
	aliveBin := filepath.Join(dir, "alive-ccfly")
	selfBin := filepath.Join(dir, "self-ccfly")
	for _, p := range []string{aliveBin, selfBin} {
		if err := os.WriteFile(p, []byte("#!"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	selfCmd := `"` + selfBin + `" panemap-hook`

	// 1) 全新创建
	installSessionHookAt(path, selfCmd)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("应已创建 settings.json: %v", err)
	}
	if !strings.Contains(string(data), "panemap-hook") || !strings.Contains(string(data), "SessionStart") {
		t.Fatalf("hook 未写入:%s", data)
	}

	// 2) 幂等:同命令二次安装,内容不变
	before, _ := os.Stat(path)
	installSessionHookAt(path, selfCmd)
	after, _ := os.Stat(path)
	if before.ModTime() != after.ModTime() || before.Size() != after.Size() {
		t.Fatalf("同命令重装应零写入")
	}

	// 3) 别的实例装的、二进制健在 → 不抢写(多实例并存绝不互相改写)
	root := map[string]any{}
	_ = json.Unmarshal(data, &root)
	root["model"] = "opus"
	root["hooks"] = map[string]any{"SessionStart": []any{map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": `"` + aliveBin + `" panemap-hook`}},
	}}}
	merged, _ := json.Marshal(root)
	if err := os.WriteFile(path, merged, 0o644); err != nil {
		t.Fatal(err)
	}
	installSessionHookAt(path, selfCmd)
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), aliveBin) || strings.Contains(string(data), selfBin) {
		t.Fatalf("二进制健在不应被抢写:%s", data)
	}

	// 4) 自愈:hook 指向已删除的二进制(临时实例被清)→ 修复成本进程命令,保留用户既有键
	deadCmd := `"` + filepath.Join(dir, "deleted-ccfly") + `" panemap-hook`
	root["hooks"] = map[string]any{"SessionStart": []any{map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": deadCmd}},
	}}}
	merged, _ = json.Marshal(root)
	if err := os.WriteFile(path, merged, 0o644); err != nil {
		t.Fatal(err)
	}
	installSessionHookAt(path, selfCmd)
	data, _ = os.ReadFile(path)
	s := string(data)
	if !strings.Contains(s, selfBin) || strings.Contains(s, "deleted-ccfly") {
		t.Fatalf("死路径应被自愈修复:%s", s)
	}
	if !strings.Contains(s, `"model"`) {
		t.Fatalf("用户既有键应保留:%s", s)
	}
	if strings.Count(s, "panemap-hook") != 1 {
		t.Fatalf("应恰一条自家 hook:%s", s)
	}

	// 5) 非法 JSON:绝不动用户文件
	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	installSessionHookAt(path, `"/x" panemap-hook`)
	data, _ = os.ReadFile(path)
	if string(data) != "{broken" {
		t.Fatalf("非法 JSON 应原样保留,得:%s", data)
	}
}

// TestHookCmdPath 命令串 → 二进制路径(自愈判活用)。
func TestHookCmdPath(t *testing.T) {
	if got := hookCmdPath(`"/usr/local/bin/ccfly" panemap-hook`); got != "/usr/local/bin/ccfly" {
		t.Fatalf("got %q", got)
	}
	if got := hookCmdPath(`ccfly panemap-hook`); got != "" {
		t.Fatalf("非引号形式应返回空,got %q", got)
	}
}

// TestPushPrev /clear、resume 换 sid 时的轨迹维护:推队首、去重、resume 回老会话时移除、封顶。
func TestPushPrev(t *testing.T) {
	// sid 未变 → 原样
	if got := pushPrev(paneMapEntry{Sid: "a", Prev: []string{"x"}}, "a"); len(got) != 1 || got[0] != "x" {
		t.Fatalf("sid 未变应保留原轨迹,得 %v", got)
	}
	// 正常推入:旧 sid 到队首
	if got := pushPrev(paneMapEntry{Sid: "a", Prev: []string{"x"}}, "b"); len(got) != 2 || got[0] != "a" || got[1] != "x" {
		t.Fatalf("应 [a x],得 %v", got)
	}
	// resume 回轨迹里的老会话:它从轨迹移除(成为当前)
	if got := pushPrev(paneMapEntry{Sid: "b", Prev: []string{"a", "x"}}, "a"); len(got) != 2 || got[0] != "b" || got[1] != "x" {
		t.Fatalf("resume 回老会话应 [b x],得 %v", got)
	}
	// 封顶 prevCap
	long := paneMapEntry{Sid: "s0", Prev: []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8"}}
	if got := pushPrev(long, "new"); len(got) != prevCap || got[0] != "s0" {
		t.Fatalf("应封顶 %d 且 s0 在队首,得 %v", prevCap, got)
	}
	// 空旧条目(pane 首次登记)→ 无轨迹
	if got := pushPrev(paneMapEntry{}, "a"); got != nil {
		t.Fatalf("首次登记应无轨迹,得 %v", got)
	}
}

// TestPaneByFormerSid 死 sid 经 Prev 轨迹找回原 pane 及其当前会话;校验与 ownershipFor 同口径。
func TestPaneByFormerSid(t *testing.T) {
	panes := []tmuxPane{ownPane("%1", "ccfly-cloud-15", "/c", "2.1.170")}
	pmap := map[string]paneMapEntry{
		"%1": {Sid: sidNew, Name: "ccfly-cloud-15", Prev: []string{sidOld}},
	}
	if name, cur := paneByFormerSid(panes, pmap, sidOld[:8]); name != "ccfly-cloud-15" || cur != sidNew {
		t.Fatalf("应找回 pane 及当前 sid,得 (%q,%q)", name, cur)
	}
	if name, _ := paneByFormerSid(panes, pmap, "deadbeef"); name != "" {
		t.Fatalf("轨迹无此 sid 应空,得 %q", name)
	}
	// pane 名对不上(pane_id 复用)→ 不采信
	renamed := map[string]paneMapEntry{"%1": {Sid: sidNew, Name: "other-name", Prev: []string{sidOld}}}
	if name, _ := paneByFormerSid(panes, renamed, sidOld[:8]); name != "" {
		t.Fatalf("名字不匹配不应采信,得 %q", name)
	}
}
