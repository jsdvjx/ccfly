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
//   POST /upload              表世界图片/文件上传(multipart);落盘会话 cwd 的 .ccfly-uploads/,回绝对路径
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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// imgBufSeq 是 imgBufferName 的进程内回退计数器(crypto/rand 失败时用,绝不退化成固定名)。
var imgBufSeq atomic.Uint64

// imgBufferName 为一次「带图原子提交」生成**唯一**的 tmux buffer 名。
// 关键:tmux buffer 是 tmux server 全局的;若所有请求都用固定名 "ccfly-img",两个并发带图提交
// (两个会话/两个标签页)会互相覆盖 buffer —— A 粘成 B 的图,或 -d(粘完即删)让另一个 paste-buffer
// 报 "no buffer" 500 且输入行已半填。每次请求一个随机名即可消除跨请求竞争(同一请求内多图顺序复用同名无害)。
func imgBufferName() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "ccfly-img-" + hex.EncodeToString(b[:])
	}
	// 熵源故障:回退到进程内自增 + 纳秒,仍保证本进程内唯一(跨进程极罕见,聊胜于固定名)。
	return "ccfly-img-" + strconv.FormatUint(imgBufSeq.Add(1), 36) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// bindIsLoopback 判定一个监听地址(host:port)是否仅绑回环。无法解析出明确回环 host 时
// 一律按「非回环」处理(宁可多警告也不漏)。空 host(如 ":7699")= 绑全网卡,显然非回环。
func bindIsLoopback(bind string) bool {
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		// 没带端口或格式异常:尝试把整串当 host 解析。
		host = bind
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false // ":7699" / "0.0.0.0:..." 这类 = 全网卡,非回环
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost"
}

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
	mux.HandleFunc("GET /dirs", handleDirs)                // 目录浏览(新建会话选 cwd 用):列某路径下的子目录
	mux.HandleFunc("POST /new", handleNew)                 // 新建会话:在指定 cwd detached 起全新 claude,轮询 panemap 回真 sid
	mux.HandleFunc("POST /reload", handleReload)           // 重载会话:杀 tmux 再以 claude --resume 重建(可注入新 env_vars)
	mux.HandleFunc("POST /takeover", handleTakeover)       // 接管:杀掉会话既有 claude 进程,随后 /term 重建进 tmux(见 takeover.go)
	mux.HandleFunc("POST /upload", handleUpload)           // 表世界图片/文件上传 → 落盘会话 cwd 的 .ccfly-uploads/(见 upload.go)
	mux.HandleFunc("GET /term", handleTerm)                // 自带网页终端 WS(ttyd 兼容);去外部 ttyd 依赖
	mux.HandleFunc("GET /sessions", handleSessions)        // 落地页会话列表(SessionMeta[] 形状)
	mux.HandleFunc("GET /sse/jsonl", handleSseJsonl)       // 原始 jsonl 增量流(SSE,fsnotify;ccfly-ttyd-ui 状态源)
	mux.HandleFunc("GET /jsonl/before", handleJsonlBefore) // 向上翻页:before 字节前的一窗更老原始行(无状态)
	// 内嵌 web 表世界 SPA:必须最后注册「GET /」兜底。Go 1.22 ServeMux「最具体优先」,
	// 上面各显式 API 路由自动赢过它;剩下「非 API、无文件」路径回退 index.html(history 路由)。
	mux.HandleFunc("GET /", staticHandler())
	return mux
}

