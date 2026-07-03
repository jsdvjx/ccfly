package control

// panemap.go — pane ↔ 当前 session id 的「真值表」(根治 webui 消息错发到别的会话)。
//
// 病根:一个 tmux pane 里跑着的 claude,其 session id 会在 /clear、里世界 /resume 切换、
// /compact 等时刻改变,而 pane 的 tmux 名(cc-<初始sid[:8]>)永远不变;同一 cwd 又常有多个
// pane 同时跑 claude。于是「sid → 哪个 pane」在事后**无法从磁盘数据推断**——旧版用
// 「cwd + 最近活动 + 取第一个」启发式去猜,多 pane 同 cwd 时必然猜错,消息打进别人的对话。
//
// 解法:不猜,在事发现场记录真值。Claude Code 的 SessionStart hook 在每次会话开始
// (startup / resume / clear / compact)时于 claude 进程环境内执行——彼时 $TMUX_PANE 就在
// 环境变量里。hook(`ccfly panemap-hook`)把「pane id → 当前 sid」写进 ~/.ccfly/panemap.json,
// 解析端(tmuxresolve.go)查表即得确定性映射,与「谁启动的 tmux」完全无关(用户手开的
// tmux 里跑 claude 一样登记,webui+本地双端控制同一会话的特性不受影响)。
//
// 增量收编:hook 由 InstallSessionHook 在 ccfly 启动时幂等写入 ~/.claude/settings.json;
// 已在跑的 claude 进程对 hooks 配置做了启动期快照、不受影响(它们继续走 tmuxresolve.go 的
// fail-closed 启发式兜底),其 claude 重启后自然进入查表体系。CCFLY_NO_HOOK=1 可关闭安装。
//
// 防 pane id 复用:tmux 重启后 %N 从头分配,旧条目可能指到全新 pane。故条目同时记录登记时的
// tmux 会话名,读取时要求 pane_id 与名字**双匹配**才采信;hook 每次写入还顺手清掉已死 pane 的
// 条目。任何不匹配 → 视作无数据,落回启发式(绝不据脏数据改绑)。

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// paneMapEntry 是真值表的一条:某个 tmux pane 当前跑着哪个 Claude 会话。
type paneMapEntry struct {
	Sid  string `json:"sid"`
	Name string `json:"name"`          // 登记时的 tmux 会话名(读取时与活 pane 双匹配,防 pane_id 复用)
	Cwd  string `json:"cwd,omitempty"` // hook 收到的会话 cwd(诊断用,不参与判定)
	// Prev:本 pane 先前跑过的 sid 轨迹(新→旧,封顶 prevCap)。/clear、里世界 /resume 换 sid 时
	// hook 把旧 sid 推进来。/sse/jsonl 的跟随重连靠它愈合:客户端断线窗口里错过了 /clear,
	// 重连还带着已死的旧 sid → 据 Prev 找回原 pane、直接续其当前会话(见 sse.go paneByFormerSid 用法)。
	Prev      []string `json:"prev,omitempty"`
	UpdatedAt int64    `json:"updated_at"` // epoch 秒
}

// prevCap:Prev 轨迹封顶条数。一个长命 pane 经多次 /clear 的常见深度在个位数;8 足够愈合
// 任何现实的断线窗口,又不让 panemap 无限膨胀。
const prevCap = 8

// pushPrev 计算 pane 条目被新 sid 接管后的 Prev 轨迹:旧 sid 推到队首(新→旧),去重、封顶。
// sid 未变(同会话重启/resume 续用)→ 原轨迹原样保留。纯函数,便于单测。
func pushPrev(old paneMapEntry, newSid string) []string {
	if old.Sid == "" || old.Sid == newSid {
		return old.Prev
	}
	out := make([]string, 0, prevCap)
	out = append(out, old.Sid)
	for _, s := range old.Prev {
		if s == newSid || s == old.Sid {
			continue // 新 sid 曾在轨迹里(resume 回老会话)→ 移除,它现在是「当前」;旧 sid 去重
		}
		if len(out) >= prevCap {
			break
		}
		out = append(out, s)
	}
	return out
}

