package control

// sse_paging_test.go — /sse/jsonl?tail 首拉 与 /jsonl/before 向上翻页 的原始行级正确性。
// 用本地真实大会话夹具(无则跳过),不写死任何机器路径 / sid(见 realfixture_test.go)。

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSidForTmuxNameHonorsSpecificSid — cc-<sid8> 必须解析到**前缀匹配的那个 sid**,而非同 cwd 最近活动的。
// 这是 resolveSessionJsonl 的修复核心:同目录多会话时,不能一刀切成「cwd 最新」(否则终端与对话渲染对不上)。
func TestSidForTmuxNameHonorsSpecificSid(t *testing.T) {
	snaps := []claudeSnapshot{
		{SessionID: "aec19d8f-0000-0000-0000-000000000001", Cwd: "/Users/jinxing", LastTs: "2026-06-09T08:00:00.000Z"},
		{SessionID: "85f6359f-0000-0000-0000-000000000002", Cwd: "/Users/jinxing", LastTs: "2026-06-09T09:00:00.000Z"}, // 更新
		{SessionID: "9a8e82e0-0000-0000-0000-000000000003", Cwd: "/Users/jinxing", LastTs: "2026-06-09T07:00:00.000Z"},
	}
	// cc-aec19d8f 应解析到 aec19d8f,而不是 cwd 最新的 85f6359f。
	if got := sidForTmuxName("cc-aec19d8f", snaps); got != "aec19d8f-0000-0000-0000-000000000001" {
		t.Fatalf("cc-aec19d8f 应得 aec19d8f…,实得 %q", got)
	}
	if got := sidForTmuxName("cc-9a8e82e0", snaps); got != "9a8e82e0-0000-0000-0000-000000000003" {
		t.Fatalf("cc-9a8e82e0 应得 9a8e82e0…,实得 %q", got)
	}
	// 对照:newestSidForCwd 才是「cwd 最新」(旧实现误用它做最终解析)。
	if got := newestSidForCwd("/Users/jinxing", snaps); got != "85f6359f-0000-0000-0000-000000000002" {
		t.Fatalf("newestSidForCwd 应得 85f6359f…,实得 %q", got)
	}
}

type beforeResp struct {
	Path       string   `json:"path"`
	Lines      []string `json:"lines"`
	FirstStart int64    `json:"firstStart"`
	HasMore    bool     `json:"hasMore"`
}

func callBefore(t *testing.T, path string, before int64) (*httptest.ResponseRecorder, beforeResp) {
	t.Helper()
	req := httptest.NewRequest("GET", "/jsonl/before?path="+url.QueryEscape(path)+"&before="+strconv.FormatInt(before, 10), nil)
	rec := httptest.NewRecorder()
	handleJsonlBefore(rec, req)
	var r beforeResp
	if rec.Code == 200 {
		_ = json.Unmarshal(rec.Body.Bytes(), &r)
	}
	return rec, r
}

// TestHandleJsonlBefore — /jsonl/before 全 HTTP 路径:confine 安全闸、JSON 形状、限窗 + hasMore/firstStart、
// before=firstStart 续翻无重叠、before=0 空、越界 path 403。
func TestHandleJsonlBefore(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	var all []string
	for i := 0; i < 400; i++ {
		all = append(all, fmt.Sprintf(`{"type":"user","uuid":"u%d"}`, i))
	}
	content := strings.Join(all, "\n") + "\n"
	path := filepath.Join(proj, "sess.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	SetClaudeDir(dir)
	defer SetClaudeDir("")
	eof := int64(len(content))

	// 1) before=EOF:返回有界尾窗(<400),hasMore=true,firstStart>0,且是 all 的末尾段。
	rec, r := callBefore(t, path, eof)
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type=%q", ct)
	}
	if len(r.Lines) == 0 || len(r.Lines) >= 400 {
		t.Fatalf("尾窗应有界 (0,400),得 %d", len(r.Lines))
	}
	if !r.HasMore || r.FirstStart <= 0 {
		t.Fatalf("大文件首窗应 hasMore=true & firstStart>0,得 hasMore=%v firstStart=%d", r.HasMore, r.FirstStart)
	}
	tail := all[len(all)-len(r.Lines):]
	for i := range r.Lines {
		if r.Lines[i] != tail[i] {
			t.Fatalf("尾窗第 %d 行与末尾段不符", i)
		}
	}

	// 2) 一路 before=firstStart 续翻到顶:拼回 == all,无重叠/遗漏。
	got := append([]string{}, r.Lines...)
	before := r.FirstStart
	guard := 0
	for before > 0 {
		guard++
		if guard > 50 {
			t.Fatal("翻页未收敛")
		}
		_, rr := callBefore(t, path, before)
		if len(rr.Lines) == 0 {
			break
		}
		got = append(append([]string{}, rr.Lines...), got...)
		if rr.FirstStart >= before {
			t.Fatalf("firstStart 未前移 → 死循环")
		}
		before = rr.FirstStart
		if !rr.HasMore {
			break
		}
	}
	if len(got) != 400 {
		t.Fatalf("续翻拼回 %d 行 != 400", len(got))
	}
	for i := range all {
		if got[i] != all[i] {
			t.Fatalf("拼回第 %d 行不一致", i)
		}
	}

	// 3) before=0:空 + hasMore=false。
	if _, r0 := callBefore(t, path, 0); len(r0.Lines) != 0 || r0.HasMore {
		t.Fatalf("before=0 应空且 hasMore=false,得 lines=%d hasMore=%v", len(r0.Lines), r0.HasMore)
	}

	// 4) 越界 path → 403。
	outside := filepath.Join(t.TempDir(), "evil.jsonl")
	_ = os.WriteFile(outside, []byte("{}\n"), 0o644)
	req := httptest.NewRequest("GET", "/jsonl/before?path="+url.QueryEscape(outside)+"&before=10", nil)
	rec2 := httptest.NewRecorder()
	handleJsonlBefore(rec2, req)
	if rec2.Code != 403 {
		t.Fatalf("越界 path 应 403,得 %d", rec2.Code)
	}
}