// Serve 在 bind(如 "127.0.0.1:7699")上起控制服务,直到 ctx 取消后优雅关停。
// bind 由调用方(cmd/ccfly)从 --bind/--port / env 解析好;不在这里探测任何网卡/mesh。
func Serve(ctx context.Context, bind string) error {
	// 部署护栏:本服务自身不鉴权(含 /upload 这种把不可信上传落盘的端点),必须绑回环、由上游反代
	// (cloud gateway 的 requireAuth + 设备归属)统一把关。若绑到非回环地址直接暴露设备端口,任何能到达
	// 该端口的人都能无鉴权上传/控制 —— 故在此打 WARN,除非运维显式 opt-in CCFLY_ALLOW_PUBLIC_BIND=1
	// (确认确实在可信网络/反代之后)。仅警告不阻断:保留高级用户自担风险绑公网的能力。
	if !bindIsLoopback(bind) && os.Getenv("CCFLY_ALLOW_PUBLIC_BIND") != "1" {
		log.Printf("ccfly control: WARNING bind %s is non-loopback and this service does NOT authenticate; "+
			"expose ONLY behind the cloud gateway (requireAuth + device ownership). "+
			"Set CCFLY_ALLOW_PUBLIC_BIND=1 to silence if this is intentional.", bind)
	}
	srv := &http.Server{Addr: bind, Handler: Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	go RunScanner(ctx) // 后台巡检:回收 cc-* 孤儿壳 + 预热扫描缓存(随 ctx 结束而停)
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
		// Clear:仅「提交一条消息」(Clear+Text+Enter)时由前端置真。语义是「原子提交」——
		// 在打字前先硬清空里世界当前输入行,杜绝 <里世界残留><本次 payload> 的拼接污染
		// (web/TUI 输入框状态分裂的整类 bug 之根因 A)。raw keys(菜单方向键/Space/Esc)与
		// 纯打字不带 Clear,故菜单导航/中断完全不受影响。
		Clear bool `json:"clear"`
		// Images:本次提交要「原生附图」进里世界输入框的、已落盘上传图片的**绝对路径**列表
		// (前端先经 /upload 落盘到会话 cwd 的 .ccfly-uploads/,再把返回的绝对路径填这里)。
		//
		// 真正的 Claude Code 体验是 **附图 → 输入框里出现 `[Image #N]` 占位**(原生嵌图、随消息一起带上、
		// 不提交不显路径)。复刻它**不靠系统剪贴板**(那要 GUI 登录会话,--system 守护进程拿不到),而靠
		// 「**括号粘贴(bracketed paste)其绝对路径**」—— 即终端「拖拽文件」的底层机制:tmux `set-buffer`
		// 把路径塞进 buffer、`paste-buffer -p`(带括号)粘进输入框,里世界一见是「粘进来的图片路径」就
		// 原生嵌成 `[Image #N]`(详见 handleSendKeys 的原子提交分支)。纯 tmux 往 PTY 注字节、与 GUI 无关,
		// 故 --system / headless 一样能用。仅在原子提交(Clear+Enter)时消费。
		Images []string `json:"images"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ctrlErr(w, 400, "bad json")
		return
	}
	if strings.TrimSpace(req.Session) == "" {
		ctrlErr(w, 400, "session required")
		return
	}
	// 解析到真正在跑的 tmux:前端 /clear 后仍按「新 sid」发来 cc-<Y[:8]>,这里落到真 pane cc-<X[:8]>。
	// stale = 真值表证实请求的会话已不在该 pane 里(/clear 后名字残留、pane 已易主)——
	// 一切发键(打字/具名键/提交)一律 409 拒发:往易主 pane 送任何键都是打进别人的对话
	// (Escape 都可能中断别人正在跑的回合)。前端冒「会话已被取代」并引导回列表。
	sess, stale := resolveSessionTarget(req.Session)
	if stale {
		ctrlJSON(w, 409, map[string]any{"ok": false, "kind": "stale"})
		return
	}
	// 原子提交判定:Clear+Enter,且「有文本 或 有待粘贴图片」。
	// 注意把「纯发图」(Text 空、Images 非空)也算作原子提交 —— 否则纯图消息既过不了 server floor、
	// 也走不进下面的图片原生粘贴分支,图片会被悄悄丢弃。server floor 与图片粘贴共用这一条件。
	atomicSubmit := req.Clear && req.Enter && (req.Text != "" || len(req.Images) > 0)
	// SERVER FLOOR(权威兜底,根因 B 的最后一道闸):仅对「原子提交」生效。
	// 在落键前重新抓一次实时画面、跑同一套 detectState;若此刻里世界不是 input 态(busy/select/offline),
	// 一律 409 拒发、原样不送任何键。这一步关掉「客户端视图陈旧/错上下文」的窗口:
	//   - 客户端 certain 门已经快速拒了大多数错时机(UX 友好),但它只信客户端那份可能陈旧的视图;
	//   - 降级轮询路径(~1.8s 陈旧)更可能误判;
	//   - 服务端这一刀是 WS-live 与 degraded-poll 共享的同一道权威闸 —— 即便客户端有 bug,
	//     往运行中的回合或权限菜单误发(灾难性的「自动批准权限菜单」)在结构上也不可能发生。
	// 仅提交分支过闸;raw keys / 纯打字不过(菜单导航、中断、实时打字必须永远可用)。
	// 代价:每次提交多一次 capture-pane(亚 100ms,仅提交时、非每键),对聊天提交完全可接受。
	if atomicSubmit {
		out, err := tmuxCmd("capture-pane", "-t", sess, "-p", "-e").CombinedOutput()
		// 抓屏失败(pane 不在)= offline,等同非 input,照样 409 拒发。
		kind := "offline"
		if err == nil {
			kind = detectState(string(out)).Kind
		}
		if kind != "input" {
			ctrlJSON(w, 409, map[string]any{"ok": false, "kind": kind})
			return
		}
	}
	// 合法图片路径:仅在原子提交分支、且 darwin 上才走「剪贴板 + C-v」原生粘贴通道(见下)。
	// 每条都先过 containment 终检(只允许会话 .ccfly-uploads/ 内的已上传文件),逐个解析其剪贴板类。
	// 非 darwin 没有可靠的「图片进剪贴板」方案 → 这些路径回退旧行为:把绝对路径当文本拼进消息(见 textPayload)。
	// 图片原生附图:把每张已过 containment 的上传图,以「括号粘贴(bracketed paste)其绝对路径」
	// 的方式注入里世界输入框 —— 这正是终端「拖拽文件」的底层机制。里世界 Claude Code 一旦收到
	// 「粘进来的图片文件路径」,就原生嵌成 [Image #N](干净占位、随消息带上、不显路径、不走 Read)。
	// 经本机实测(v2.1.168):tmux `paste-buffer -p`(带括号)送路径 → 输入框直接出 [Image #1]。
	//
	// 为何不再用「osascript 剪贴板 + C-v」:那依赖 GUI 登录会话的剪贴板,--system 守护进程拿不到;
	// 而括号粘贴是纯 tmux 往 PTY 注字节、与 GUI/剪贴板无关 → --system / headless 一样能用,全平台统一。
	// 优雅降级:万一某版 Claude 不再自动嵌图,路径就当文本落在框里 → 提交后 Claude 仍会 Read 它取图。
	var imgPaths []string // 已过 containment 的真实绝对路径,逐张括号粘贴
	if atomicSubmit && len(req.Images) > 0 {
		// containment 复用:uploadDirForSession 解析出的目录,与 upload.go 落盘口径**完全一致**
		// (resolveSessionParam 扛 /clear → 反查 sid → sidCwd 取冻结 cwd → <cwd>/.ccfly-uploads/,
		//  兜底 ~/.ccfly/uploads/)。validateUploadPath 再用与 upload.go 同款的 EvalSymlinks(dir)+
		// filepath.Rel 终检,确保每条 Images 路径真落在该目录之内 —— 绝不把任意本机路径粘给里世界。
		dir := uploadDirForSession(req.Session)
		for _, p := range req.Images {
			if real, ok := validateUploadPath(dir, p); ok {
				imgPaths = append(imgPaths, real)
			}
			// 越界/不存在路径静默跳过(纵深防御:决不粘未通过 containment 的路径)。
		}
	}

	// textPayload:真正要 `send-keys -l --` 打进去的字面文本(纯用户文本;图片不进文本、走括号粘贴)。
	textPayload := req.Text

	var cmds [][]string
	// CLEAR PRIMITIVE(根因 A):提交前先硬清空里世界当前输入行,再打字。
	// 用 `C-a C-k`(行首 + 杀到行尾)而非单个 C-u —— C-u 在部分 readline 模式下只杀「光标→行首」,
	// C-a 先把光标移到行首、C-k 再杀到行尾,无论光标在哪都确定性清空整行;空行上是安全 no-op、幂等。
	// 落键顺序变为:[C-a C-k] → [-l -- text] →(各图 括号粘贴其路径 → [Image #N])→ [Enter]:打字前行空、Enter 后行被消费,拼接结构上不可能。
	// 仅在提交分支(已被上面 server floor 过闸)生效;放在字面文本命令之前 PREPEND。
	if atomicSubmit {
		cmds = append(cmds, []string{"send-keys", "-t", sess, "C-a", "C-k"})
	}
	// -l = literal(原样字面),避免把 "/model" 之类当按键名解析;-- 终止选项解析。
	// 用 textPayload(darwin=纯用户文本;非 darwin=用户文本+回退路径)。空串则不打字(纯发图也成立)。
	if textPayload != "" {
		cmds = append(cmds, []string{"send-keys", "-t", sess, "-l", "--", textPayload})
	}
	// 具名键(Escape / C-c / Up …)不带 -l。
	if len(req.Keys) > 0 {
		args := append([]string{"send-keys", "-t", sess, "--"}, req.Keys...)
		cmds = append(cmds, args)
	}
	// 先把「clear + 打字 + 具名键」这批确定性 tmux 命令依次发出(图片粘贴在它们之后、Enter 之前)。
	if len(cmds) == 0 && len(imgPaths) == 0 && !req.Enter {
		ctrlErr(w, 400, "nothing to send")
		return
	}
	for _, c := range cmds {
		if out, err := tmuxCmd(c...).CombinedOutput(); err != nil {
			ctrlErr(w, 500, "tmux: "+strings.TrimSpace(string(out))+" ("+err.Error()+")")
			return
		}
	}

	// ── 图片原生附图:逐张「括号粘贴其绝对路径」──。对每张图:tmux set-buffer 把路径塞进一个具名
	// buffer,再 paste-buffer -p(-p=括号粘贴,模拟拖拽)把它粘进里世界输入框;里世界即把这条
	// 「粘进来的图片路径」原生嵌成 `[Image #N]`(不提交、不显路径、不走 Read)。-d 粘完即删该 buffer。
	// buffer 名**每次请求唯一**(imgBufferName):tmux buffer 全局共享,固定名会让并发带图提交互相
	// 覆盖(详见 imgBufferName 注释)。同一请求内多图顺序复用同一名无害。多图之间留一小段,确保里世界
	// 逐张吃进、序号(#1 #2 …)不串。
	// 注:imgPaths 均为 validateUploadPath 解出的绝对路径(必以 / 开头),故 set-buffer 直接传、无需 --。
	imgBuf := imgBufferName()
	//
	// 里世界把括号粘贴进来的图片路径**异步**嵌成的实时占位串(本机实测 v2.1.168 = `[Image #1]`);
	// 下面用它作「图已吃进」的就绪信号。注:这是**实时输入框**里的渲染形态,与落盘 transcript 的
	// `[Image: source: …]` 不同;若某版 Claude 改了这串,轮询数不到 → 落到超时兜底照常发 Enter(优雅降级)。
	const imgPlaceholder = "[Image #"
	// 基线-增量(baseline-delta):粘图前先抓一屏、数出当前可见区已有多少个占位(历史/上条消息、
	// 甚至用户本次文本里若含该串,此刻都已入账),作为基线;粘完只需等到「基线 + 本次张数」,
	// 绝不把旧占位误算成「已就绪」。仅在确有图要粘时抓 —— 纯文本提交零额外开销。
	imgBaseline := 0
	if len(imgPaths) > 0 {
		if out, err := tmuxCmd("capture-pane", "-t", sess, "-p").CombinedOutput(); err == nil {
			imgBaseline = strings.Count(string(out), imgPlaceholder)
		}
	}
	for i, p := range imgPaths {
		if out, err := tmuxCmd("set-buffer", "-b", imgBuf, p).CombinedOutput(); err != nil {
			ctrlErr(w, 500, "tmux: "+strings.TrimSpace(string(out))+" ("+err.Error()+")")
			return
		}
		if out, err := tmuxCmd("paste-buffer", "-p", "-d", "-b", imgBuf, "-t", sess).CombinedOutput(); err != nil {
			ctrlErr(w, 500, "tmux: "+strings.TrimSpace(string(out))+" ("+err.Error()+")")
			return
		}
		if i < len(imgPaths)-1 {
			time.Sleep(120 * time.Millisecond) // 多图间隔:让里世界逐张吃进、`[Image #N]` 序号不串
		}
	}

	// ── 关键修复(图片不发送的根因):粘完最后一张图后、发 Enter 之前,确定性地等到「图都吃进去了」──。
	// `paste-buffer -p` 是括号粘贴(ESC[200~…ESC[201~),里世界把粘进的路径**异步**嵌成 `[Image #N]`
	// (要识别成图片文件、登记附件、分配 #N、重渲输入行)。若紧贴最后一张 paste 就发 Enter,那个 \r 落在
	// 这段异步「粘贴处理」窗口里被吞掉而**不提交** —— 正是「框里有字+[Image #N] 却发不出去」的根因
	// (无图时走 send-keys -l 字面打字、无异步粘贴态,Enter 即刻提交、一直正常)。旧代码的 120ms 仅在
	// 「多图之间」生效(if i<len-1),**最后一张到 Enter 之间零间隔**,故偏偏漏掉它。
	//
	// 用 capture-pane 轮询替代盲睡:**先抓一次**(快路径:若已渲好立即收手发 Enter,近乎零额外延迟),
	// 数 `[Image #` 到达「基线 + 本次张数」即就绪;否则睡 200ms 再抓,最多 8 次(≈1.4s 兜底)。
	// 只信硬阈值 n>=want(刻意不做「连续两次相同即就绪」的提前收手——那会在多图增量渲染的间歇里误判、
	// 把 Enter 又抢在最后一张渲完前发出,等于重新引入本 bug)。超时也照常发 Enter,绝不挂起(优雅降级)。
	// 纯文本提交(len(imgPaths)==0)整段跳过,零额外延迟。
	if len(imgPaths) > 0 {
		want := imgBaseline + len(imgPaths)
		for poll := 0; poll < 8; poll++ {
			out, err := tmuxCmd("capture-pane", "-t", sess, "-p").CombinedOutput()
			if err != nil {
				break // 抓屏失败(pane 没了等)→ 不死等,落下面照常发 Enter
			}
			if strings.Count(string(out), imgPlaceholder) >= want {
				break // 本次的图都已渲成 [Image #N] → 吃进完毕,立刻发 Enter
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// 最后整体提交:Enter 是独立按键事件(非粘贴),里世界把「文本 + 各 [Image #N]」作一条消息发出。
	// 防空提交护栏:仅当是「纯发图」原子提交(无文本)且一张图都没过 containment(imgPaths 为空)时,
	//   框里空空如也 —— 此刻发 Enter 会提交一条空消息(打扰里世界)。故此情形吞掉 Enter,返回 ok:false。
	//   其余情形(有文本 / 至少粘了一张图的路径)照常发 Enter。
	if req.Enter && atomicSubmit && textPayload == "" && len(imgPaths) == 0 {
		ctrlJSON(w, 200, map[string]any{"ok": false, "kind": "input", "reason": "no image pasted"})
		return
	}
	if req.Enter {
		if out, err := tmuxCmd("send-keys", "-t", sess, "Enter").CombinedOutput(); err != nil {
			ctrlErr(w, 500, "tmux: "+strings.TrimSpace(string(out))+" ("+err.Error()+")")
			return
		}
	}
	ctrlJSON(w, 200, map[string]any{"ok": true})
}

// validateUploadPath 对一条客户端给的图片路径做与 upload.go **同款**的 containment 终检:
// 只允许它落在 dir(会话 .ccfly-uploads/,由 uploadDirForSession 解析)之内,抗符号链接 + 跨平台。
// 通过则返回该文件 EvalSymlinks 后的真实路径与 true;任何越界/不存在/可疑路径 → ("", false)。
//
// 安全要旨(与 upload.go 落盘终检对称,见其注释):决不能让不可信前端指定任意路径再去 osascript 读取
// (那等于「读任意本机文件进剪贴板」的提权)。故:
//   - 对 dir 与目标文件各做 EvalSymlinks 取真实物理路径(解开沿途所有 link,看穿预置的逃逸 symlink);
//   - 再用 filepath.Rel 判定文件真在该物理目录之内(Rel 结果不得为 ".." / 以 ".."+sep 开头 / 绝对路径);
//   - 目标必须是已存在的**普通文件**(EvalSymlinks 要求存在;额外拒目录/特殊文件)。
func validateUploadPath(dir, p string) (string, bool) {
	if strings.TrimSpace(p) == "" {
		return "", false
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", false // dir 不存在(本会话从未上传过)→ 无可信容器,一律拒
	}
	cleanDir := filepath.Clean(realDir)
	realFile, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", false // 文件不存在 / 路径里有断链 symlink:拒
	}
	if fi, err := os.Stat(realFile); err != nil || !fi.Mode().IsRegular() {
		return "", false // 非普通文件(目录/设备/FIFO…):拒,绝不 osascript 读
	}
	cleanFile := filepath.Clean(realFile)
	rel, err := filepath.Rel(cleanDir, cleanFile)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", false // 越界:不在 .ccfly-uploads/ 之内
	}
	return cleanFile, true
}

func handleCapture(w http.ResponseWriter, r *http.Request) {
	sess := r.URL.Query().Get("session")
	if sess == "" {
		ctrlErr(w, 400, "session required")
		return
	}
	sess = resolveSessionParam(sess) // 扛 /clear:解析到真正在跑的 tmux
	lines := r.URL.Query().Get("lines")
	if _, err := strconv.Atoi(lines); err != nil {
		lines = "2000"
	}
	// 一律 -p(打印到 stdout);?ansi=1 时**追加** -e(保留 TUI 原始 ANSI 上色,展示/原始回退用)。
	// 关键:-e 必须与 -p 同用 —— 单独 `capture-pane -e`(无 -p)会把内容写进 tmux paste buffer 而非 stdout,
	// 于是 HTTP 返回空串,信息卡(/cost /status /mcp …)抓屏永远拿不到内容 → 解析失败 →「未能打开」。
	args := []string{"capture-pane", "-t", sess, "-p", "-S", "-" + lines}
	if r.URL.Query().Get("ansi") == "1" {
		args = append(args, "-e")
	}
	out, err := tmuxCmd(args...).CombinedOutput()
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
//
//	base64 式 → 解码后按 media_type 返回;路径式 → 读文件返回(Content-Type 按扩展名)。
//
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
	// 扛 /clear:解析到真正在跑的 tmux(否则 /clear 后总判 offline)。
	// stale(pane 已易主)→ 对请求的那个会话而言就是 offline:别把别人会话的 input 态
	// 误报给它(那会诱导前端放行提交,再被 /sendkeys 的 409 拦——不如源头就说不可用)。
	sess, stale := resolveSessionTarget(sess)
	if stale {
		ctrlJSON(w, 200, ctrlState{Kind: "offline"})
		return
	}
	// -e 保留 ANSI 上色:detectState 内部对各判定先剥色,但「输入建议」靠 dim 属性识别,需带色原文。
	out, err := tmuxCmd("capture-pane", "-t", sess, "-p", "-e").CombinedOutput()
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
	// 扛 /clear:若该 sid 其实是某个在跑 pane 的「当前会话」(名字已陈旧),解析到那个真 pane。
	// 解析后名字已在跑 → 视作已启动(幂等返回 ok),不再 new-session(否则 tmux 报 duplicate)。
	// stale = 同名 tmux 在跑但已易主(跑着别的会话):既不能谎称 already,也无法同名新建 → 409。
	sess, stale := resolveSessionTarget(req.Session)
	if stale {
		ctrlJSON(w, 409, map[string]any{"ok": false, "kind": "stale"})
		return
	}
	if tmuxSessionLive(sess) {
		ctrlJSON(w, 200, map[string]any{"ok": true, "already": true})
		return
	}
	// 调用方没给 cmd → 自动起 claude --resume(同 /term 口径),把离线会话按原始 cwd 拉活成 claude
	// 而非裸壳(这正是 /start 的用途)。上面的 tmuxSessionLive 短路已保证不重启在跑会话。
	if req.Cmd == "" {
		if snaps, e := scanClaudeSessions(); e == nil {
			if c, cw, ok := claudeResumeCmd(sess, snaps); ok {
				req.Cmd = c
				if req.Cwd == "" {
					req.Cwd = cw
				}
			}
		}
	}
	// -u:UTF-8 客户端(防中文/符号被降级成 '_');-e 代理环境注入(CCFLY_TMUX_PROXY 配了才有,见 proxyenv.go)。
	args := append([]string{"-u", "new-session", "-d"}, tmuxProxyEnvArgs()...)
	args = append(args, "-s", sess)
	if req.Cwd != "" {
		args = append(args, "-c", req.Cwd)
	}
	if req.Cmd != "" {
		args = append(args, req.Cmd)
	}
	if out, err := tmuxCmd(args...).CombinedOutput(); err != nil {
		ctrlErr(w, 500, "tmux: "+strings.TrimSpace(string(out))+" ("+err.Error()+")")
		return
	}
	ctrlJSON(w, 200, map[string]any{"ok": true})
}

// handleDirs — GET /dirs?path=<abs>:列某路径下的子目录(新建会话的目录浏览器用)。
// path 空 → 用户家目录。返回 {path, parent, dirs:[name...]}(只列子目录,跳过隐藏)。
func handleDirs(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			path = h
		} else {
			path = "/"
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		ctrlErr(w, 400, "bad path")
		return
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		ctrlErr(w, 400, "cannot read dir: "+err.Error())
		return
	}
	dirs := []string{}
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // 跳过隐藏目录
		}
		if e.IsDir() {
			dirs = append(dirs, name)
		} else if e.Type()&os.ModeSymlink != 0 {
			if fi, err := os.Stat(filepath.Join(abs, name)); err == nil && fi.IsDir() {
				dirs = append(dirs, name)
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i]) < strings.ToLower(dirs[j]) })
	ctrlJSON(w, 200, map[string]any{"path": abs, "parent": filepath.Dir(abs), "dirs": dirs})
}

