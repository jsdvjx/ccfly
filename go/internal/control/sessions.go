package control

// sessions.go — 落地页(landing)用的会话列表端点 GET /sessions。
//
// 扫描本地 Claude 会话 jsonl(scanClaudeSessions),整形成 @ccfly/react 的 fetchSessions
// 所期望的 SessionMeta 形状(snake_case 键),供落地页的 SessionPicker 直接消费:
// 把 Provider 的 sessionsUrl 指到本端点(baseUrl + "/sessions")即可,无需额外适配层。
//
// 形状对齐(Contract A 的 SessionMeta):
//   hostname / session_id / title / state / turns / tokens / model / cwd / last_ts
// 额外补充(落地页排序/活性展示用,SessionMeta 多余字段会被前端忽略):
//   mtime_ms  最后活动时间(毫秒,排序用;无有效时间戳 → 0)
//   age_sec   距最后活动秒数(scanClaudeSessions 已算)
//   preview   末条消息短预览
//   live      该会话是否有同名 tmux 会话在跑(默认 tmuxName: "cc-"+sid[:8])
//
// 排序:按 last_ts 倒序(最近活动在前),与 Jarvis web 的会话列表一致。
//
// 安全模型同本服务其它端点:自身不鉴权,默认绑回环,远端暴露交反代。

import (
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// sessionRow 是落地页会话列表的一行。snake_case 键与 @ccfly/react 的 SessionMeta 对齐,
// 额外字段(mtime_ms/age_sec/preview/live)前端按需取用,不需要的会被忽略。
type sessionRow struct {
	Hostname  string `json:"hostname"`
	SessionID string `json:"session_id"`
	Title     string `json:"title,omitempty"`
	State     string `json:"state"`
	Turns     int    `json:"turns"`
	Tokens    int    `json:"tokens"`
	Model     string `json:"model,omitempty"`
	Cwd       string `json:"cwd"`
	LastTs    string `json:"last_ts"` // RFC3339(scanClaudeSessions 原样透传)
	MtimeMs   int64  `json:"mtime_ms"`
	AgeSec    int64  `json:"age_sec"`
	Preview   string `json:"preview,omitempty"`
	Live      bool   `json:"live"`
}

// handleSessions — GET /sessions:落地页会话列表(SessionMeta[] 形状 + 落地页补充字段)。
func handleSessions(w http.ResponseWriter, _ *http.Request) {
	snaps, err := scanClaudeSessions()
	if err != nil {
		ctrlErr(w, 500, err.Error())
		return
	}
	host := hostName()
	live := liveTmuxSessions() // 一次 list-sessions,O(会话数) 查表,廉价

	rows := make([]sessionRow, 0, len(snaps))
	for _, s := range snaps {
		rows = append(rows, sessionRow{
			Hostname:  host,
			SessionID: s.SessionID,
			Title:     s.Title,
			State:     s.State,
			Turns:     s.Turns,
			Tokens:    s.Tokens,
			Model:     s.Model,
			Cwd:       s.Cwd,
			LastTs:    s.LastTs,
			MtimeMs:   tsToMs(s.LastTs),
			AgeSec:    s.AgeSec,
			Preview:   s.Preview,
			Live:      live[defaultTmuxName(s.SessionID)],
		})
	}
	// 按 last_ts 倒序(最近活动在前);时间戳缺失/无法解析的沉底。
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].MtimeMs > rows[j].MtimeMs })

	ctrlJSON(w, 200, rows)
}

// hostName 返回本机主机名(取不到时空串,SessionMeta.hostname 仍为合法键)。
func hostName() string {
	h, _ := os.Hostname()
	return h
}

// defaultTmuxName 复刻 @ccfly/react 默认的 tmuxName:"cc-" + sid 前 8 字符。
// 落地页若自定义了 Provider.tmuxName,live 判定会不准——但默认部署一致。
func defaultTmuxName(sid string) string {
	if len(sid) > 8 {
		sid = sid[:8]
	}
	return "cc-" + sid
}

// liveTmuxSessions 一次 `tmux list-sessions` 取所有在跑会话名 → set。
// tmux 不在跑 / 无会话(exit 1)都视作空集(不报错):落地页 live 全 false 即可。
func liveTmuxSessions() map[string]bool {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	set := map[string]bool{}
	if err != nil {
		return set
	}
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			set[name] = true
		}
	}
	return set
}

// tsToMs 把 RFC3339 时间戳转毫秒(排序用);空/坏 → 0(沉底)。
func tsToMs(ts string) int64 {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}