// allRawLines — 正向读全文件的完整非空行(基准;与 rawJsonlSince/rawJsonlRange 同口径:只取 '\n' 结尾、跳空行)。
func allRawLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	r := bufio.NewReader(f)
	for {
		b, e := r.ReadBytes('\n')
		if len(b) > 0 && b[len(b)-1] == '\n' {
			line := strings.TrimRight(string(b), "\r\n")
			if strings.TrimSpace(line) != "" {
				out = append(out, line)
			}
		}
		if e != nil {
			break
		}
	}
	return out
}

// TestRawJsonlReversePagingReal — 从 EOF 反向一窗窗(startByteForLastN→rawJsonlRange,before=上一窗 firstStart)
// 翻到顶,prepend 拼回的行应与正向全量逐行相等:无重叠、无遗漏、不乱序、不死循环。
func TestRawJsonlReversePagingReal(t *testing.T) {
	path := realTranscript()
	if path == "" {
		t.Skip("无真实大会话 jsonl 夹具,跳过")
	}
	want := allRawLines(path)
	if len(want) == 0 {
		t.Skip("空会话")
	}
	st, _ := os.Stat(path)

	var got []string
	before := st.Size()
	windows := 0
	for before > 0 {
		start := startByteForLastN(path, before, transcriptWindowItems, transcriptWindowBytes)
		lines, firstStart := rawJsonlRange(path, start, before)
		if len(lines) == 0 {
			break
		}
		got = append(append([]string{}, lines...), got...) // prepend 本窗
		windows++
		if firstStart <= 0 {
			break // 触顶
		}
		if firstStart >= before {
			t.Fatalf("firstStart=%d 未前移(before=%d)→ 会死循环", firstStart, before)
		}
		before = firstStart
	}
	if len(got) != len(want) {
		t.Fatalf("反向翻页拼回 %d 行 != 全量 %d 行(windows=%d)", len(got), len(want), windows)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 行不一致\n want=%.80s\n got =%.80s", i, want[i], got[i])
		}
	}
	t.Logf("反向 %d 窗拼回 %d 行,与全量逐行一致", windows, len(got))
}

// TestRawJsonlTailWindowReal — ?tail 首拉(startByteForLastN→rawJsonlSince 到 EOF)应只取末尾一段,
// 且逐行等于全量的末尾段(最新优先、不丢尾)。
func TestRawJsonlTailWindowReal(t *testing.T) {
	path := realTranscript()
	if path == "" {
		t.Skip("无真实大会话 jsonl 夹具,跳过")
	}
	want := allRawLines(path)
	if len(want) == 0 {
		t.Skip("空会话")
	}
	st, _ := os.Stat(path)

	start := startByteForLastN(path, st.Size(), 200, transcriptWindowBytes)
	lines, _ := rawJsonlSince(path, start) // 与 handleSseJsonl 的 tail 首拉同路径
	got := make([]string, len(lines))
	for i, l := range lines {
		got[i] = l.line
	}
	if len(got) == 0 || len(got) > len(want) {
		t.Fatalf("尾窗应取 (0,%d] 行,得 %d", len(want), len(got))
	}
	tail := want[len(want)-len(got):]
	for i := range got {
		if got[i] != tail[i] {
			t.Fatalf("尾窗第 %d 行与全量末尾段不符\n win =%.80s\n want=%.80s", i, got[i], tail[i])
		}
	}
	t.Logf("尾窗 %d 行(全量 %d),逐行匹配末尾段", len(got), len(want))
}