// handleNew — POST /new {cwd, permission_mode?, skip_permissions?}:在 cwd detached 起一个**全新**
// claude(非 resume),随后轮询 panemap 等 SessionStart hook 写入 pane→sid,返回真 sid。
// 前端据此导航到 /d/<device>/<sid>,后续按 sid 走既有镜像/转写主路;sid 暂未就绪也返回 tmux 名兜底。
func handleNew(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cwd            string            `json:"cwd"`
		PermissionMode string            `json:"permission_mode"`
		SkipPerms      bool              `json:"skip_permissions"`
		EnvVars        map[string]string `json:"env_vars,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ctrlErr(w, 400, "bad json")
		return
	}
	cwd := strings.TrimSpace(req.Cwd)
	if cwd == "" {
		cwd = "."
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		ctrlErr(w, 400, "not a directory: "+cwd)
		return
	}
	suffix, err := claudePermSuffix(req.SkipPerms, req.PermissionMode)
	if err != nil {
		ctrlErr(w, 400, err.Error())
		return
	}
	trustFolder(cwd) // 用户显式选了此目录 → 预信任,跳过「信任此文件夹」对话框(否则挡住 SessionStart)
	// sid 由我们预生成并经 `claude --session-id` 强制指定:/new 无需再轮询 panemap-hook 回传,
	// 会话名直接就是规范的 cc-<sid8>。这同时解决 Windows(psmux 不设 TMUX_PANE,hook 注册不了)
	// 下 /new 永远拿不到 sid → 前端按名解析 404 → 「连接中」卡死的问题;unix 下 hook 照常补真值表。
	sid := newSessionUUID()
	name := defaultTmuxName(sid)
	if tmuxSessionLive(name) { // sid 前 8 位撞上活会话(概率极低):换随机名+新 sid 再试一次
		sid = newSessionUUID()
		name = defaultTmuxName(sid)
		if tmuxSessionLive(name) {
			ctrlErr(w, 500, "tmux session name collision")
			return
		}
	}
	args := append([]string{"-u", "new-session", "-d"}, tmuxProxyEnvArgs()...)
	args = append(args, sandboxEnvArgs(req.SkipPerms)...) // root + skip-permissions → IS_SANDBOX=1 放行
	for k, v := range req.EnvVars {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, "-s", name, "-c", cwd, "claude"+suffix+" --session-id "+sid)
	if out, e := tmuxCmd(args...).CombinedOutput(); e != nil {
		ctrlErr(w, 500, "tmux: "+strings.TrimSpace(string(out))+" ("+e.Error()+")")
		return
	}
	// 直接把「pane → sid」写进真值表(不等 hook):查刚建会话的 pane id,best-effort 登记。
	// unix 上 SessionStart hook 稍后会以同 sid 幂等覆写;Windows(hook 因 TMUX_PANE 缺失而失效)
	// 则全靠这里。查询失败不阻塞返回 —— 读取端还有「名字前缀 ↔ 扫描快照」兜底。
	if out, e := tmuxCmd("list-panes", "-t", name, "-F", "#{pane_id}").Output(); e == nil {
		if paneID := strings.TrimSpace(string(out)); paneID != "" {
			registerPaneMapEntry(paneID, sid, name, cwd)
		}
	}
	ctrlJSON(w, 200, map[string]any{"ok": true, "session": name, "session_id": sid})
}

// handleReload — POST /reload {session, env_vars?, cwd?}:杀掉会话对应的 tmux 会话后以
// claude --resume 重建,可注入新的 env_vars(例如更新代理/环境变量)。
// 典型用途:用户更改了出网配置,需要对已有会话「热重载」而非新建。
// 流程:(1) 找到 cwd(优先 req.Cwd,否则从 scanClaudeSessions 取原始 cwd);
//
//	(2) tmux kill-session 删旧 pane;
//	(3) tmux new-session -d 注入 env_vars + proxy env + claude --resume <sid>。
func handleReload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string            `json:"session"`
		EnvVars map[string]string `json:"env_vars,omitempty"`
		Cwd     string            `json:"cwd,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ctrlErr(w, 400, "bad json")
		return
	}
	sess := strings.TrimSpace(req.Session)
	if sess == "" {
		ctrlErr(w, 400, "session required")
		return
	}

	// 先从 scanClaudeSessions 拿 cmd + cwd(kill 之前),以免杀掉后数据丢失。
	var resumeCmd, cwd string
	if snaps, e := scanClaudeSessions(); e == nil {
		if c, cw, ok := claudeResumeCmd(sess, snaps); ok {
			resumeCmd = c
			if req.Cwd != "" {
				cwd = req.Cwd
			} else {
				cwd = cw
			}
		}
	}
	if cwd == "" && req.Cwd != "" {
		cwd = req.Cwd
	}
	if resumeCmd == "" {
		ctrlErr(w, 400, "cannot determine resume command for session: "+sess)
		return
	}

	// kill 旧 tmux 会话(进程 + pane 一并清除)。
	if out, e := tmuxCmd("kill-session", "-t", sess).CombinedOutput(); e != nil {
		// 会话已不存在也允许继续(视为已死)。
		_ = out
	}

	// 重建:注入 proxy env + 用户 env_vars + claude --resume <sid>。
	args := append([]string{"-u", "new-session", "-d"}, tmuxProxyEnvArgs()...)
	for k, v := range req.EnvVars {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, "-s", sess)
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	// --resume 的会话会保留**存档时**的模型并无视 ANTHROPIC_MODEL env(见 Claude Code model-config
	// 文档:"keep the model they were using when the transcript was saved")。故改模型必须额外用 --model
	// flag(优先级高于 env);env 与 flag 两条腿一起走,最大化「重载即换模型」的成功率。模型值取自前端预设
	// (claude-opus-4-8 等,无空格/元字符),tmux 按词拆 cmd 直接 exec,单串拼接安全。
	if m := strings.TrimSpace(req.EnvVars["ANTHROPIC_MODEL"]); m != "" {
		resumeCmd += " --model " + m
	}
	args = append(args, resumeCmd)
	if out, e := tmuxCmd(args...).CombinedOutput(); e != nil {
		ctrlErr(w, 500, "tmux: "+strings.TrimSpace(string(out))+" ("+e.Error()+")")
		return
	}
	ctrlJSON(w, 200, map[string]any{"ok": true})
}

