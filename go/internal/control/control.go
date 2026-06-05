package control

// control.go — ccfly 的本地 HTTP 控制服务。
//
// 必须以「用户」身份运行:要和起 Claude 的那个用户用同一个默认 tmux socket、同一个
// ~/.claude,send-keys / capture-pane / 读 jsonl 才命中同一批会话。
//
// 安全模型(重要):
//   - 本服务**自身不做任何鉴权**。默认只绑回环 127.0.0.1(见 cmd/ccfly 的 --bind/CCFLY_BIND)。
//   - 任何对外暴露(给远端 UI 用)都应交由上游反向代理 / 消费方在前面统一把关
//     (例:Caddy/hub 用 operator session 鉴权后再转发到本回环服务)。
//   - 不含任何 wireguard / mesh / enroll / report 等网状网或上报逻辑;
//     ccfly 只暴露一个本地 HTTP 面。
//
// 端点:
//   GET  /healthz             健康检查
//   POST /sendkeys            往指定 tmux 会话注入文本/按键(斜杠命令快捷键用)
//   GET  /capture             tmux capture-pane 原文(无 jsonl 的普通会话 fallback)
//   POST /start               在 tmux 起一个 detached 会话(把离线会话拉活)
//   GET  /state               抓当前屏幕 → 判断器输出当前控件结构化状态
//   GET  /transcript[/stream] 会话紧凑全文 / SSE 实时跟随
//   GET  /subtranscript[/stream] 子代理 transcript / SSE
//   GET  /subagents           当前正在运行的子代理列表
//   GET  /workflow            一次 Workflow 执行的薄聚合摘要
//   GET  /workflowagent[/stream] 单个 workflow agent 的 transcript / SSE
//   GET  /cmdresult           信息类斜杠命令的结构化 markdown 结果
//   GET  /image               用户消息里的图片字节
//   GET  /info                会话信息(模型/上下文用量/累计 token)
//   GET  /term                自带网页终端 WebSocket(PTY+tmux,ttyd 帧兼容;去外部 ttyd 依赖)
//   GET  /sessions            落地页会话列表(@ccfly/react SessionMeta[] 形状)
//   GET  /                     内嵌 web 表世界 SPA(兜底;静态文件 + history 回退 index.html)

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Handler 构造并返回控制服务的 HTTP 处理器(所有端点)。消费方可自行包一层反代/鉴权。
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("POST /sendkeys", handleSendKeys)
	mux.HandleFunc("GET /transcript", handleTranscript)
	mux.HandleFunc("GET /transcript/stream", handleTranscriptStream)
	mux.HandleFunc("GET /subtranscript", handleSubtranscript)
	mux.HandleFunc("GET /subtranscript/stream", handleSubtranscriptStream)
	mux.HandleFunc("GET /subagents", handleSubagents)
	mux.HandleFunc("GET /workflow", handleWorkflow)
	mux.HandleFunc("GET /workflowagent", handleWorkflowAgent)
	mux.HandleFunc("GET /workflowagent/stream", handleWorkflowAgentStream)
	mux.HandleFunc("GET /capture", handleCapture)
	mux.HandleFunc("GET /cmdresult", handleCmdResult)
	mux.HandleFunc("GET /image", handleImage)
	mux.HandleFunc("GET /state", handleState)
	mux.HandleFunc("GET /info", handleInfo)
	mux.HandleFunc("POST /start", handleStart)
	mux.HandleFunc("GET /term", handleTerm)         // 自带网页终端 WS(ttyd 兼容);去外部 ttyd 依赖
	mux.HandleFunc("GET /sessions", handleSessions) // 落地页会话列表(SessionMeta[] 形状)
	// 内嵌 web 表世界 SPA:必须最后注册「GET /」兜底。Go 1.22 ServeMux「最具体优先」,
	// 上面各显式 API 路由自动赢过它;剩下「非 API、无文件」路径回退 index.html(history 路由)。
	mux.HandleFunc("GET /", staticHandler())
	return mux
}

