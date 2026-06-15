package control

// transcript.go — 把 Claude 会话的 jsonl 渲染成「紧凑全文」供只读历史阅读器用。
// 关键设计:jsonl 是 append-only,所以用「字节偏移」做游标 —— 增量只读 since 之后的新行,
// 永不重传旧数据(SSE 跟随、断线重连、手动刷新共用此游标)。
// 体积主要来自 tool_result 大块,故工具块塌成一行摘要,user/assistant 文本全量。

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// reImageSource 匹配 harness 附图的「路径式」text block:整条形如
// [Image: source: /Users/…/.claude/image-cache/<sid>/1.png],捕获真实文件路径。
var reImageSource = regexp.MustCompile(`^\[Image: source: (.+)\]$`)

// tItem 是渲染后的单条消息(发给 UI 的最小单元)。
// Text 是「紧凑文本」(termkit 旧阅读器用);Blocks 是「结构化块」(新 React 前端自行渲染 diff/工具卡)。
type tItem struct {
	Role      string   `json:"role"` // user / assistant
	Kind      string   `json:"kind"` // text / tool_use / tool_result
	Text      string   `json:"text"`
	Ts        string   `json:"ts"`
	Uuid      string   `json:"uuid,omitempty"` // 该 jsonl 行的 uuid(图片块按 uuid+idx 回 /image 取字节)
	Model     string   `json:"model,omitempty"`
	OutTokens int      `json:"outTokens,omitempty"` // assistant 行的输出 token(message.usage.output_tokens),供「轮注脚」累加
	Blocks    []tBlock `json:"blocks,omitempty"`
}

// tBlock 是一条消息里的结构化内容块。前端按 type 渲染。
type tBlock struct {
	Type    string          `json:"type"`              // text | thinking | tool_use | tool_result | image
	Text    string          `json:"text,omitempty"`    // text / thinking 正文
	ID      string          `json:"id,omitempty"`      // tool_use 的 id
	Name    string          `json:"name,omitempty"`    // tool_use 工具名
	Input   json.RawMessage `json:"input,omitempty"`   // tool_use 原始入参(结构化,前端据此画 diff/卡片)
	ForID   string          `json:"forId,omitempty"`   // tool_result 对应的 tool_use_id
	Content string          `json:"content,omitempty"` // tool_result 文本
	IsError bool            `json:"isError,omitempty"` // tool_result 是否报错
	// Edit/MultiEdit 的 tool_result 行带顶层 toolUseResult.structuredPatch(标准 hunk 数组,
	// 含上下文行,与 TUI 同源)。原样透传给前端用于渲染带上下文的 diff。
	Patch json.RawMessage `json:"patch,omitempty"` // tool_result:structuredPatch hunk 数组
	// image 块(不内联 base64,避免撑爆 transcript;前端用 uuid+imgIdx 回 /image 取字节)。
	MediaType string `json:"mediaType,omitempty"` // image:媒体类型(image/png 等),base64 式才有
	Path      string `json:"path,omitempty"`      // image:路径式的真实文件路径
	ImgIdx    *int   `json:"imgIdx,omitempty"`    // image:该消息内第 N 个图片(两类合计,从 0 起)
}

// tStep = 一条消息 + 它所在行的字节偏移。
// Cursor = 该行**末尾**字节(= 消费到此处的游标,向后增量用 since=Cursor 续上);
// StartCursor = 该行**起始**字节(= 向上分页用 before=StartCursor 续上,保证不重不漏)。
type tStep struct {
	Item        tItem
	Cursor      int64
	StartCursor int64
}

// transcriptPath 在 ~/.claude/projects/**/<sid>.jsonl 里按文件名定位会话文件。
// 只为「读」,不需要 cwd —— 绕开 resume 的 cwd 作用域坑。
func transcriptPath(sid string) string {
	if sid == "" || strings.ContainsAny(sid, "/\\") {
		return ""
	}
	root := claudeProjectsDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name(), sid+".jsonl")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// imageCacheDir 返回 harness 附图缓存根目录 ~/.claude/image-cache(与 projects 同级;随 claudeProjectsDir 一起被 --claude-dir/CCFLY_CLAUDE_DIR 覆盖)。