// newTmuxName 生成 cc-<rand8> 的 tmux 会话名(与 `ccfly new` 同口径;真 sid 由 SessionStart hook 登记)。
func newTmuxName() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("cc-%08x", imgBufSeq.Add(1))
	}
	return "cc-" + hex.EncodeToString(b)
}

// newSessionUUID 生成 UUIDv4(给 `claude --session-id` 预指定 sid;/new 据此免等 hook)。
func newSessionUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", imgBufSeq.Add(1))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// sandboxEnvArgs 在「以 root 跑 + 用了 --dangerously-skip-permissions」时返回 tmux 的
// `-e IS_SANDBOX=1`。Claude Code 默认拒绝 root 下的 skip-permissions(安全保护:报
// "cannot be used with root/sudo privileges"),IS_SANDBOX=1 是其文档化的放行开关。
// 非 root 无需(skip 原生可用),不注入以免改变行为。tmux 服务端以本进程同一用户跑,故
// os.Geteuid() 即将来 claude 的 uid。
func sandboxEnvArgs(skip bool) []string {
	if skip && os.Geteuid() == 0 {
		return []string{"-e", "IS_SANDBOX=1"}
	}
	return nil
}

// SandboxEnvArgs 导出给 cmd/ccfly(ccfly new / picker 的 newSession)复用同一判定。
func SandboxEnvArgs(skip bool) []string { return sandboxEnvArgs(skip) }

