package control

// claudescan_stampede_test.go — 2026-07「/sessions 缓存踩踏把 ccfly 顶到 8G」修复的回归:
//   - single-flight:一轮扫描进行中,N 个并发调用只跑 1 次真扫描、共享结果(scanClaudeSessions);
//   - 行上限:摘要扫描遇超大行(> scanLineCap)跳过该行,但**不丢失其后的行**(scanOneSession/jsonlLinesCapped)。

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestScanSingleFlight:leader 卡在 scanBarrier 制造「扫描进行中」窗口,期间涌入的并发调用应全部走
// single-flight 等待、共享结果,真扫描只发生 1 次。
func TestScanSingleFlight(t *testing.T) {
	root := t.TempDir()
	SetClaudeDir(root)
	defer SetClaudeDir("")
	resetScanState()

	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		sid := "sess" + string(rune('a'+i)) + "aaa-1111-2222-3333-444444444444"
		writeSession(t, root, "proj", sid, []string{
			userLine(sid, cwd, "2026-06-06T10:00:00Z", "q"),
			asstLine(sid, "2026-06-06T10:00:01Z", "a"),
		})
	}

	release := make(chan struct{})
	var scans atomic.Int32
	scanBarrier = func() {
		scans.Add(1)
		<-release // 卡住 leader,维持「扫描进行中」窗口
	}
	defer func() { scanBarrier = nil }()

	const N = 24
	var wg sync.WaitGroup
	got := make([]int, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			snaps, err := scanClaudeSessions()
			if err != nil {
				t.Errorf("scan[%d]: %v", i, err)
			}
			got[i] = len(snaps)
		}(i)
	}
	// 等 leader 进入 barrier(scans==1),再宽限一下让其余 N-1 都进到 single-flight 等待分支
	// (它们见 scanInflight!=nil,不会再调 barrier)。
	waitFor(t, func() bool { return scans.Load() == 1 }, 2*time.Second)
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	if s := scans.Load(); s != 1 {
		t.Fatalf("single-flight 应只跑 1 次真扫描,实际 %d 次", s)
	}
	for i := range got {
		if got[i] != 4 {
			t.Fatalf("并发调用[%d] 应共享同一结果(4 个会话),得 %d", i, got[i])
		}
	}
}

// TestScanLineCapSkipsHugeLineKeepsRest:一条超过 scanLineCap 的巨行,摘要扫描应跳过它(截断后 json
// 解析失败),但**其后的正常行仍被解析计数**——证明「不丢后续行」不变量。
func TestScanLineCapSkipsHugeLineKeepsRest(t *testing.T) {
	root := t.TempDir()
	SetClaudeDir(root)
	defer SetClaudeDir("")
	resetScanState()

	const sid = "bbbbbbbb-1111-2222-3333-444444444444"
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	// 行1 正常;行2 巨行(content 2 MiB > 1 MiB 上限);行3 正常且在巨行之后。
	writeSession(t, root, "proj", sid, []string{
		userLine(sid, cwd, "2026-06-06T10:00:00Z", "hi"),
		hugeAsstLine(sid, "2026-06-06T10:00:01Z", 2<<20),
		asstLine(sid, "2026-06-06T10:00:02Z", "after-huge"),
	})

	snaps, err := scanClaudeSessions()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("应得 1 个会话,得 %d", len(snaps))
	}
	s := snaps[0]
	// 巨行被跳过 → MsgCount 只数行1+行3=2(既非 3=没截断,也非 1=把后续行丢了)。
	if s.MsgCount != 2 {
		t.Fatalf("MsgCount 应为 2(巨行跳过、行3 保留),得 %d —— 若为 1 说明丢了巨行后的行(不变量破坏),若为 3 说明未截断", s.MsgCount)
	}
	// 最后一条被解析的应是行3(证明扫描确实推进到了巨行之后)。
	if s.LastTs != "2026-06-06T10:00:02Z" {
		t.Fatalf("LastTs 应来自巨行之后的行3,得 %q", s.LastTs)
	}
	if !strings.Contains(s.Preview, "after-huge") {
		t.Fatalf("Preview 应来自行3(after-huge),得 %q", s.Preview)
	}
}

// hugeAsstLine 造一条 content 为 n 字节的 assistant 事件行(整行远超 scanLineCap)。
func hugeAsstLine(sid, ts string, n int) string {
	return `{"type":"assistant","sessionId":"` + sid + `","timestamp":"` + ts +
		`","message":{"role":"assistant","content":"` + strings.Repeat("x", n) + `","model":"claude-opus"}}`
}

// waitFor 轮询 cond 直至为真或超时。
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}