// Serve 在 bind(如 "127.0.0.1:7699")上起控制服务,直到 ctx 取消后优雅关停。
// bind 由调用方(cmd/ccfly)从 --bind/--port / env 解析好;不在这里探测任何网卡/mesh。
func Serve(ctx context.Context, bind string) error {
	srv := &http.Server{Addr: bind, Handler: Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Printf("ccfly control: listening on %s (claude dir=%s)", bind, claudeProjectsDir())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// ─── handlers ───────────────────────────────────────────────────────────────

func handleSendKeys(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string   `json:"session"`
		Text    string   `json:"text"`
		Keys    []string `json:"keys"`
		Enter   bool     `json:"enter"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ctrlErr(w, 400, "bad json")
		return
	}
	if strings.TrimSpace(req.Session) == "" {
		ctrlErr(w, 400, "session required")
		return
	}
	var cmds [][]string
	// -l = literal(原样字面),避免把 "/model" 之类当按键名解析;-- 终止选项解析。
	if req.Text != "" {
		cmds = append(cmds, []string{"send-keys", "-t", req.Session, "-l", "--", req.Text})
	}
	// 具名键(Escape / C-c / Up …)不带 -l。
	if len(req.Keys) > 0 {
		args := append([]string{"send-keys", "-t", req.Session, "--"}, req.Keys...)
		cmds = append(cmds, args)
	}
	if req.Enter {
		cmds = append(cmds, []string{"send-keys", "-t", req.Session, "Enter"})
	}
	if len(cmds) == 0 {
		ctrlErr(w, 400, "nothing to send")
		return
	}
	for _, c := range cmds {
		if out, err := exec.Command("tmux", c...).CombinedOutput(); err != nil {
			ctrlErr(w, 500, "tmux: "+strings.TrimSpace(string(out))+" ("+err.Error()+")")
			return
		}
	}
	ctrlJSON(w, 200, map[string]any{"ok": true})
}

func handleCapture(w http.ResponseWriter, r *http.Request) {
	sess := r.URL.Query().Get("session")
	if sess == "" {
		ctrlErr(w, 400, "session required")
		return
	}
	lines := r.URL.Query().Get("lines")
	if _, err := strconv.Atoi(lines); err != nil {
		lines = "2000"
	}
	// ?ansi=1 → 保留 TUI 原始 ANSI 上色(展示/原始回退用);默认 -p(无色,解析/判定吃这个)。
	colorFlag := "-p"
	if r.URL.Query().Get("ansi") == "1" {
		colorFlag = "-e"
	}
	out, err := exec.Command("tmux", "capture-pane", "-t", sess, colorFlag, "-S", "-"+lines).CombinedOutput()
	if err != nil {
		ctrlErr(w, 404, "tmux: "+strings.TrimSpace(string(out)))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(out)
}

// handleCmdResult — 按需取「信息类斜杠命令」的结构化结果(?sid=&since=<byte>)。
// 从 since 字节起扫主 jsonl,返回首条 type:user + isMeta:true 且 content 为非空字符串的
// Markdown(实测 /context 走此路径),前端直接 markdown 渲染,摆脱 capture-pane + ANSI 解析。
// 找到:{found:true, markdown, cursor=该消息行末游标};没找到:{found:false, cursor=当前EOF}。
func handleCmdResult(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	path := transcriptPath(sid)
	if path == "" {
		ctrlErr(w, 404, "session not found")
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64) // 空/坏 → 0
	md, cursor, found := readCmdResult(path, since)
	ctrlJSON(w, 200, map[string]any{"found": found, "markdown": md, "cursor": cursor})
}

// handleImage — 取用户消息里的图片字节(?sid=&uuid=&idx=)。
// 在主 jsonl 按 uuid 定位该行,取 message.content 里第 idx 个图片(路径式 + base64 式合计计数):
//   base64 式 → 解码后按 media_type 返回;路径式 → 读文件返回(Content-Type 按扩展名)。
// 安全:路径式必须落在 ~/.claude/image-cache/<sid>/ 之下(findMessageImage 内 safeImagePath 已校验防穿越),否则当作找不到 → 404。
// 图片不可变 → 长缓存。
func handleImage(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	uuid := r.URL.Query().Get("uuid")
	idx, err := strconv.Atoi(r.URL.Query().Get("idx"))
	if sid == "" || uuid == "" || err != nil || idx < 0 {
		ctrlErr(w, 400, "sid, uuid, idx required")
		return
	}
	info, ok := findMessageImage(sid, uuid, idx)
	if !ok {
		ctrlErr(w, 404, "image not found")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if info.Path != "" {
		data, err := os.ReadFile(info.Path)
		if err != nil {
			ctrlErr(w, 404, "image not found")
			return
		}
		ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(info.Path)))
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.Write(data)
		return
	}
	w.Header().Set("Content-Type", info.MediaType)
	w.Write(info.Data)
}

// handleState — 抓「当前屏幕」(仅可见,无 -S)→ 判断器输出当前控件结构化状态。
func handleState(w http.ResponseWriter, r *http.Request) {
	sess := r.URL.Query().Get("session")
	if sess == "" {
		ctrlErr(w, 400, "session required")
		return
	}
	// -e 保留 ANSI 上色:detectState 内部对各判定先剥色,但「输入建议」靠 dim 属性识别,需带色原文。
	out, err := exec.Command("tmux", "capture-pane", "-t", sess, "-p", "-e").CombinedOutput()
	if err != nil {
		ctrlJSON(w, 200, ctrlState{Kind: "offline"})
		return
	}
	ctrlJSON(w, 200, detectState(string(out)))
}

// handleStart — 在 tmux 起一个会话(detached);用于表世界把「离线会话」拉活(随后 trust/启动提示自会被 /state 映射)。
func handleStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
		Cwd     string `json:"cwd"`
		Cmd     string `json:"cmd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ctrlErr(w, 400, "bad json")
		return
	}
	if strings.TrimSpace(req.Session) == "" {
		ctrlErr(w, 400, "session required")
		return
	}
	args := []string{"new-session", "-d", "-s", req.Session}
	if req.Cwd != "" {
		args = append(args, "-c", req.Cwd)
	}
	if req.Cmd != "" {
		args = append(args, req.Cmd)
	}
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		ctrlErr(w, 500, "tmux: "+strings.TrimSpace(string(out))+" ("+err.Error()+")")
		return
	}
	ctrlJSON(w, 200, map[string]any{"ok": true})
}

