package control

// claudescan.go — 扫描本地 Claude 会话 jsonl 派生进度快照,以及被 transcript / info /
// subagents / workflow 共用的底层解析(rawEvent / contentKindPreview / scanSession 等)。
//
// 纯本地实现:不向任何云端上报,只做本地扫描 + 共享解析原语。
// claudeProjectsDir 在 config.go(可被 --claude-dir / CCFLY_CLAUDE_DIR 覆盖)。

import (
	"bufio"
	"encoding/json"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// jsonlLines 以 \n 分隔逐行迭代 r,**行长无上限** —— 取代 bufio.Scanner。
// 病灶:bufio.Scanner 的固定缓冲上限(本仓多处 16/64MB)在遇到超大行(内联 base64 图、巨型
// 工具结果/快照)时**静默截断该行,且其后所有行都不可见**;多处旧代码还没查 sc.Err(),无从察觉
// —— 结果是同一文件不同端点解析出的内容不一致、transcript/info/subagent/图片在某条大行后凭空消失。
// 改用与 readTranscriptSteps 同口径的 bufio.Reader.ReadBytes:无上限,不会截断。
//
// 语义:行尾 \n/\r\n 已剥除;空行跳过;末尾无 \n 的半截行(正在追加写)也会产出,与旧 Scanner
// 一致(调用方 json 解析失败自会跳过)。读到 EOF 或任何读错误即停止(本地可信文件,读错误极罕见;
// 「遇错即止」与旧 Scanner 同行为)。yield 返回 false 即提前结束(找到即停,省去读完整个文件)。
//
// 取舍:无上限意味着一条 200MB 的行会整行读进内存(旧上限会丢弃)。源是用户本机 ~/.claude 可信
// 数据,这是「正确优先于省内存」——宁可多占一次瞬时内存,也不静默吞掉那行之后的全部历史。
func jsonlLines(r io.Reader) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		br := bufio.NewReader(r)
		for {
			line, err := br.ReadBytes('\n')
			if n := len(line); n > 0 {
				for n > 0 && (line[n-1] == '\n' || line[n-1] == '\r') {
					n--
				}
				if n > 0 && !yield(line[:n]) {
					return
				}
			}
			if err != nil {
				return // EOF 或读错:停止(末尾半截行上面已产出)
			}
		}
	}
}

// scanLineCap 是**摘要扫描**(scanSession→applyEvent)的单行保留上限。摘要只需 role/kind/preview/counts;
// claude 会话偶发的超大行(内联 base64 图、巨型 tool_result,可达数十~上百 MB)若整行读进内存,在
// /sessions 并发扫描下会 ×并发数 放大到数 GB(2026-07:单机 315 并发把 ccfly 顶到 8G、拖垮整机的实案)。
// 故摘要路径给单行设上限。全文渲染(transcript/subagents/图片)仍走无上限的 jsonlLines,不受影响。
const scanLineCap = 1 << 20 // 1 MiB:足够解析正常消息元数据,截掉病态大行

// readLineAt 读到下一个 '\n'(含)为止,供**增量摘要扫描**按字节偏移续读用。返回:剥除尾部 \n/\r 的
// 行内容(最多 max 字节;超限行只留前 max 字节 → json 多半解析失败被跳过,但**不丢后续行**);consumed=
// 本行消耗的原始字节数(含被截断丢弃的溢出 + '\n');complete=是否读到了 '\n'(false=EOF 前的半截行,
// 尚在追加写 → 调用方不处理、不推进偏移,下轮写完再读)。max 上限的病态大行背景见 scanLineCap。
func readLineAt(br *bufio.Reader, max int) (line []byte, consumed int64, complete bool) {
	for {
		chunk, err := br.ReadSlice('\n') // chunk 是 br 内部缓冲视图,下次读前拷走
		consumed += int64(len(chunk))
		if len(line) < max {
			if room := max - len(line); len(chunk) > room {
				line = append(line, chunk[:room]...)
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue // 本行超 bufio 缓冲、还没到 \n:继续(已按预算保留/丢弃)
		}
		if err != nil {
			return line, consumed, false // EOF 等:本行无 '\n' = 半截行
		}
		n := len(line) // 读到 '\n':剥尾部 \n/\r
		for n > 0 && (line[n-1] == '\n' || line[n-1] == '\r') {
			n--
		}
		return line[:n], consumed, true
	}
}

// claudeSnapshot 是单个 Claude 会话的进度快照(本地扫描用)。
type claudeSnapshot struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	GitBranch string `json:"git_branch,omitempty"`
	Title     string `json:"title,omitempty"` // aiTitle
	MsgCount  int    `json:"msg_count"`
	Turns     int    `json:"turns"`           // 用户提问轮数(排除工具结果回传)
	Tokens    int    `json:"tokens"`          // 当前上下文占用(最后一条 assistant 的 input+cache)
	Model     string `json:"model,omitempty"` // opus / sonnet / haiku
	LastTs    string `json:"last_ts"`
	LastRole  string `json:"last_role"` // user / assistant
	LastKind  string `json:"last_kind"` // text / tool_use / tool_result
	Preview   string `json:"preview"`
	State     string `json:"state"`   // working / awaiting_input / idle / error / closed / unknown
	AgeSec    int64  `json:"age_sec"` // 距最后活动秒数
	Bytes     int64  `json:"bytes"`   // jsonl 文件字节数(会话大小);资源占用/resume 冷加载耗时与之强相关,前端据此判该关哪个
}