// paneByFormerSid 在真值表里找「曾经跑过 sid(按前缀匹配)」的活 pane:返回 (pane 名, 它当前的 sid)。
// 供 /sse/jsonl 跟随重连愈合:页面带着已死的旧 sid 回来,据此锚回原 pane、续上当前会话。
// 只认 pane_id+名字双匹配的活 pane(与 ownershipFor 同口径);找不到 → ("", "")。
func paneByFormerSid(panes []tmuxPane, pmap map[string]paneMapEntry, sidPrefix string) (string, string) {
	if sidPrefix == "" || len(pmap) == 0 {
		return "", ""
	}
	for _, p := range panes {
		if p.PaneID == "" {
			continue
		}
		e, ok := pmap[p.PaneID]
		if !ok || e.Name != p.Name || e.Sid == "" {
			continue
		}
		for _, old := range e.Prev {
			if strings.HasPrefix(old, sidPrefix) {
				return p.Name, e.Sid
			}
		}
	}
	return "", ""
}

// paneMapPath 真值表落盘位置。CCFLY_PANEMAP 可覆盖(测试用);取不到 home → ""(功能整体降级)。
func paneMapPath() string {
	if p := os.Getenv("CCFLY_PANEMAP"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ccfly", "panemap.json")
}

// loadPaneMap 读真值表(键 = tmux pane id,如 "%5")。文件缺失/损坏 → nil(等同无数据,落回启发式)。
func loadPaneMap() map[string]paneMapEntry {
	path := paneMapPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	m := map[string]paneMapEntry{}
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return m
}

// paneOwnership 是真值表对照「当前活 pane」校验后的双向视图,解析期的唯一查询入口。
// 只含通过校验(pane_id 与名字双匹配)的条目;查不到 = hook 未覆盖,调用方走启发式兜底。
type paneOwnership struct {
	bySid  map[string]string // sid → 正跑着它的 tmux 会话名
	byName map[string]string // tmux 会话名 → 该 pane 当前的 sid
}

// ownershipFor 把 pmap 对照 panes 校验成 paneOwnership。pmap/panes 为空 → 空视图(全走兜底)。
func ownershipFor(panes []tmuxPane, pmap map[string]paneMapEntry) paneOwnership {
	own := paneOwnership{bySid: map[string]string{}, byName: map[string]string{}}
	if len(pmap) == 0 {
		return own
	}
	for _, p := range panes {
		if p.PaneID == "" {
			continue
		}
		e, ok := pmap[p.PaneID]
		if !ok || e.Sid == "" || e.Name != p.Name {
			continue // 无条目 / 名字对不上(pane_id 已被 tmux 复用)→ 不采信
		}
		own.bySid[e.Sid] = p.Name
		own.byName[p.Name] = e.Sid
	}
	return own
}

// RunPaneMapHook 是 `ccfly panemap-hook` 子命令的实现:作为 Claude Code 的 SessionStart hook
// 运行,把「本 pane → 本会话」写进真值表。
//
// 输入:stdin 的 hook JSON(取 session_id / cwd);环境变量 TMUX_PANE 定位 pane。
// 铁律:**绝不向 stdout 输出任何字节**(SessionStart hook 的 stdout 会被注入 Claude 上下文),
// 任何失败都静默返回 nil(hook 绝不打扰/拖慢会话启动)。
func RunPaneMapHook(stdin io.Reader) error {
	var in struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
	}
	if json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&in) != nil || in.SessionID == "" {
		return nil
	}
	paneID := os.Getenv("TMUX_PANE")
	if paneID == "" {
		return nil // 不在 tmux 里跑的 claude:没有 pane 可控,无须登记
	}
	nameB, err := tmuxCmd("display-message", "-p", "-t", paneID, "#{session_name}").Output()
	if err != nil {
		return nil
	}
	name := strings.TrimSpace(string(nameB))
	if name == "" {
		return nil
	}
	path := paneMapPath()
	if path == "" {
		return nil
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return nil
	}
	// 排他锁:开机恢复一批会话时 hook 并发触发,锁保证「读-改-写」不丢更新。
	// (发布目标仅 darwin/linux,见 scripts/build-binaries.sh,Flock 可用。)
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil
	}
	defer lock.Close()
	if flockExclusive(lock.Fd()) != nil {
		return nil
	}
	defer flockUnlock(lock.Fd()) //nolint:errcheck // 解锁失败随 Close 释放

	m := loadPaneMap()
	if m == nil {
		m = map[string]paneMapEntry{}
	}
	// sid 变更(/clear、里世界 /resume)→ 旧 sid 推入 Prev 轨迹(跟随重连的愈合依据)。
	m[paneID] = paneMapEntry{
		Sid: in.SessionID, Name: name, Cwd: in.Cwd,
		Prev: pushPrev(m[paneID], in.SessionID), UpdatedAt: time.Now().Unix(),
	}
	// 顺手清掉已死 pane 的条目:pane id 会被 tmux 复用,留着会张冠李戴(读取端还有名字双匹配兜底)。
	if out, err := tmuxCmd("list-panes", "-a", "-F", "#{pane_id}").Output(); err == nil {
		alive := map[string]bool{}
		for _, id := range strings.Fields(string(out)) {
			alive[id] = true
		}
		for id := range m {
			if !alive[id] {
				delete(m, id)
			}
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return nil
	}
	return os.Rename(tmp, path) // 原子替换:读端永远看到完整 JSON
}