// handleInfo — 会话信息(模型/上下文用量/累计 token 花费/元信息),从 jsonl 派生,统一展示用。
func handleInfo(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	path := transcriptPath(sid)
	if path == "" {
		ctrlErr(w, 404, "session not found")
		return
	}
	info, ok := readSessionInfo(path)
	if !ok {
		ctrlErr(w, 404, "empty session")
		return
	}
	ctrlJSON(w, 200, info)
}

// handleTranscript — 会话紧凑全文,三种调用(详见各分支注释),返回统一加 firstCursor + hasMore。
//
//	firstCursor = 本批**最旧** item 所在行的**起始**字节;前端下次向上分页用 ?before=<firstCursor> 无缝接续(不重不漏)。
//	hasMore     = 是否还有更老的 item(可继续向上翻)。
//	cursor      = 向后增量游标(本批最新边界):首拉/before=窗口右端,since=EOF。前端 SSE 跟随仍用最新的 cursor。
//
//  1. 首拉(无 since、无 before)= 尾窗:返回末尾最多 150 条(且 ~4MB 字节预算,先到为准)。
//     → {items, cursor:EOF, firstCursor:本批最旧行首, hasMore:firstCursor>0}
//  2. 向上分页(?before=<byte>):返回紧邻 before 之前的末尾最多 150 条(更老的一窗)。
//     → {items, cursor:before, firstCursor:本批最旧行首, hasMore}
//  3. 增量更新(?since=<byte>,保持现有语义):从 since 读到 EOF。
//     → {items, cursor:EOF, firstCursor:since, hasMore:false}
func handleTranscript(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	path := transcriptPath(sid)
	if path == "" {
		ctrlErr(w, 404, "session not found")
		return
	}
	q := r.URL.Query()

	// 3) 增量:?since= 优先(保持现有语义,SSE 续连/手动刷新共用)。
	if q.Has("since") {
		since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
		steps, cursor, err := readTranscriptSteps(path, since)
		if err != nil {
			ctrlErr(w, 500, err.Error())
			return
		}
		items := make([]tItem, 0, len(steps))
		for _, s := range steps {
			items = append(items, s.Item)
		}
		ctrlJSON(w, 200, map[string]any{"cursor": cursor, "items": items, "firstCursor": since, "hasMore": false})
		return
	}

	st, err := os.Stat(path)
	if err != nil {
		ctrlErr(w, 500, err.Error())
		return
	}
	eof := st.Size()

	// 2) 向上分页:?before=<byte> → 取 [.., before) 的尾窗。before<=0 视作到顶,返回空。
	if q.Has("before") {
		before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
		if before > eof {
			before = eof
		}
		items, firstCursor, hasMore, err := transcriptWindow(path, before)
		if err != nil {
			ctrlErr(w, 500, err.Error())
			return
		}
		if items == nil {
			items = []tItem{}
		}
		ctrlJSON(w, 200, map[string]any{"cursor": before, "items": items, "firstCursor": firstCursor, "hasMore": hasMore})
		return
	}

	// 1) 首拉:尾窗(末尾最多 150 条 / ~4MB)。
	items, firstCursor, hasMore, err := transcriptWindow(path, eof)
	if err != nil {
		ctrlErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []tItem{}
	}
	ctrlJSON(w, 200, map[string]any{"cursor": eof, "items": items, "firstCursor": firstCursor, "hasMore": hasMore})
}