// ── 扫描缓存(claudecache):按 mtime+size 免重扫 idle 会话,增长文件只读新尾 ─────────────
// jsonl 是 append-only:任何新行都改 size(通常也改 mtime)。绝大多数 idle 会话每轮 (mtimeNs,size)
// 命中,O(ReadDir) 取代 O(读全部行)。**正在增长的会话**过去每轮 miss → 重读整个文件(几百 MB 的活跃
// 会话就是 ccfly CPU 高的根因);现改为**增量**:记住已消费到的行边界偏移 off,增长时只读 [off, size)
// 的新尾、从上次快照累进(计数累加、last_* 取新尾),成本从「读全文」降到「读新增几 KB」。
type scanCacheEntry struct {
	mtimeNs int64
	size    int64 // 上次扫描时的文件字节数(命中判定 + 增量「是否增长」判定)
	off     int64 // 已消费到的行边界字节偏移(末尾半截行不计入)——增量从此处续读
	snap    claudeSnapshot
	ok      bool // MsgCount>0;false 仍缓存以免反复开空文件
}

var (
	scanMu    sync.Mutex // 守 scanCache;仅护 map 读写,绝不跨 scanSession(磁盘/解析)持有
	scanCache = map[string]scanCacheEntry{}

	memoMu       sync.Mutex // 守整轮结果 memo + scanInflight(与 scanMu 独立)
	memoSnaps    []claudeSnapshot
	memoAt       time.Time
	scanInflight *scanCall // 非 nil = 已有一轮全量扫描在跑;后到的并发调用等它、共享结果(single-flight)
)

// memoTTL 远小于 useSessions 的 5s 轮询:只合并「同一刻多端点扇出」,绝不放陈旧。
const memoTTL = 800 * time.Millisecond

// scanCall 是一轮进行中的全量扫描的共享句柄:leader 跑扫描,期间到达的调用方 <-done 后读同一份结果,
// 避免 N 个并发请求各自全量解析全部会话 jsonl → 峰值内存 ×N 放大(2026-07:单机 315 并发 /sessions
// 把 ccfly 顶到 8G、swap 抖动拖垮整机的根因)。
type scanCall struct {
	done  chan struct{}
	snaps []claudeSnapshot
	err   error
}