// trustFolder 预备 ~/.claude.json,让 ccfly 起的 claude 会话不被 TUI 的两道拦截挡在 SessionStart:
//
//	(a) 标记首启引导已完成(hasCompletedOnboarding + theme)—— 跳过「选主题/登录方式」设置向导;
//	    凭证有了也不够,新登录的设备/实例首启仍会弹这个向导(login 容器里也是这么 seed 的)。
//	(b) 预登记 dir 为已信任(projects[abs].hasTrustDialogAccepted=true)—— 用户经目录浏览器显式选了它,
//	    即已表达信任,免掉「是否信任此文件夹」那次点击。dir 为空则只做 (a)。
//
// **文件不存在时会创建**(claude 首启会读它 → 直接跳过两道拦截);这是相比旧版的关键修正 —— 旧版缺文件
// 就返回,全新设备连 trust 都补不上。失败安全:已有文件解析不了就不动它(绝不损坏主配置);原子写。
func trustFolder(dir string) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	path := filepath.Join(home, ".claude.json")
	doc := map[string]any{}
	if data, e := os.ReadFile(path); e == nil {
		if json.Unmarshal(data, &doc) != nil {
			return // 已有文件解析不了 → 绝不改写,避免损坏
		}
	}
	changed := false
	if v, _ := doc["hasCompletedOnboarding"].(bool); !v {
		doc["hasCompletedOnboarding"] = true
		changed = true
	}
	if _, ok := doc["theme"]; !ok {
		doc["theme"] = "dark"
		changed = true
	}
	if abs, e := filepath.Abs(dir); e == nil && strings.TrimSpace(dir) != "" {
		projects, ok := doc["projects"].(map[string]any)
		if !ok || projects == nil {
			projects = map[string]any{}
			doc["projects"] = projects
		}
		proj, ok := projects[abs].(map[string]any)
		if !ok || proj == nil {
			proj = map[string]any{}
			projects[abs] = proj
		}
		if t, _ := proj["hasTrustDialogAccepted"].(bool); !t {
			proj["hasTrustDialogAccepted"] = true
			changed = true
		}
	}
	if !changed {
		return // 两道都已就绪
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".ccfly-trust-tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path) // 原子替换
}