// handleTranscriptStream — SSE。~1s 轮询 jsonl 增量,逐条 data:{cursor,item} 推送。
// 每条带「该条行末游标」,断线重连用最后收到的游标续上,不重不漏。
func handleTranscriptStream(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	path := transcriptPath(sid)
	if path == "" {
		ctrlErr(w, 404, "session not found")
		return
	}
	streamTranscript(w, r, path)
}

// handleSubtranscript — 子代理 transcript(?sid=&toolUseId=&since=)。
// 由 sid+toolUseId 解析 <projectDir>/<sid>/subagents/agent-<agentId>.jsonl;
// 找不到 → 404(前端据此降级);复用 readTranscriptSteps,返回结构同 /transcript + meta。
func handleSubtranscript(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	toolUseID := r.URL.Query().Get("toolUseId")
	path, meta := subagentPathByToolUse(sid, toolUseID)
	if path == "" {
		ctrlErr(w, 404, "subagent not found")
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	steps, cursor, err := readTranscriptSteps(path, since)
	if err != nil {
		ctrlErr(w, 500, err.Error())
		return
	}
	items := make([]tItem, 0, len(steps))
	for _, s := range steps {
		items = append(items, s.Item)
	}
	ctrlJSON(w, 200, map[string]any{"meta": meta, "cursor": cursor, "items": items, "hasMore": false})
}

// handleSubtranscriptStream — 子代理 SSE,语义同 handleTranscriptStream,只是 path 由 sid+toolUseId 解析。
func handleSubtranscriptStream(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	toolUseID := r.URL.Query().Get("toolUseId")
	path, _ := subagentPathByToolUse(sid, toolUseID)
	if path == "" {
		ctrlErr(w, 404, "subagent not found")
		return
	}
	streamTranscript(w, r, path)
}

// handleWorkflow — 「一次 Workflow 执行」的薄聚合摘要(?sid=&runId=,可选 &toolUseId= 兜底)。
// 读 <projectDir>/<sid>/workflows/wf_<runId>.json,剥掉 script/scriptPath/logs/result 及
// 各 agent 的 promptPreview/resultPreview 等大字段,只回卡片所需(见 wfSummary)。
// 定位优先 runId(=文件名);仅给 toolUseId 时扫主 jsonl 找该 tool_use 的 async_launched 行取 runId。
// runId/agentId 校验无斜杠 + filepath.Clean + 强制前缀落在该 sid 目录下防穿越。
func handleWorkflow(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	runId := r.URL.Query().Get("runId")
	if runId == "" { // 兜底:仅给 toolUseId 时,扫主 jsonl 反查 runId
		runId = workflowRunIdByToolUse(sid, r.URL.Query().Get("toolUseId"))
	}
	path := workflowSummaryPath(sid, runId)
	if path == "" {
		ctrlErr(w, 404, "workflow not found")
		return
	}
	sum, ok := readWorkflowSummary(path)
	if !ok {
		ctrlErr(w, 500, "bad workflow summary")
		return
	}
	ctrlJSON(w, 200, sum)
}

// handleWorkflowAgent — 单个 workflow agent 的 transcript(?sid=&runId=&agentId=&since=)。
// 定位 <projectDir>/<sid>/subagents/workflows/wf_<runId>/agent-<agentId>.jsonl,复用
// readTranscriptSteps,返回结构同 /subtranscript({cursor,items,hasMore,firstCursor})。
func handleWorkflowAgent(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	runId := r.URL.Query().Get("runId")
	agentId := r.URL.Query().Get("agentId")
	path := workflowAgentPath(sid, runId, agentId)
	if path == "" {
		ctrlErr(w, 404, "workflow agent not found")
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	steps, cursor, err := readTranscriptSteps(path, since)
	if err != nil {
		ctrlErr(w, 500, err.Error())
		return
	}
	items := make([]tItem, 0, len(steps))
	for _, s := range steps {
		items = append(items, s.Item)
	}
	ctrlJSON(w, 200, map[string]any{"cursor": cursor, "items": items, "hasMore": false, "firstCursor": since})
}

// handleWorkflowAgentStream — workflow agent 的 SSE,语义同 handleSubtranscriptStream,只是 path 由
// sid+runId+agentId 解析。~1s 轮询 jsonl 增量逐条 data:{cursor,item} 推送,断线重连用最后游标续上。
func handleWorkflowAgentStream(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	runId := r.URL.Query().Get("runId")
	agentId := r.URL.Query().Get("agentId")
	path := workflowAgentPath(sid, runId, agentId)
	if path == "" {
		ctrlErr(w, 404, "workflow agent not found")
		return
	}
	streamTranscript(w, r, path)
}

// streamTranscript — 所有 SSE 端点共用的实现:~1s 轮询 path 的 jsonl 增量,逐条
// data:{cursor,item} 推送;~15s 无新增发一条注释心跳保活中间代理。游标从 ?since= 起,
// 每条带「该条行末游标」,断线重连用最后收到的游标续上,不重不漏。
func streamTranscript(w http.ResponseWriter, r *http.Request, path string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		ctrlErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flusher.Flush()

	cursor, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	ctx := r.Context()
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	idle := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			steps, newCursor, err := readTranscriptSteps(path, cursor)
			if err == nil && len(steps) > 0 {
				for _, s := range steps {
					data, _ := json.Marshal(map[string]any{"cursor": s.Cursor, "item": s.Item})
					fmt.Fprintf(w, "data: %s\n\n", data)
				}
				cursor = newCursor
				flusher.Flush()
				idle = 0
			} else if idle++; idle >= 15 {
				// ~15s 心跳注释,保活中间代理(反代/hub)。
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
				idle = 0
			}
		}
	}
}

// ─── helpers(避免与其它文件同名,用 ctrl 前缀)─────────────────────────────

func ctrlJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ctrlErr(w http.ResponseWriter, status int, msg string) {
	ctrlJSON(w, status, map[string]any{"error": msg})
}