// scanClaudeSessions 扫描 <claudeProjectsDir>/**/*.jsonl,每个会话出一个快照。三层挡并发/重扫:
// ① 800ms 整轮 memo 快路径(窗口内多端点 /sessions + resolveSessionParam×N + /sse 共用一次结果);
// ② single-flight:一轮扫描进行中,后到者等它共享结果、绝不自己再全量扫一遍;③(leader 内)按
// (mtime+size) 逐文件缓存,只有变动/新增文件才付全行扫描。返回值恒为拷贝,绝不外泄内部切片。
func scanClaudeSessions() ([]claudeSnapshot, error) {
	memoMu.Lock()
	// ① memo 快路径。
	if memoSnaps != nil && time.Since(memoAt) < memoTTL {
		out := append([]claudeSnapshot(nil), memoSnaps...)
		memoMu.Unlock()
		return out, nil
	}
	// ② single-flight:已有一轮在跑 → 等它、共享结果。
	if c := scanInflight; c != nil {
		memoMu.Unlock()
		<-c.done
		return append([]claudeSnapshot(nil), c.snaps...), c.err
	}
	// 成为 leader:登记在跑,锁外做重活(见 doScanClaudeSessions)。
	c := &scanCall{done: make(chan struct{})}
	scanInflight = c
	memoMu.Unlock()

	var completed bool
	defer func() {
		memoMu.Lock()
		if completed && c.err == nil { // 仅正常完成且无错时刷新 memo(panic/出错不缓存)
			memoSnaps, memoAt = append([]claudeSnapshot(nil), c.snaps...), time.Now()
		}
		scanInflight = nil
		memoMu.Unlock()
		close(c.done) // 唤醒所有等待者
	}()

	if scanBarrier != nil {
		scanBarrier() // 测试 seam(生产为 nil):在此卡住 leader 制造「扫描进行中」窗口以验证 single-flight
	}
	c.snaps, c.err = doScanClaudeSessions()
	completed = true
	return append([]claudeSnapshot(nil), c.snaps...), c.err
}

// scanBarrier 是仅供测试的 seam:非 nil 时由 leader 在真扫描前调用一次(生产恒为 nil,无开销)。
var scanBarrier func()

// doScanClaudeSessions 跑一轮真正的全量扫描(在锁外,由 scanClaudeSessions 的 leader 调用一次);
// 并发协调(memo / single-flight)全在 scanClaudeSessions,这里只管扫。逐文件 (mtime+size) 缓存复用
// idle 会话,只有变动/新增文件付全行扫描。
func doScanClaudeSessions() ([]claudeSnapshot, error) {
	root := claudeProjectsDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := []claudeSnapshot{}
	seen := make(map[string]bool, 128) // 本轮真实存在的文件 → 据此逐出已删条目
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pdir := filepath.Join(root, e.Name())
		files, _ := os.ReadDir(pdir)
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			path := filepath.Join(pdir, f.Name())
			fi, statErr := f.Info() // DirEntry.Info():复用 ReadDir 已得 stat,省一次 syscall
			if statErr != nil {
				continue // ReadDir 与 Info 之间文件消失等 → 跳过,且不入 seen → 自然逐出
			}
			seen[path] = true
			if snap, ok := cachedScanOne(path, fi.ModTime().UnixNano(), fi.Size()); ok {
				out = append(out, snap)
			}
		}
	}
	pruneScanCache(seen)
	return out, nil
}

// cachedScanOne:①精确命中复用;②文件增长(append-only)只读新尾增量累进;③否则全量。
// 重活(开文件 + JSON 解析)在锁外做,并发端点扫不同变动文件不互相阻塞。
func cachedScanOne(path string, mtimeNs, size int64) (claudeSnapshot, bool) {
	scanMu.Lock()
	e, hit := scanCache[path]
	scanMu.Unlock()

	// ① 精确命中:文件未变,复用解析。State/AgeSec 是时变量(classify 按「距今 <120s」判 working),
	//    会话停笔后文件冻结、不再 miss → 必须每次重算,否则停笔瞬间的 working 永久冻住不衰减成 idle
	//    (/sessions 与 syncer 报陈旧 working 的根因)。只用已缓存字段重算,不重读文件,廉价。
	if hit && e.mtimeNs == mtimeNs && e.size == size {
		snap, ok := e.snap, e.ok // claudeSnapshot 扁平值类型,拷贝出锁安全
		if ok {
			snap.AgeSec, snap.State = classify(snap.LastRole, snap.LastKind, snap.Preview, snap.LastTs)
		}
		return snap, ok
	}

	// ② 增量 vs ③ 全量。append-only 文件仅增长(size 变大且旧偏移仍落在文件内)→ 从上次快照续、只读
	//    [off, EOF) 的新尾;否则(新文件 / 被 /clear 截断重写 size 变小 / 无缓存)→ 全量从头。
	//    scanSession 已算好 classify;两条路径结果一致(计数累加、last_* 取新尾、首个 cwd 由 base 保留)。
	var snap claudeSnapshot
	var off int64
	var ok bool
	if hit && size > e.size && e.off >= 0 && e.off <= e.size {
		snap, off, ok = scanSession(path, e.off, e.snap) // 增量
	} else {
		snap, off, ok = scanSession(path, 0, claudeSnapshot{}) // 全量
	}
	snap.Bytes = size // 文件字节数(会话大小);size 已由 ReadDir 的 stat 得到,零额外 syscall
	scanMu.Lock()
	scanCache[path] = scanCacheEntry{mtimeNs: mtimeNs, size: size, off: off, snap: snap, ok: ok}
	scanMu.Unlock()
	return snap, ok
}