func imageCacheDir() string {
	return filepath.Join(filepath.Dir(claudeProjectsDir()), "image-cache")
}

// imageInfo 描述一张可回取的图片:base64 式带 Data+MediaType,路径式带 Path。
type imageInfo struct {
	Data      []byte // base64 式:已解码字节
	MediaType string // base64 式:媒体类型
	Path      string // 路径式:真实文件路径(已校验在 image-cache 之下)
}

// findMessageImage 在主 jsonl 里按 uuid 找到该行,取 message.content 里第 idx 个图片(两类合计计数)。
// base64 式 → 解码字节 + media_type;路径式 → 校验路径在 ~/.claude/image-cache/<sid>/ 之下后返回路径。
// 防路径穿越:filepath.Clean + 前缀校验。找不到/越界返回 (imageInfo{}, false)。
func findMessageImage(sid, uuid string, idx int) (imageInfo, bool) {
	if uuid == "" || idx < 0 {
		return imageInfo{}, false
	}
	path := transcriptPath(sid)
	if path == "" {
		return imageInfo{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return imageInfo{}, false
	}
	defer f.Close()
	for line := range jsonlLines(f) { // 无上限逐行:大 base64 图片行不截断(见 jsonlLines)
		var ev struct {
			UUID    string `json:"uuid"`
			Message *struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.UUID != uuid || ev.Message == nil {
			continue
		}
		return imageFromContent(ev.Message.Content, sid, idx)
	}
	return imageInfo{}, false
}

// imageFromContent 遍历 message.content 的 block 数组,按「两类图片合计」第 idx 个返回。
func imageFromContent(raw json.RawMessage, sid string, idx int) (imageInfo, bool) {
	var arr []map[string]json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return imageInfo{}, false
	}
	cur := 0
	for _, b := range arr {
		var typ string
		_ = json.Unmarshal(b["type"], &typ)
		switch typ {
		case "image":
			if cur == idx {
				var src struct {
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				}
				_ = json.Unmarshal(b["source"], &src)
				data, err := base64.StdEncoding.DecodeString(src.Data)
				if err != nil {
					return imageInfo{}, false
				}
				mt := src.MediaType
				if mt == "" {
					mt = "image/png"
				}
				return imageInfo{Data: data, MediaType: mt}, true
			}
			cur++
		case "text":
			var t string
			_ = json.Unmarshal(b["text"], &t)
			m := reImageSource.FindStringSubmatch(strings.TrimSpace(t))
			if m == nil {
				continue
			}
			if cur == idx {
				p := safeImagePath(sid, m[1])
				if p == "" {
					return imageInfo{}, false
				}
				return imageInfo{Path: p}, true
			}
			cur++
		}
	}
	return imageInfo{}, false
}

// safeImagePath 校验路径式附图。路径来自用户自己会话 jsonl 的 [Image: source: …] 标记、经 uuid+idx 读出
// (URL 只传 uuid+idx,注入不了任意路径),故视为可信:放行「image-cache/<sid>/ 之下」或「真实存在的图片扩展名文件」
// (微信 InputTemp 等本地图同样需可读)。仍要求是已存在的普通文件并 filepath.Clean 防穿越;非图片扩展名一律拒。
func safeImagePath(sid, raw string) string {
	if sid == "" || strings.ContainsAny(sid, "/\\") {
		return ""
	}
	clean := filepath.Clean(raw)
	if !filepath.IsAbs(clean) {
		return ""
	}
	if st, err := os.Stat(clean); err != nil || st.IsDir() {
		return ""
	}
	// image-cache/<sid>/ 之下:最稳,直接放行。
	base := filepath.Clean(filepath.Join(imageCacheDir(), sid)) + string(os.PathSeparator)
	if strings.HasPrefix(clean, base) {
		return clean
	}
	// 其余:仅放行图片扩展名的真实文件(SVG 排除,避免脚本风险)。
	switch strings.ToLower(filepath.Ext(clean)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".heic", ".heif", ".avif":
		return clean
	}
	return ""
}

// subagentMeta 是子代理的元信息(给前端 AgentCard 用),源自 agent-<agentId>.meta.json + 文件名。
type subagentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	AgentID     string `json:"agentId"`
}