// renamePaneMapEntryName 在「把某 pane 的 tmux 会话重命名」之后,把真值表里该 paneID 条目的 Name
// 同步成 newName。必须同步:ownershipFor 用「pane_id + 名字双匹配」校验条目(e.Name != p.Name 即弃用),
// 若只改了 tmux 名却没改真值表,该 pane 的归属就被判不一致而丢弃,解析退化到启发式(本类 bug 之源)。
// 与 hook 同款 flock + 原子写,避免与并发 hook 写互相丢更新。尽力而为,任何失败静默返回。
func renamePaneMapEntryName(paneID, newName string) {
	path := paneMapPath()
	if path == "" || paneID == "" || newName == "" {
		return
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	defer lock.Close()
	if flockExclusive(lock.Fd()) != nil {
		return
	}
	defer flockUnlock(lock.Fd()) //nolint:errcheck
	m := loadPaneMap()
	e, ok := m[paneID]
	if !ok || e.Name == newName {
		return
	}
	e.Name = newName
	m[paneID] = e
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, path) // 原子替换
}

// registerPaneMapEntry 直接把「pane → sid」写进真值表(/new 用:sid 经 --session-id 预知,
// 不等 SessionStart hook —— Windows/psmux 不设 TMUX_PANE,hook 注册不了,全靠这里)。
// 与 hook 同款 flock + 原子写;尽力而为,任何失败静默返回。
func registerPaneMapEntry(paneID, sid, name, cwd string) {
	path := paneMapPath()
	if path == "" || paneID == "" || sid == "" {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	defer lock.Close()
	if flockExclusive(lock.Fd()) != nil {
		return
	}
	defer flockUnlock(lock.Fd()) //nolint:errcheck
	m := loadPaneMap()
	if m == nil {
		m = map[string]paneMapEntry{}
	}
	m[paneID] = paneMapEntry{
		Sid: sid, Name: name, Cwd: cwd,
		Prev: pushPrev(m[paneID], sid), UpdatedAt: time.Now().Unix(),
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// claudeSettingsPath 用户级 Claude Code 配置。CCFLY_CLAUDE_SETTINGS 可覆盖(测试用)。
func claudeSettingsPath() string {
	if p := os.Getenv("CCFLY_CLAUDE_SETTINGS"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// InstallSessionHook 把 SessionStart hook 幂等写入 ~/.claude/settings.json,并具备**自愈**:
// 由巡检(scanner.go)每拍重申。不带 matcher(startup/resume/clear/compact 全触发——sid 在
// 这些时刻都可能变)。CCFLY_NO_HOOK=1 跳过。
//
// 自愈语义(实案教训:一个跑在 /private/tmp 下的临时 ccfly 实例曾把 hook 命令改写成自己的
// 路径,临时目录删除后**每次 SessionStart 都报错**——错误块异步注入 TUI,直接打断表世界的
// 读屏/菜单流;panemap 也全面停更):
//   - 现有条目的二进制仍在 → 不动(不管是不是本进程的路径:多个并存实例绝不互相抢写,
//     任何一个活着的合法二进制都能写真值表);
//   - 现有条目的二进制已不存在(临时实例被删、npm 旧版本被清)→ 用本进程路径修复;
//   - 无条目 → 安装。
func InstallSessionHook() {
	if os.Getenv("CCFLY_NO_HOOK") == "1" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	installSessionHookAt(claudeSettingsPath(), fmt.Sprintf("%q panemap-hook", exe))
}

// hookCmdPath 从 hook 命令串里取出二进制路径(命令形如 `"/abs/path" panemap-hook`)。
// 非引号开头(手写的 PATH 形式等)→ ""(调用方按「无法判活」处理,直接重写成规范形式)。
func hookCmdPath(cmd string) string {
	if !strings.HasPrefix(cmd, `"`) {
		return ""
	}
	if i := strings.Index(cmd[1:], `"`); i >= 0 {
		return cmd[1 : 1+i]
	}
	return ""
}

// installSessionHookAt 把 hookCmd upsert 进 path 的 hooks.SessionStart(按 "panemap-hook" 识别
// 自家条目;自愈语义见 InstallSessionHook)。原文件解析失败 → 不动用户配置直接放弃;
// 内容无变化 → 零写入(巡检每拍调用,常态必须是无副作用的快路径)。
func installSessionHookAt(path, hookCmd string) {
	if path == "" {
		return
	}
	root := map[string]any{}
	mode := os.FileMode(0o644)
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &root) != nil {
			log.Printf("ccfly panemap: %s 不是合法 JSON,跳过 hook 安装(不动用户配置)", path)
			return
		}
		if fi, err := os.Stat(path); err == nil {
			mode = fi.Mode().Perm() // 保留用户既有权限
		}
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	arr, _ := hooks["SessionStart"].([]any)
	updated := false
	for _, g := range arr {
		group, _ := g.(map[string]any)
		hs, _ := group["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			cmd, _ := hm["command"].(string)
			if !strings.Contains(cmd, "panemap-hook") {
				continue
			}
			if cmd == hookCmd {
				return // 已是本进程的规范命令 → 零写入
			}
			if p := hookCmdPath(cmd); p != "" {
				if _, err := os.Stat(p); err == nil {
					return // 别的实例装的、二进制健在 → 不抢写(避免多实例互相改写)
				}
			}
			hm["command"] = hookCmd // 二进制已不存在 / 命令不规范 → 自愈修复
			updated = true
		}
	}
	if !updated {
		arr = append(arr, map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": hookCmd}},
		})
	}
	hooks["SessionStart"] = arr
	root["hooks"] = hooks
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	tmp := path + ".ccfly.tmp"
	if os.WriteFile(tmp, append(data, '\n'), mode) != nil {
		return
	}
	if os.Rename(tmp, path) == nil {
		log.Printf("ccfly panemap: SessionStart hook 已写入/修复 %s → %s(pane↔sid 真值表;CCFLY_NO_HOOK=1 可禁用)", path, hookCmd)
	}
}
