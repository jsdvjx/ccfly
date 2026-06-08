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
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

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
	mux.HandleFunc("POST /upload", handleUpload)    // 表世界图片/文件上传 → 落盘会话 cwd 的 .ccfly-uploads/(见 upload.go)
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
		// Images:本次提交要「原生粘贴」进里世界输入框的、已落盘上传图片的**绝对路径**列表
		// (前端先经 /upload 落盘到会话 cwd 的 .ccfly-uploads/,再把返回的绝对路径填这里)。
		//
		// 为什么不再像旧版那样把路径当文本拼进消息:把 "/abs/path.png" 当字面文本打进去,里世界
		// Claude 只是「看到一条含路径的文字」,并不一定把它当图读;而真正的 Claude Code 体验是
		// **粘贴图片 → 输入框里出现 `[Image #N]` 占位**(原生嵌图、随消息一起带上、不提交不显路径)。
		// 经本机实测,这一原生嵌图可被复刻:把目标图片塞进**系统剪贴板**,再向 tmux 会话发一次
		// `C-v`,里世界即吐出 `[Image #N]`(详见 handleSendKeys 的原子提交分支)。故 Images 走的是
		// 「设置 OS 剪贴板 → C-v」这条原生粘贴通道,而非文本拼接。仅在原子提交(Clear+Enter)时消费。
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
	sess := resolveSessionParam(req.Session)
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
		out, err := exec.Command("tmux", "capture-pane", "-t", sess, "-p", "-e").CombinedOutput()
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
	var pasteImgs []imgClip // darwin:要 设置剪贴板→C-v 的图片(已过 containment)
	var fallbackPaths []string // 非 darwin:回退「路径拼进文本」的图片
	if atomicSubmit && len(req.Images) > 0 {
		// containment 复用:uploadDirForSession 解析出的目录,与 upload.go 落盘口径**完全一致**
		// (resolveSessionParam 扛 /clear → 反查 sid → sidCwd 取冻结 cwd → <cwd>/.ccfly-uploads/,
		//  兜底 ~/.ccfly/uploads/)。validateUploadPath 再用与 upload.go 同款的
		// EvalSymlinks(dir)+filepath.Rel 终检,确保每条 Images 路径真落在该目录之内 ——
		// 绝不 osascript 读任意路径(否则等于把「读任意本机文件进剪贴板」的能力开给不可信前端)。
		dir := uploadDirForSession(req.Session)
		for _, p := range req.Images {
			real, ok := validateUploadPath(dir, p)
			if !ok {
				continue // 静默跳过越界/不存在路径(纵深防御:决不读未通过 containment 的路径)
			}
			// darwin + 有 GUI 会话:走「剪贴板 + C-v」原生粘贴(干净的 [Image #N]、原生视觉、无需 Read 工具)。
			// `--system` 守护进程没有 GUI 登录会话的剪贴板(osascript 写的与里世界 Claude 读的不是同一块,
			// asuser 也注入不进)→ 安装时置 CCFLY_IMAGE_PATHS=1(见 svc.go installDarwin),改走与 Linux
			// 相同的回退:把图的绝对路径当文本发进去,Claude 用 Read 工具读图(上传落在会话 cwd 内的
			// .ccfly-uploads/,Read 默认放行、不弹权限)—— 实测可正确读出图片内容。
			if runtime.GOOS == "darwin" && os.Getenv("CCFLY_IMAGE_PATHS") != "1" {
				pasteImgs = append(pasteImgs, imgClip{path: real, class: pngfClassForExt(real)})
			} else {
				// 非 darwin / --system:无可靠图片剪贴板,回退「路径拼进文本」(空格分隔),Claude 读路径取图。
				fallbackPaths = append(fallbackPaths, real)
			}
		}
	}

	// textPayload:真正要 `send-keys -l --` 打进去的字面文本。darwin 走原生粘贴(图不进文本);
	// 非 darwin 回退时,把 fallbackPaths 以空格接在用户文本之后(与旧版一致:全程单行、无换行字节)。
	textPayload := req.Text
	if len(fallbackPaths) > 0 {
		textPayload = strings.TrimSpace(strings.Join(append([]string{req.Text}, fallbackPaths...), " "))
	}

	var cmds [][]string
	// CLEAR PRIMITIVE(根因 A):提交前先硬清空里世界当前输入行,再打字。
	// 用 `C-a C-k`(行首 + 杀到行尾)而非单个 C-u —— C-u 在部分 readline 模式下只杀「光标→行首」,
	// C-a 先把光标移到行首、C-k 再杀到行尾,无论光标在哪都确定性清空整行;空行上是安全 no-op、幂等。
	// 落键顺序变为:[C-a C-k] → [-l -- text] →(各图 剪贴板+C-v)→ [Enter]:打字前行空、Enter 后行被消费,拼接结构上不可能。
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
	if len(cmds) == 0 && len(pasteImgs) == 0 && !req.Enter {
		ctrlErr(w, 400, "nothing to send")
		return
	}
	for _, c := range cmds {
		if out, err := exec.Command("tmux", c...).CombinedOutput(); err != nil {
			ctrlErr(w, 500, "tmux: "+strings.TrimSpace(string(out))+" ("+err.Error()+")")
			return
		}
	}

	// ── 图片原生粘贴(darwin)──:对每张已过 containment 的图,先把它塞进系统剪贴板,再发一次 C-v。
	// 里世界 Claude Code 收到 C-v 时从剪贴板取图,在输入框就地吐出 `[Image #N]` 占位(原生嵌图,
	// 不提交、不显路径)—— 这正是「真实粘贴图片」的体验,经本机实测可复刻(见 Images 字段注释)。
	// 时序:osascript 设剪贴板与 C-v 之间留 ~150ms 让剪贴板写入落定(否则 C-v 可能取到上一张/空),
	// 多图之间也留一小段,确保里世界逐张吃进、序号(#1 #2 …)不串。
	for i, im := range pasteImgs {
		if err := setClipboardImageDarwin(im.path, im.class); err != nil {
			// 单张设剪贴板失败不毁整条提交:跳过这张,继续其余(已打的文本/已粘的图仍在框里)。
			log.Printf("ccfly control: set clipboard for %s failed: %v (skip this image)", im.path, err)
			continue
		}
		time.Sleep(150 * time.Millisecond) // 剪贴板写入落定窗口
		if out, err := exec.Command("tmux", "send-keys", "-t", sess, "C-v").CombinedOutput(); err != nil {
			ctrlErr(w, 500, "tmux: "+strings.TrimSpace(string(out))+" ("+err.Error()+")")
			return
		}
		if i < len(pasteImgs)-1 {
			time.Sleep(150 * time.Millisecond) // 多图间隔:让里世界逐张吃进、`[Image #N]` 序号不串
		}
	}

	// 最后整体提交:Enter 是独立按键事件(非粘贴),里世界把「文本 + 各 [Image #N]」作一条消息发出。
	// 防空提交护栏:仅当是「纯发图」原子提交(无文本、无回退路径)且最终一张图都没真粘进去
	//   (全部图片未过 containment / 设剪贴板失败)时,框里其实空空如也 —— 此刻发 Enter 会提交一条
	//   空消息(打扰里世界)。故此情形下吞掉 Enter,返回 ok:false 让前端知道「图片未粘成、未提交」。
	//   其余情形(有文本 / 至少粘进一张图 / 普通非提交回车)照常发 Enter。
	if req.Enter && atomicSubmit && textPayload == "" && len(pasteImgs) == 0 {
		ctrlJSON(w, 200, map[string]any{"ok": false, "kind": "input", "reason": "no image pasted"})
		return
	}
	if req.Enter {
		if out, err := exec.Command("tmux", "send-keys", "-t", sess, "Enter").CombinedOutput(); err != nil {
			ctrlErr(w, 500, "tmux: "+strings.TrimSpace(string(out))+" ("+err.Error()+")")
			return
		}
	}
	ctrlJSON(w, 200, map[string]any{"ok": true})
}