// subagentPathByToolUse 在 <projectDir>/<sid>/subagents/ 下按 toolUseId 定位子代理 transcript。
// 遍历 *.meta.json,解析其 toolUseId 字段匹配 → 从文件名 agent-<agentId>.meta.json 取 agentId
// → 返回同目录 agent-<agentId>.jsonl 路径 + meta。找不到返回 ("", meta{})。防路径穿越。
func subagentPathByToolUse(sid, toolUseId string) (string, subagentMeta) {
	if sid == "" || strings.ContainsAny(sid, "/\\") {
		return "", subagentMeta{}
	}
	if toolUseId == "" || strings.ContainsAny(toolUseId, "/\\") {
		return "", subagentMeta{}
	}
	main := transcriptPath(sid) // <projectDir>/<sid>.jsonl;其 dir 即 projectDir
	if main == "" {
		return "", subagentMeta{}
	}
	subDir := filepath.Join(filepath.Dir(main), sid, "subagents")
	entries, err := os.ReadDir(subDir)
	if err != nil {
		return "", subagentMeta{}
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".meta.json") || !strings.HasPrefix(name, "agent-") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(subDir, name))
		if err != nil {
			continue
		}
		var m struct {
			AgentType   string `json:"agentType"`
			Description string `json:"description"`
			ToolUseID   string `json:"toolUseId"`
		}
		if json.Unmarshal(raw, &m) != nil || m.ToolUseID != toolUseId {
			continue
		}
		agentID := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".meta.json")
		jsonlPath := filepath.Join(subDir, "agent-"+agentID+".jsonl")
		if st, err := os.Stat(jsonlPath); err != nil || st.IsDir() {
			return "", subagentMeta{}
		}
		return jsonlPath, subagentMeta{AgentType: m.AgentType, Description: m.Description, AgentID: agentID}
	}
	return "", subagentMeta{}
}

// readTranscriptSteps 从 since 字节读到当前 EOF,逐「完整行」解析。
// 返回每条消息及其行末字节游标,以及最终游标(= 已消费的完整行总字节)。
// 末尾若有半截行(正在追加写)则不消费、不推进游标,留待下次 since=cursor 再读。
func readTranscriptSteps(path string, since int64) ([]tStep, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, since, err
	}
	defer f.Close()
	if since > 0 {
		if _, err := f.Seek(since, 0); err != nil {
			return nil, since, err
		}
	}
	cursor := since
	r := bufio.NewReader(f)
	var steps []tStep
	for {
		lineStart := cursor // 本行起始字节(推进前的游标)
		// ReadBytes 不受 bufio.Scanner 的单 token 上限制约,可读超大行(工具结果)。
		chunk, err := r.ReadBytes('\n')
		if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
			cursor += int64(len(chunk))
			if it, ok := renderEvent(chunk); ok {
				steps = append(steps, tStep{Item: it, Cursor: cursor, StartCursor: lineStart})
			}
		}
		if err != nil { // io.EOF 或其它:半截行已被忽略(未推进游标)
			break
		}
	}
	return steps, cursor, nil
}

// readTranscriptStepsRange 读 [start, end) 区间内的完整行(其余同 readTranscriptSteps)。
// 用于向上分页:只解析 [start, before) 这一窗,避免把 before 之后的旧重复数据再算进来。
// 返回的 step 仍带行首/行末游标;end<=0 视作读到 EOF(等价 readTranscriptSteps)。
func readTranscriptStepsRange(path string, start, end int64) ([]tStep, error) {
	if start < 0 {
		start = 0
	}
	all, _, err := readTranscriptSteps(path, start)
	if err != nil {
		return nil, err
	}
	if end <= 0 {
		return all, nil
	}
	out := all[:0:0]
	for _, s := range all {
		if s.Cursor <= end { // 行完整落在 before 之前才纳入(Cursor=行末)
			out = append(out, s)
		}
	}
	return out, nil
}

