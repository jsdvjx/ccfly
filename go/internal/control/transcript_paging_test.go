package control

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realTranscript 返回测试用的本地大会话 jsonl 路径(发现本地任意 claude 会话;若无则空串)。
// 不写死任何机器路径 / session id(见 realfixture_test.go 的发现助手)。
func realTranscript() string {
	projectDir, sid := discoverRealSession()
	if projectDir == "" {
		return ""
	}
	p := filepath.Join(projectDir, sid+".jsonl")
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

// itemKey 给 item 取一个稳定指纹(ts + 角色 + kind + 文本头),用于校验「衔接处连续、不重不漏」。
func itemKey(it tItem) string {
	t := it.Text
	if len(t) > 64 {
		t = t[:64]
	}
	return it.Ts + "|" + it.Role + "|" + it.Kind + "|" + t
}

// TestTranscriptWindowTailReal 首拉(尾窗):真实大会话应返回 ≤150 条、hasMore=true、firstCursor>0。
func TestTranscriptWindowTailReal(t *testing.T) {
	path := realTranscript()
	if path == "" {
		t.Skip("无真实大会话 jsonl 夹具,跳过")
	}
	st, _ := os.Stat(path)
	eof := st.Size()

	items, firstCursor, hasMore, err := transcriptWindow(path, eof)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 || len(items) > transcriptWindowItems {
		t.Fatalf("首拉应返回 (0,%d] 条,得 %d", transcriptWindowItems, len(items))
	}
	if !hasMore {
		t.Fatalf("大会话首拉 hasMore 应为 true(还有更老)")
	}
	if firstCursor <= 0 {
		t.Fatalf("hasMore=true 时 firstCursor 应 >0,得 %d", firstCursor)
	}
	t.Logf("尾窗: items=%d firstCursor=%d eof=%d", len(items), firstCursor, eof)
}

// TestTranscriptPagingNoOverlapReal 一路 before 向上翻到顶:
//   - 每次 before=上一窗 firstCursor,衔接处连续(无重叠、无遗漏);
//   - 翻到顶后 hasMore=false;
//   - 累计去重后的条数 == 全量 readTranscriptSteps(path,0) 的条数(不重不漏铁证)。
func TestTranscriptPagingNoOverlapReal(t *testing.T) {
	path := realTranscript()
	if path == "" {
		t.Skip("无真实大会话 jsonl 夹具,跳过")
	}

	// 全量基准:顺序的 item key 序列。
	fullSteps, _, err := readTranscriptSteps(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fullKeys []string
	for _, s := range fullSteps {
		fullKeys = append(fullKeys, itemKey(s.Item))
	}
	t.Logf("全量 items=%d", len(fullKeys))

	st, _ := os.Stat(path)
	eof := st.Size()

	// 一路向上翻,把每窗 items 倒序前插,拼出「从最旧到最新」的完整序列。
	var assembled []string // 最旧→最新
	end := eof
	windows := 0
	prevFirstCursor := int64(-1)
	for {
		items, firstCursor, hasMore, err := transcriptWindow(path, end)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 0 {
			break
		}
		windows++
		// 把本窗(本身是「旧→新」)整体插到 assembled 前面。
		keys := make([]string, 0, len(items))
		for _, it := range items {
			keys = append(keys, itemKey(it))
		}
		assembled = append(keys, assembled...)

		if !hasMore {
			break
		}
		// before=firstCursor 续上;firstCursor 必须严格递减,否则会死循环/重叠。
		if prevFirstCursor >= 0 && firstCursor >= prevFirstCursor {
			t.Fatalf("firstCursor 未严格递减(%d >= %d),向上分页会重叠/死循环", firstCursor, prevFirstCursor)
		}
		prevFirstCursor = firstCursor
		end = firstCursor
		if windows > 100000 {
			t.Fatalf("窗口数异常过多,疑似未推进")
		}
	}
	t.Logf("翻了 %d 窗,拼出 %d 条", windows, len(assembled))

	// 铁证 1:拼出的序列长度 == 全量长度。
	if len(assembled) != len(fullKeys) {
		t.Fatalf("不重不漏失败:分页拼出 %d 条 ≠ 全量 %d 条", len(assembled), len(fullKeys))
	}
	// 铁证 2:逐条 key 完全一致(顺序、内容都对得上 → 衔接处连续)。
	for i := range fullKeys {
		if assembled[i] != fullKeys[i] {
			t.Fatalf("第 %d 条不一致:\n 分页=%q\n 全量=%q", i, assembled[i], fullKeys[i])
		}
	}
}

// TestTranscriptWindowSynthetic 用内联夹具精确验证尾窗/before/裁剪/边界(不依赖真实文件)。
func TestTranscriptWindowSynthetic(t *testing.T) {
	// 造 400 条可渲染消息,每条 text 唯一,便于核对衔接。
	const total = 400
	var sb strings.Builder
	for i := 0; i < total; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		sb.WriteString(fmt.Sprintf(
			`{"type":"%s","timestamp":"2026-06-05T00:00:%02d.000Z","message":{"role":"%s","content":"msg-%04d"}}`+"\n",
			role, i%60, role, i))
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	eof := st.Size()

	// 首拉:应得末尾 150 条(msg-0250 … msg-0399),hasMore=true。
	items, firstCursor, hasMore, err := transcriptWindow(path, eof)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != transcriptWindowItems {
		t.Fatalf("首拉应得 %d 条,得 %d", transcriptWindowItems, len(items))
	}
	if items[len(items)-1].Text != "msg-0399" {
		t.Fatalf("尾窗最后一条应为 msg-0399,得 %q", items[len(items)-1].Text)
	}
	if items[0].Text != fmt.Sprintf("msg-%04d", total-transcriptWindowItems) {
		t.Fatalf("尾窗第一条应为 msg-%04d,得 %q", total-transcriptWindowItems, items[0].Text)
	}
	if !hasMore || firstCursor <= 0 {
		t.Fatalf("首拉 hasMore=true & firstCursor>0,得 hasMore=%v firstCursor=%d", hasMore, firstCursor)
	}

	// before=firstCursor:应紧邻地接上一窗(末尾 msg-0249),无重叠无遗漏。
	items2, firstCursor2, hasMore2, err := transcriptWindow(path, firstCursor)
	if err != nil {
		t.Fatal(err)
	}
	if items2[len(items2)-1].Text != "msg-0249" {
		t.Fatalf("第二窗最后一条应为 msg-0249(紧接首窗 msg-0250),得 %q", items2[len(items2)-1].Text)
	}
	if len(items2) != transcriptWindowItems {
		t.Fatalf("第二窗应得 %d 条,得 %d", transcriptWindowItems, len(items2))
	}
	if !hasMore2 {
		t.Fatalf("第二窗 hasMore 应为 true(顶上还有 100 条)")
	}

	// 第三窗:剩 100 条(msg-0000 … msg-0099),hasMore=false,firstCursor=0。
	items3, firstCursor3, hasMore3, err := transcriptWindow(path, firstCursor2)
	if err != nil {
		t.Fatal(err)
	}
	if len(items3) != total-2*transcriptWindowItems {
		t.Fatalf("第三窗应得 %d 条,得 %d", total-2*transcriptWindowItems, len(items3))
	}
	if items3[0].Text != "msg-0000" {
		t.Fatalf("第三窗第一条应为 msg-0000,得 %q", items3[0].Text)
	}
	if hasMore3 || firstCursor3 != 0 {
		t.Fatalf("到顶应 hasMore=false & firstCursor=0,得 hasMore=%v firstCursor=%d", hasMore3, firstCursor3)
	}

	// 拼起来核对不重不漏:三窗 = 全量 400 条。
	got := append(append(append([]tItem{}, items3...), items2...), items...)
	if len(got) != total {
		t.Fatalf("三窗合计应 %d 条,得 %d", total, len(got))
	}
	for i := 0; i < total; i++ {
		want := fmt.Sprintf("msg-%04d", i)
		if got[i].Text != want {
			t.Fatalf("拼接第 %d 条应为 %q,得 %q", i, want, got[i].Text)
		}
	}
}

// TestTranscriptWindowEdge 边界:空文件 / endByte<=0 / before<=0 不 panic,返回空。
func TestTranscriptWindowEdge(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if items, fc, hm, err := transcriptWindow(empty, 0); err != nil || items != nil || fc != 0 || hm {
		t.Fatalf("空文件应返回 (nil,0,false,nil),得 (%v,%d,%v,%v)", items, fc, hm, err)
	}
	// endByte<=0
	if items, fc, hm, err := transcriptWindow(empty, -1); err != nil || items != nil || fc != 0 || hm {
		t.Fatalf("endByte<0 应返回空,得 (%v,%d,%v,%v)", items, fc, hm, err)
	}
	// startByteForLastN 边界
	if s := startByteForLastN(empty, 0, 10, 1<<20); s != 0 {
		t.Fatalf("startByteForLastN endByte=0 应为 0,得 %d", s)
	}
}

// TestStartByteForLastNHugeLine 巨大单行:预算要容得下至少 1 行(不切断、不漏)。
func TestStartByteForLastNHugeLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.jsonl")
	big := strings.Repeat("x", 700*1024) // ~700KB 单行工具结果
	lines := []string{
		`{"type":"user","timestamp":"2026-06-05T00:00:00.000Z","message":{"role":"user","content":"a"}}`,
		fmt.Sprintf(`{"type":"assistant","timestamp":"2026-06-05T00:00:01.000Z","message":{"role":"assistant","content":[{"type":"tool_result","tool_use_id":"t1","content":"%s"}]}}`, big),
		`{"type":"user","timestamp":"2026-06-05T00:00:02.000Z","message":{"role":"user","content":"b"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	eof := st.Size()
	// 用一个小预算(256KB)逼它在巨大行处触发预算上限,仍须返回完整行行首(start 对齐行首)。
	start := startByteForLastN(path, eof, transcriptWindowItems, 256*1024)
	if start < 0 || start >= eof {
		t.Fatalf("start 应在 [0,eof),得 %d (eof=%d)", start, eof)
	}
	// 从 start 解析应得到完整、可解析的 step(行未被切断)。
	steps, err := readTranscriptStepsRange(path, start, eof)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) == 0 {
		t.Fatalf("巨大行窗口应至少含 1 条完整 item")
	}
	// 最后一条应是 msg "b"(末行),证明行首对齐、没切断。
	if steps[len(steps)-1].Item.Text != "b" {
		t.Fatalf("末条应为 b,得 %q", steps[len(steps)-1].Item.Text)
	}
}