// imgClip 描述一张待原生粘贴的图片:已过 containment 的真实路径 + 其 AppleScript 剪贴板类(«class …»)。
type imgClip struct {
	path  string // EvalSymlinks 解析后的真实绝对路径(已确认落在会话 .ccfly-uploads/ 内)
	class string // PNGf / JPEG / GIFf / TIFF(按扩展名选,见 pngfClassForExt)
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

// pngfClassForExt 按扩展名选 AppleScript 剪贴板图片类(«class …»)。剪贴板里的「图片数据」
// 需带正确的类型标识里世界才认得;未知扩展默认按 PNGf(最常见、上传截图多为 png)。
func pngfClassForExt(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png":
		return "PNGf"
	case ".jpg", ".jpeg":
		return "JPEG"
	case ".gif":
		return "GIFf"
	case ".tif", ".tiff":
		return "TIFF"
	default:
		return "PNGf"
	}
}

// setClipboardImageDarwin 在 macOS 上把一张图片文件读进系统剪贴板(供随后的 C-v 原生粘贴)。
// 实现:osascript 跑一句 AppleScript —— `set the clipboard to (read (POSIX file "<p>") as «class PNGf»)`。
//
// 注入安全(关键):path 来自不可信前端,绝不能拼进 shell(已用 exec.Command 传参、无 shell),
// 但它会进入 AppleScript 源串,故必须对 AppleScript 字符串字面做正确转义 —— 见 appleScriptQuote:
// 把反斜杠与双引号转义后用双引号包起来,这样含空格/引号/反斜杠的路径也不会越出字符串字面、
// 更不可能注入 AppleScript 语句。class(PNGf/JPEG/…)来自我们自己的白名单(pngfClassForExt),非客户端可控。
// osascript 退出非 0(读图失败/类型不符)→ 返回 err,调用方据此跳过该图、不发 C-v。
func setClipboardImageDarwin(p, class string) error {
	// 形如:set the clipboard to (read (POSIX file "<quoted-path>") as «class PNGf»)
	// «» 是 0xC2AB / 0xC2BB(法文角引号),AppleScript 的原始类标识语法;直接写进源串即可。
	script := "set the clipboard to (read (POSIX file " + appleScriptQuote(p) + ") as «class " + class + "»)"
	// 只有「有 GUI 登录会话」的安装才会走到这里(交互式 `ccfly serve` 或用户级 LaunchAgent):
	// 此时直跑 osascript 写的就是里世界 Claude 读的同一块剪贴板,随后的 C-v 即可粘到图。
	// `--system` 守护进程没有 GUI 会话剪贴板,改走「路径拼文本」回退(CCFLY_IMAGE_PATHS=1),不会调用本函数。
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// appleScriptQuote 把任意字符串安全地转成一个 AppleScript 双引号字符串字面:
// 反斜杠 \ → \\,双引号 " → \",再两端包双引号。这样不可信路径无论含什么字符,都被关在字符串
// 字面里,既不会提前闭合字符串、也无从拼出可执行的 AppleScript 语句(切断 osascript 注入面)。
func appleScriptQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
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
	out, err := exec.Command("tmux", args...).CombinedOutput()
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
	sess = resolveSessionParam(sess) // 扛 /clear:解析到真正在跑的 tmux(否则 /clear 后总判 offline)
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
	// 扛 /clear:若该 sid 其实是某个在跑 pane 的「当前会话」(名字已陈旧),解析到那个真 pane。
	// 解析后名字已在跑 → 视作已启动(幂等返回 ok),不再 new-session(否则 tmux 报 duplicate)。
	sess := resolveSessionParam(req.Session)
	if tmuxSessionLive(sess) {
		ctrlJSON(w, 200, map[string]any{"ok": true, "already": true})
		return
	}
	args := []string{"new-session", "-d", "-s", sess}
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