// startByteForLastN 从 endByte 向前读块、按 '\n'(0x0A,UTF-8 安全字节)反向数物理行,
// 直到凑够 ~maxItems 行或累计字节超 maxBytes,返回对齐到行首的起始字节(总是 <= endByte)。
// 注意:数的是**物理行**(近似 item 数,可能略多——空行/非消息行也算),调用方解析后再裁到末尾 N 个 item。
// 巨大单行预算容得下:即便超 maxBytes,也保证回退到至少一个完整行的行首(不会切断行)。
// 空文件/endByte<=0 → 返回 0。
func startByteForLastN(path string, endByte int64, maxItems int, maxBytes int64) int64 {
	if endByte <= 0 || maxItems <= 0 {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	const blk = 64 * 1024
	pos := endByte           // 已扫描区间 [pos, endByte) 的左端,向 0 收缩
	newlines := 0            // 在 [pos, endByte) 区间内见到的 '\n' 个数
	lastLineStart := endByte // 最近一个「行首」(某个 '\n' 之后那个字节);初值=endByte(末行无尾换行时的行首兜底)
	buf := make([]byte, blk)
	for pos > 0 {
		// 这一块的读取范围 [readAt, pos)
		readAt := pos - blk
		if readAt < 0 {
			readAt = 0
		}
		n := int(pos - readAt)
		if _, err := f.Seek(readAt, 0); err != nil {
			return 0
		}
		if _, err := readFull(f, buf[:n]); err != nil {
			return 0
		}
		// 在本块内从后往前找 '\n':每个 '\n' 标记「下一行的行首」= 该 '\n' 的下一个字节。
		for i := n - 1; i >= 0; i-- {
			if buf[i] != '\n' {
				continue
			}
			lineStart := readAt + int64(i) + 1
			// 跳过「就在 endByte 末尾的那个 '\n'」(它不开启新行,只是末行的结尾)。
			if lineStart >= endByte {
				continue
			}
			newlines++
			lastLineStart = lineStart
			// 凑够 maxItems 物理行:lastLineStart 之后正好有 maxItems 个完整行落在 [.,endByte)。
			if newlines >= maxItems {
				return lastLineStart
			}
			// 字节预算:从此行首到 endByte 已超预算 → 收一行(保证至少一个完整行,不切断)。
			if endByte-lineStart >= maxBytes {
				return lineStart
			}
		}
		pos = readAt
	}
	// 扫到文件头都没凑够 → 从 0 开始(含首行)。
	return 0
}

// readFull 把 b 读满(os.File.Read 可能短读);EOF 也算错(调用方读的是已知存在的区间)。
func readFull(f *os.File, b []byte) (int, error) {
	got := 0
	for got < len(b) {
		n, err := f.Read(b[got:])
		got += n
		if err != nil {
			return got, err
		}
	}
	return got, nil
}

// 尾窗/向上分页的默认预算:每窗最多 150 条可渲染 item,且单窗 ~4MB 字节上限(二者先到为准)。
const (
	transcriptWindowItems = 150
	transcriptWindowBytes = 4 * 1024 * 1024
)

// transcriptWindow 取一个「字节窗」内的末尾 N 条 item,供首拉(尾窗)与向上分页复用。
//   - endByte:窗口右端(首拉=EOF;before 分页=before 字节)。
//   - 反向定位出起始字节 start,解析 [start, endByte) 的 steps,若仍 > N 则只留末尾 N 个。
//
// 返回:
//   - items:本窗末尾最多 N 条;
//   - firstCursor:本批**最旧**保留 item 所在行的**起始**字节(前端下次 before=firstCursor 无缝接续);
//     无 item 时为 0;
//   - hasMore:firstCursor>0 即还有更老(前端可继续向上翻)。
//
// endByte<=0 / 空窗 → 返回 (nil, 0, false),不 panic。
func transcriptWindow(path string, endByte int64) (items []tItem, firstCursor int64, hasMore bool, err error) {
	if endByte <= 0 {
		return nil, 0, false, nil
	}
	start := startByteForLastN(path, endByte, transcriptWindowItems, transcriptWindowBytes)
	steps, err := readTranscriptStepsRange(path, start, endByte)
	if err != nil {
		return nil, 0, false, err
	}
	if len(steps) == 0 {
		return nil, 0, false, nil
	}
	if len(steps) > transcriptWindowItems {
		steps = steps[len(steps)-transcriptWindowItems:] // 只留末尾 N 个 item
	}
	items = make([]tItem, 0, len(steps))
	for _, s := range steps {
		items = append(items, s.Item)
	}
	firstCursor = steps[0].StartCursor // 最旧保留 item 的行首字节
	hasMore = firstCursor > 0          // 行首>0 → 它前面还有更老的行
	return items, firstCursor, hasMore, nil
}

// renderEvent 解析一行 jsonl;只有 user/assistant 消息产出条目。
func renderEvent(line []byte) (tItem, bool) {
	var ev rawEvent
	if json.Unmarshal(line, &ev) != nil {
		return tItem{}, false
	}
	if ev.Message == nil || (ev.Type != "user" && ev.Type != "assistant") {
		return tItem{}, false
	}
	kind, text := fullContent(ev.Message.Content)
	blocks := contentBlocks(ev.Message.Content)
	// Edit/MultiEdit 的 tool_result 行:顶层 toolUseResult.structuredPatch 透传到对应 tool_result 块。
	if len(ev.ToolUseResult) > 0 {
		if patch := extractStructuredPatch(ev.ToolUseResult); len(patch) > 0 {
			for i := range blocks {
				if blocks[i].Type == "tool_result" {
					blocks[i].Patch = patch
					break
				}
			}
		}
	}
	if text == "" && len(blocks) == 0 {
		return tItem{}, false
	}
	// 输出 token:仅 assistant 行的 message.usage.output_tokens(供前端「轮注脚」累加;user 行无)。
	outTok := 0
	if ev.Message.Role == "assistant" && ev.Message.Usage != nil {
		outTok = ev.Message.Usage.OutputTokens
	}
	return tItem{Role: ev.Message.Role, Kind: kind, Text: text, Ts: ev.Timestamp, Uuid: ev.UUID, Model: shortModel(ev.Message.Model), OutTokens: outTok, Blocks: blocks}, true
}

// contentBlocks 把 message.content 解析成结构化块(text/thinking/tool_use/tool_result),保留原始入参。
func contentBlocks(raw json.RawMessage) []tBlock {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return []tBlock{{Type: "text", Text: s}}
	}
	var arr []map[string]json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return nil
	}
	var out []tBlock
	imgIdx := 0 // 同一消息内对「所有图片(路径式 + base64 式合计)」连续编号,供 /image 端点定位
	for _, b := range arr {
		var typ string
		_ = json.Unmarshal(b["type"], &typ)
		switch typ {
		case "text":
			var t string
			_ = json.Unmarshal(b["text"], &t)
			// 路径式附图:整条即 [Image: source: <path>] → 结构化 image 块(不再留成 [Image: source:…] 文本)。
			if m := reImageSource.FindStringSubmatch(strings.TrimSpace(t)); m != nil {
				idx := imgIdx
				out = append(out, tBlock{Type: "image", Path: m[1], ImgIdx: &idx})
				imgIdx++
				break
			}
			if strings.TrimSpace(t) != "" {
				out = append(out, tBlock{Type: "text", Text: t})
			}
		case "image":
			// base64 式(TUI 粘贴):取 media_type,但不内联 base64(前端用 uuid+imgIdx 回 /image 取字节)。
			var src struct {
				MediaType string `json:"media_type"`
			}
			_ = json.Unmarshal(b["source"], &src)
			mt := src.MediaType
			if mt == "" {
				mt = "image/png"
			}
			idx := imgIdx
			out = append(out, tBlock{Type: "image", MediaType: mt, ImgIdx: &idx})
			imgIdx++
		case "thinking":
			var t string
			_ = json.Unmarshal(b["thinking"], &t)
			out = append(out, tBlock{Type: "thinking", Text: t}) // 多数为空(不持久化)
		case "tool_use":
			var id, name string
			_ = json.Unmarshal(b["id"], &id)
			_ = json.Unmarshal(b["name"], &name)
			out = append(out, tBlock{Type: "tool_use", ID: id, Name: name, Input: b["input"]})
		case "tool_result":
			var tid string
			_ = json.Unmarshal(b["tool_use_id"], &tid)
			var isErr bool
			_ = json.Unmarshal(b["is_error"], &isErr)
			out = append(out, tBlock{Type: "tool_result", ForID: tid, Content: resultTextRaw(b["content"]), IsError: isErr})
		}
	}
	return out
}

