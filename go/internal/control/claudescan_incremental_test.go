package control

// claudescan_incremental_test.go — 2026-07「增量扫描」回归:增长的 append-only 会话只读新尾、结果与
// 全量一致(避免几百 MB 活跃会话每轮重读整文件——ccfly connect CPU 高的根因)。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScanIncrementalMatchesFull:追加后再扫应走增量(scanFrom 从非零偏移续读),且结果与清缓存全量
// 重扫逐字段一致。
func TestScanIncrementalMatchesFull(t *testing.T) {
	root := t.TempDir()
	SetClaudeDir(root)
	defer SetClaudeDir("")
	resetScanState()

	const sid = "cccccccc-1111-2222-3333-444444444444"
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSession(t, root, "proj", sid, []string{
		userLine(sid, cwd, "2026-06-06T10:00:00Z", "q1"),
		asstLine(sid, "2026-06-06T10:00:01Z", "a1"),
	})

	var starts []int64
	scanFromHook = func(o int64) { starts = append(starts, o) }
	defer func() { scanFromHook = nil }()

	// 首扫:全量,scanFrom 从 0 起。
	s1, _ := scanClaudeSessions()
	if len(s1) != 1 || s1[0].MsgCount != 2 || s1[0].Turns != 1 {
		t.Fatalf("首扫应 1 会话 MsgCount=2 Turns=1,得 %+v", s1)
	}
	if len(starts) != 1 || starts[0] != 0 {
		t.Fatalf("首扫应全量(scanFrom startOff=0),得 %v", starts)
	}

	// 追加 3 条 → size 变大 → 增量路径。
	path := filepath.Join(root, "proj", sid+".jsonl")
	appendLine(t, path, userLine(sid, cwd, "2026-06-06T10:00:02Z", "q2"))
	appendLine(t, path, asstLine(sid, "2026-06-06T10:00:03Z", "a2"))
	appendLine(t, path, asstLine(sid, "2026-06-06T10:00:04Z", "a3-last"))

	starts = nil
	resetMemo() // 绕过 memo,真走 cachedScanOne(scanCache 保留 → 命中增量)
	s2, _ := scanClaudeSessions()
	if s2[0].MsgCount != 5 || s2[0].Turns != 2 {
		t.Fatalf("增量后应 MsgCount=5 Turns=2,得 %+v", s2[0])
	}
	if !strings.Contains(s2[0].Preview, "a3-last") {
		t.Fatalf("增量后 preview 应来自最后一条,得 %q", s2[0].Preview)
	}
	// 关键:增量应从非零偏移续读(= 没重读整文件)。
	if len(starts) != 1 || starts[0] == 0 {
		t.Fatalf("增量应从非零偏移续读(证明没重读整文件),得 startOffs=%v", starts)
	}

	// 对照:清空缓存全量重扫,结果应与增量逐字段一致。
	resetScanState()
	scanFromHook = nil
	s3, _ := scanClaudeSessions()
	a, b := s2[0], s3[0]
	if a.MsgCount != b.MsgCount || a.Turns != b.Turns || a.Preview != b.Preview ||
		a.LastTs != b.LastTs || a.LastRole != b.LastRole || a.Cwd != b.Cwd || a.SessionID != b.SessionID {
		t.Fatalf("增量结果应与全量一致:\n增量=%+v\n全量=%+v", a, b)
	}
}

// TestScanIncrementalPartialTrailingLine:文件末尾有半截行(正在写,无 \n)不被计入;等它写完追加后,
// 增量把它当完整行读一次(不重不漏)——验证 off 停在行边界。
func TestScanIncrementalPartialTrailingLine(t *testing.T) {
	root := t.TempDir()
	SetClaudeDir(root)
	defer SetClaudeDir("")
	resetScanState()

	const sid = "dddddddd-1111-2222-3333-444444444444"
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	pdir := filepath.Join(root, "proj")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(pdir, sid+".jsonl")

	// 1 条完整行 + 1 条半截行(故意无 \n,模拟正在追加写)。
	full := userLine(sid, cwd, "2026-06-06T10:00:00Z", "q1") + "\n"
	partial := asstLine(sid, "2026-06-06T10:00:01Z", "a1-partial")
	if err := os.WriteFile(path, []byte(full+partial), 0o644); err != nil {
		t.Fatal(err)
	}
	s1, _ := scanClaudeSessions()
	if s1[0].MsgCount != 1 {
		t.Fatalf("半截行不应计入,MsgCount 应为 1,得 %d", s1[0].MsgCount)
	}

	// 真·追加:补完半截行(\n)+ 再加一条。
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString("\n" + asstLine(sid, "2026-06-06T10:00:02Z", "a2") + "\n"); err != nil {
		t.Fatal(err)
	}
	fh.Close()

	resetMemo()
	s2, _ := scanClaudeSessions()
	if s2[0].MsgCount != 3 {
		t.Fatalf("补完半截行 + 追加后应 MsgCount=3(q1+a1+a2,a1 只算一次),得 %d", s2[0].MsgCount)
	}

	// 对照全量。
	resetScanState()
	s3, _ := scanClaudeSessions()
	if s3[0].MsgCount != s2[0].MsgCount {
		t.Fatalf("增量(半截行处理)应与全量一致:增量=%d 全量=%d", s2[0].MsgCount, s3[0].MsgCount)
	}
}