// pruneScanCache:逐出本轮 ReadDir 未见到的路径(会话 jsonl 被删/迁移/换 --claude-dir)。
func pruneScanCache(seen map[string]bool) {
	scanMu.Lock()
	for p := range scanCache {
		if !seen[p] {
			delete(scanCache, p)
		}
	}
	scanMu.Unlock()
}

type rawEvent struct {
	Type      string `json:"type"`
	UUID      string `json:"uuid"`
	SessionID string `json:"sessionId"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
	GitBranch string `json:"gitBranch"`
	AiTitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	// Edit/MultiEdit 的 tool_result 行在**顶层**带 toolUseResult(非 message 内),其中
	// structuredPatch 是含上下文行的标准 hunk 数组(与 TUI 同源)。transcript 透传给前端画 diff。
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	Message       *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Model   string          `json:"model"`
		Usage   *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// scanSession 从 fromOff 处扫描 path、以 base 为起点累进,返回快照 + 已消费到的行边界偏移 + ok(有消息)。
// fromOff==0 = 全量(base 传零值);fromOff>0 = 增量(base 传上次快照,只读新尾 [fromOff, EOF))。
// 因 jsonl append-only:计数类累加、last_* 由新尾覆盖、首个 cwd 由 base 保留 → 增量与全量结果一致。
func scanSession(path string, fromOff int64, base claudeSnapshot) (claudeSnapshot, int64, bool) {
	fh, err := os.Open(path)
	if err != nil {
		return base, fromOff, base.MsgCount > 0
	}
	defer fh.Close()

	snap := base
	if snap.SessionID == "" { // 全量起点:文件名兜底,遇到带 sessionId 的事件再覆盖
		snap.SessionID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	off := scanFrom(fh, fromOff, &snap)
	if snap.MsgCount == 0 {
		return snap, off, false
	}
	snap.AgeSec, snap.State = classify(snap.LastRole, snap.LastKind, snap.Preview, snap.LastTs)
	return snap, off, true
}

// scanFrom 从 fh 的 startOff 处逐行读,对每个**完整行**(以 \n 结尾)调用 applyEvent 累进 snap,返回读到
// 的最后一个完整行之后的字节偏移(下次增量从此续)。末尾无 \n 的半截行(正在追加写)不处理、不推进偏移
// → 下轮文件长大后从同一偏移把它当完整行读一次,不重不漏。
// scanFromHook 是仅供测试的 seam:非 nil 时 scanFrom 入口以 startOff 调用一次(生产恒 nil,一次 nil 检查)。
// 用于验证「增长文件走增量、从非零偏移续读」,不重读整文件。
var scanFromHook func(startOff int64)

func scanFrom(fh *os.File, startOff int64, snap *claudeSnapshot) int64 {
	if scanFromHook != nil {
		scanFromHook(startOff)
	}
	if _, err := fh.Seek(startOff, io.SeekStart); err != nil {
		return startOff // 常规文件 seek 到有效偏移不会失败;万一失败则不推进(下轮 mtime 变会重来)
	}
	br := bufio.NewReaderSize(fh, 64<<10)
	off := startOff
	for {
		line, consumed, complete := readLineAt(br, scanLineCap)
		if !complete {
			return off // EOF 或末尾半截行:停在最后一个完整行之后
		}
		applyEvent(snap, line)
		off += consumed
	}
}

// applyEvent 解析一行 jsonl 事件并累进 snap(全量/增量共用)。解析失败的行忽略(截断的超大行也落此)。
func applyEvent(snap *claudeSnapshot, line []byte) {
	var ev rawEvent
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	if ev.SessionID != "" {
		snap.SessionID = ev.SessionID
	}
	// resume 按「第一个(原始)cwd」作用域:jsonl 落在 encode(初始 cwd) 的 project 目录下,cwd 漂移过的
	// 会话若用「最后的 cwd」去 claude --resume 会报 "No conversation found"。故取第一个(增量下由 base 保留)。
	if ev.Cwd != "" && snap.Cwd == "" {
		snap.Cwd = ev.Cwd
	}
	if ev.GitBranch != "" {
		snap.GitBranch = ev.GitBranch
	}
	if ev.Type == "ai-title" && ev.AiTitle != "" {
		snap.Title = ev.AiTitle
	}
	// /rename 写入 custom-title 事件,优先级高于 ai-title
	if ev.Type == "custom-title" && ev.CustomTitle != "" {
		snap.Title = ev.CustomTitle
	}
	if ev.Message != nil && (ev.Type == "user" || ev.Type == "assistant") {
		snap.MsgCount++
		if ev.Timestamp != "" {
			snap.LastTs = ev.Timestamp
		}
		snap.LastRole = ev.Message.Role
		snap.LastKind, snap.Preview = contentKindPreview(ev.Message.Content)
		// 用户真·提问轮数:排除工具结果回传(role=user 但 content 是 tool_result)
		if ev.Type == "user" && snap.LastKind != "tool_result" {
			snap.Turns++
		}
		if ev.Type == "assistant" {
			if ev.Message.Model != "" {
				snap.Model = shortModel(ev.Message.Model)
			}
			if u := ev.Message.Usage; u != nil {
				// 当前上下文占用 ≈ 最后一条 assistant 的 input + 两类 cache(随会话推进取最新)
				snap.Tokens = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
			}
		}
	}
}

// contentKindPreview 解析 message.content(string 或 block 数组),返回 kind + 短预览。
func contentKindPreview(raw json.RawMessage) (kind, preview string) {
	if len(raw) == 0 {
		return "", ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return "text", trunc(s, 160)
	}
	var blocks []map[string]any
	if json.Unmarshal(raw, &blocks) == nil {
		kind = "text"
		var parts []string
		for _, b := range blocks {
			switch bt, _ := b["type"].(string); bt {
			case "tool_use":
				kind = "tool_use"
				if n, ok := b["name"].(string); ok {
					parts = append(parts, "tool:"+n)
				}
			case "tool_result":
				if kind != "tool_use" {
					kind = "tool_result"
				}
				parts = append(parts, "result")
			case "text":
				if t, ok := b["text"].(string); ok {
					parts = append(parts, t)
				}
			case "thinking":
				parts = append(parts, "(thinking)")
			}
		}
		return kind, trunc(strings.Join(parts, " | "), 160)
	}
	return "", ""
}

// classify 由"最后消息的角色/类型/内容 + 距今时长"推断会话状态。
// 状态:error / closed / awaiting_input / working / idle / unknown
func classify(role, kind, preview, lastTs string) (int64, string) {
	age := ageSeconds(lastTs)
	switch {
	case role == "assistant" && containsAny(preview, "API Error", "hit your limit", "Please run /login", "overloaded", "rate limit", "Request was aborted"):
		return age, "error"
	case role == "user" && containsAny(preview, "Bye!", "Goodbye!", "See ya!", "Catch you later!", "Until next time"):
		return age, "closed" // 用户 /bye 等主动退出(Claude Code 的告别语)
	case role == "assistant" && kind == "text":
		return age, "awaiting_input" // 助手给了文字回复 = 一轮结束,该你了
	case age >= 0 && age < 120:
		return age, "working"
	default:
		return age, "idle"
	}
}

// shortModel 把完整模型名简化成 opus/sonnet/haiku(展示用)。
func shortModel(m string) string {
	switch {
	case strings.Contains(m, "opus"):
		return "opus"
	case strings.Contains(m, "sonnet"):
		return "sonnet"
	case strings.Contains(m, "haiku"):
		return "haiku"
	}
	return m
}

func ageSeconds(lastTs string) int64 {
	t, err := time.Parse(time.RFC3339, lastTs)
	if err != nil {
		return -1
	}
	return int64(time.Since(t).Seconds())
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}