// extractStructuredPatch 从顶层 toolUseResult 里取 structuredPatch(hunk 数组),原样返回其 JSON。
// 仅当它是非空数组时返回(空数组/缺失 → nil,前端据此回退到 old/new diff)。
func extractStructuredPatch(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var tr struct {
		StructuredPatch json.RawMessage `json:"structuredPatch"`
	}
	if json.Unmarshal(raw, &tr) != nil || len(tr.StructuredPatch) == 0 {
		return nil
	}
	// 判定非空数组:解析成 []json.RawMessage,len>0 才透传。
	var arr []json.RawMessage
	if json.Unmarshal(tr.StructuredPatch, &arr) != nil || len(arr) == 0 {
		return nil
	}
	return tr.StructuredPatch
}

// resultTextRaw 取 tool_result content 的纯文本(string 或 [{type:text,text}])。
func resultTextRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var arr []map[string]any
	if json.Unmarshal(raw, &arr) == nil {
		var sb strings.Builder
		for _, m := range arr {
			if t, ok := m["text"].(string); ok {
				sb.WriteString(t)
			}
		}
		return sb.String()
	}
	return ""
}

// fullContent 解析 message.content(string 或 block 数组),返回 kind + 全文。
// 与 claudescan 的 contentKindPreview 同结构,但不截断正文;工具块塌成一行摘要。
func fullContent(raw json.RawMessage) (kind, text string) {
	if len(raw) == 0 {
		return "", ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return "text", strings.TrimSpace(s)
	}
	var blocks []map[string]any
	if json.Unmarshal(raw, &blocks) == nil {
		kind = "text"
		var parts []string
		for _, b := range blocks {
			switch bt, _ := b["type"].(string); bt {
			case "text":
				if t, ok := b["text"].(string); ok && strings.TrimSpace(t) != "" {
					parts = append(parts, t)
				}
			case "thinking":
				// Claude Code 不持久化思考正文(只剩 signature),通常为空 → 跳过。
				if t, ok := b["thinking"].(string); ok && strings.TrimSpace(t) != "" {
					parts = append(parts, "💭 "+t)
				}
			case "tool_use":
				kind = "tool_use"
				name, _ := b["name"].(string)
				parts = append(parts, strings.TrimSpace("▸ "+name+" "+toolBrief(b["input"])))
			case "tool_result":
				if kind != "tool_use" {
					kind = "tool_result"
				}
				parts = append(parts, resultBrief(b["content"]))
			}
		}
		return kind, strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return "", ""
}

// toolBrief 从工具入参里挑一个关键字段做一行摘要。
func toolBrief(input any) string {
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "description"} {
		if v, ok := m[k].(string); ok && v != "" {
			return trunc(v, 100)
		}
	}
	b, _ := json.Marshal(m)
	return trunc(string(b), 100)
}