// TrustFolder 导出给 cmd/ccfly(newSession)复用。
func TrustFolder(dir string) { trustFolder(dir) }

// claudePermSuffix 把权限选项展开成追加到 `claude` 的命令行后缀(校验 permission_mode 取值)。
func claudePermSuffix(skip bool, mode string) (string, error) {
	if skip {
		return " --dangerously-skip-permissions", nil
	}
	switch mode {
	case "", "default":
		return "", nil
	case "acceptEdits", "plan", "bypassPermissions":
		return " --permission-mode " + mode, nil
	default:
		return "", fmt.Errorf("invalid permission_mode %q (want default|acceptEdits|plan|bypassPermissions)", mode)
	}
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
//		firstCursor = 本批**最旧** item 所在行的**起始**字节;前端下次向上分页用 ?before=<firstCursor> 无缝接续(不重不漏)。
//		hasMore     = 是否还有更老的 item(可继续向上翻)。
//		cursor      = 向后增量游标(本批最新边界):首拉/before=窗口右端,since=EOF。前端 SSE 跟随仍用最新的 cursor。
//
//	 1. 首拉(无 since、无 before)= 尾窗:返回末尾最多 150 条(且 ~4MB 字节预算,先到为准)。
//	    → {items, cursor:EOF, firstCursor:本批最旧行首, hasMore:firstCursor>0}
//	 2. 向上分页(?before=<byte>):返回紧邻 before 之前的末尾最多 150 条(更老的一窗)。
//	    → {items, cursor:before, firstCursor:本批最旧行首, hasMore}
//	 3. 增量更新(?since=<byte>,保持现有语义):从 since 读到 EOF。
//	    → {items, cursor:EOF, firstCursor:since, hasMore:false}
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
