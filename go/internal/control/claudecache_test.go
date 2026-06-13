package control

// claudecache_test.go — Goal A 扫描缓存(claudescan.go)的回归:
//   - 缓存命中:同一文件不变,二次扫描复用上次解析(map 只有一条目);
//   - 失效:追加一行(size 变)→ 重扫拾起新 MsgCount;
//   - 逐出:删除文件 → pruneScanCache 把它清掉;
//   - 并发安全:-race 下多 goroutine 猛打 cachedScanOne 不炸。
//
// 用 t.TempDir() + SetClaudeDir 隔离真实 ~/.claude;每个子测重置包级缓存(scanCache/memoSnaps)。

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// resetScanState 清空包级缓存(子测之间互不串台:memo 否则会跨子测放陈旧)。
func resetScanState() {
	scanMu.Lock()
	scanCache = map[string]scanCacheEntry{}
	scanMu.Unlock()
	memoMu.Lock()
	memoSnaps, memoAt = nil, time.Time{}
	memoMu.Unlock()
}

// writeSession 在 <root>/<project>/<sid>.jsonl 写若干行(每行一个 user/assistant 事件)。
func writeSession(t *testing.T, root, project, sid string, lines []string) string {
	t.Helper()
	pdir := filepath.Join(root, project)
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(pdir, sid+".jsonl")
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func joinLines(lines []string) string {
	s := ""
	for _, l := range lines {
		s += l + "\n"
	}
	return s
}

// userLine / asstLine 造最小可解析的 jsonl 事件行。
func userLine(sid, cwd, ts, text string) string {
	return `{"type":"user","sessionId":"` + sid + `","cwd":"` + cwd + `","timestamp":"` + ts +
		`","message":{"role":"user","content":"` + text + `"}}`
}

func asstLine(sid, ts, text string) string {
	return `{"type":"assistant","sessionId":"` + sid + `","timestamp":"` + ts +
		`","message":{"role":"assistant","content":"` + text + `","model":"claude-opus"}}`
}

func TestScanCacheHitAndInvalidate(t *testing.T) {
	root := t.TempDir()
	SetClaudeDir(root)
	defer SetClaudeDir("")
	resetScanState()

	const sid = "aaaaaaaa-1111-2222-3333-444444444444"
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSession(t, root, "proj", sid, []string{
		userLine(sid, cwd, "2026-06-06T10:00:00Z", "hi"),
		asstLine(sid, "2026-06-06T10:00:01Z", "hello"),
	})

	// 1) 首扫:文件入缓存。
	snaps, err := scanClaudeSessions()
	if err != nil {
		t.Fatalf("scan1: %v", err)
	}
	if len(snaps) != 1 || snaps[0].SessionID != sid {
		t.Fatalf("首扫应得 1 个会话 %q,得 %+v", sid, snaps)
	}
	if snaps[0].MsgCount != 2 {
		t.Fatalf("首扫 MsgCount 应为 2,得 %d", snaps[0].MsgCount)
	}
	if n := cacheLen(); n != 1 {
		t.Fatalf("缓存应有 1 条,得 %d", n)
	}

	// 2) 文件未变,绕过 memo 再扫一次 → 仍是 1 条缓存(命中,不新增)。
	resetMemo()
	snaps2, _ := scanClaudeSessions()
	if len(snaps2) != 1 || snaps2[0].MsgCount != 2 {
		t.Fatalf("二扫(命中)应一致,得 %+v", snaps2)
	}
	if n := cacheLen(); n != 1 {
		t.Fatalf("命中后缓存仍应 1 条,得 %d", n)
	}

	// 3) 追加一行(size 变)→ 重扫应拾起新 MsgCount。
	path := filepath.Join(root, "proj", sid+".jsonl")
	appendLine(t, path, asstLine(sid, "2026-06-06T10:00:02Z", "more"))
	resetMemo()
	snaps3, _ := scanClaudeSessions()
	if len(snaps3) != 1 || snaps3[0].MsgCount != 3 {
		t.Fatalf("追加后 MsgCount 应为 3,得 %+v", snaps3)
	}

	// 4) 删除文件 → pruneScanCache 应清掉它,缓存归零。
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	resetMemo()
	snaps4, _ := scanClaudeSessions()
	if len(snaps4) != 0 {
		t.Fatalf("删后应无会话,得 %+v", snaps4)
	}
	if n := cacheLen(); n != 0 {
		t.Fatalf("删后缓存应逐出归零,得 %d", n)
	}
}

// TestScanCacheConcurrent 在 -race 下多 goroutine 同时扫不同/相同文件,验证锁正确。
func TestScanCacheConcurrent(t *testing.T) {
	root := t.TempDir()
	SetClaudeDir(root)
	defer SetClaudeDir("")
	resetScanState()

	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	// 多个会话文件,逼 cachedScanOne 并发命中/回填不同 key。
	for i := 0; i < 8; i++ {
		sid := "sess" + string(rune('a'+i)) + "aaa-1111-2222-3333-444444444444"
		writeSession(t, root, "proj", sid, []string{
			userLine(sid, cwd, "2026-06-06T10:00:00Z", "q"),
			asstLine(sid, "2026-06-06T10:00:01Z", "a"),
		})
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				resetMemo() // 强制每次真走逐文件缓存路径(否则全 memo 命中,测不到 cachedScanOne)
				if _, err := scanClaudeSessions(); err != nil {
					t.Errorf("concurrent scan: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// ── 测试辅助:窥探/重置包级缓存状态 ───────────────────────────────────────────

func cacheLen() int {
	scanMu.Lock()
	defer scanMu.Unlock()
	return len(scanCache)
}

func resetMemo() {
	memoMu.Lock()
	memoSnaps, memoAt = nil, time.Time{}
	memoMu.Unlock()
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(line + "\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
}