// resultBrief 把 tool_result 内容塌成「首几行 + 总字符数」,避免几十上百 KB 的大块。
func resultBrief(content any) string {
	txt := strings.TrimSpace(resultText(content))
	if txt == "" {
		return "(result)"
	}
	n := len([]rune(txt))
	lines := strings.Split(txt, "\n")
	head := lines
	if len(head) > 6 {
		head = head[:6]
	}
	s := "(result)\n" + strings.Join(head, "\n")
	if len(lines) > 6 || n > 600 {
		s += fmt.Sprintf("\n… (%d 字符)", n)
	}
	return s
}

// resultText 取 tool_result 的纯文本(content 可能是 string 或 [{type:text,text}])。
func resultText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, it := range c {
			if m, ok := it.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	}
	return ""
}

// ── 会话信息(/info):从 jsonl 派生「上下文用量 + 累计 token 花费 + 元信息」,统一展示。
// 非侵入(不注入 /context、/cost、/status 命令);$ 金额需定价表、账户/版本只有 /status 有,暂不含。
type infoResp struct {
	Model       string `json:"model"`
	CtxTokens   int    `json:"ctxTokens"`   // 当前上下文占用(最后一条 assistant 的 input+cache)
	CtxLimit    int    `json:"ctxLimit"`    // 推断(>200k 视作 1M context)
	InTokens    int    `json:"inTokens"`    // 累计输入
	OutTokens   int    `json:"outTokens"`   // 累计输出
	CacheTokens int    `json:"cacheTokens"` // 累计缓存读+写
	Turns       int    `json:"turns"`
	MsgCount    int    `json:"msgCount"`
	Cwd         string `json:"cwd"`
	GitBranch   string `json:"gitBranch,omitempty"`
	LastTs      string `json:"lastTs"`
}

func readSessionInfo(path string) (infoResp, bool) {
	f, err := os.Open(path)
	if err != nil {
		return infoResp{}, false
	}
	defer f.Close()
	var info infoResp
	maxCtx := 0
	for line := range jsonlLines(f) { // 无上限逐行(超大行不截断;见 jsonlLines)
		var ev rawEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Cwd != "" && info.Cwd == "" {
			info.Cwd = ev.Cwd // 取第一个(原始)cwd
		}
		if ev.GitBranch != "" {
			info.GitBranch = ev.GitBranch
		}
		if ev.Message == nil || (ev.Type != "user" && ev.Type != "assistant") {
			continue
		}
		info.MsgCount++
		if ev.Timestamp != "" {
			info.LastTs = ev.Timestamp
		}
		kind, _ := contentKindPreview(ev.Message.Content)
		if ev.Type == "user" && kind != "tool_result" {
			info.Turns++
		}
		if ev.Type == "assistant" {
			if ev.Message.Model != "" {
				info.Model = shortModel(ev.Message.Model)
			}
			if u := ev.Message.Usage; u != nil {
				info.InTokens += u.InputTokens
				info.OutTokens += u.OutputTokens
				info.CacheTokens += u.CacheReadInputTokens + u.CacheCreationInputTokens
				ctx := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
				info.CtxTokens = ctx // 当前 = 最后一条 assistant 的占用
				if ctx > maxCtx {
					maxCtx = ctx
				}
			}
		}
	}
	if info.MsgCount == 0 {
		return infoResp{}, false
	}
	info.CtxLimit = 200000
	if maxCtx > 200000 {
		info.CtxLimit = 1000000
	}
	return info, true
}

// ── 信息类斜杠命令结果(/cmdresult):从 jsonl 读「干净 Markdown」,摆脱抓屏。
// 机制:跑一个会产出结构化输出的斜杠命令(实测仅 /context),Claude Code 会往主 jsonl
// 追加一条 type:"user" 且顶层 isMeta:true 的消息,message.content 是完整 Markdown 字符串
// (真用户消息无 isMeta)。这里从 since 字节起扫,返回「第一条」这样的消息及行末游标。
//
// 用法:前端「发命令前」先拿当前 EOF 作 since,sendkeys 提交命令,再轮询本接口直到 found。

// readCmdResult 从 since 字节起逐完整行扫描,返回首条 isMeta(string content)消息的 markdown
// 及其行末字节游标;未找到返回 ("", 当前EOF, false)。半截行不消费、不推进游标。
func readCmdResult(path string, since int64) (markdown string, cursor int64, found bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", since, false
	}
	defer f.Close()
	if since < 0 {
		since = 0
	}
	if since > 0 {
		if _, err := f.Seek(since, 0); err != nil {
			return "", since, false
		}
	}
	cursor = since
	r := bufio.NewReader(f)
	for {
		chunk, err := r.ReadBytes('\n')
		if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
			cursor += int64(len(chunk))
			if md, ok := isMetaMarkdown(chunk); ok {
				return md, cursor, true
			}
		}
		if err != nil { // io.EOF/其它:半截行已忽略(未推进游标)
			break
		}
	}
	return "", cursor, false
}

// isMetaMarkdown 判定一行 jsonl 是否为「type:user + isMeta:true + content 为非空字符串」,
// 是则返回那段 markdown。坏行/数组型 content 一律跳过(只认字符串型 isMeta 输出)。
func isMetaMarkdown(line []byte) (string, bool) {
	var ev struct {
		Type    string `json:"type"`
		IsMeta  bool   `json:"isMeta"`
		Message *struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return "", false
	}
	if ev.Type != "user" || !ev.IsMeta || ev.Message == nil {
		return "", false
	}
	var s string
	if json.Unmarshal(ev.Message.Content, &s) != nil { // 仅认字符串型 content
		return "", false
	}
	if strings.TrimSpace(s) == "" {
		return "", false
	}
	return s, true
}
