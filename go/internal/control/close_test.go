package control

// close_test.go — POST /close 的集成测试。用真 tmux(有则跑,无则 Skip)在**隔离的 claude dir**里
// 造一个假会话 + 一个同名 tmux 会话(跑无害的 sleep,非 claude),验证:
//   - 对「当前占用其 tmux 的」会话下发 /close → 真的 kill-session 掉,回执 closed 含该 sid;
//   - 对不存在/离线的 sid → 跳过 not_live,绝不误杀;
//   - 同一 sid 传两次只处理一次(dedup)。
// 隔离要点:SetClaudeDir 指向临时目录(只含本测试造的假会话),tmux 会话名唯一且用毕即杀,
// 不启动 RunScanner(不后台回收),故不碰用户真实会话。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func tmuxAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux 不可用,跳过 /close 集成测试")
	}
}

// writeFakeSession 写一个最小但合法的会话 jsonl(一条 user 消息 → scanOneSession 返回 ok)。
func writeFakeSession(t *testing.T, path, sid, cwd string) {
	t.Helper()
	ev := map[string]any{
		"type":      "user",
		"sessionId": sid,
		"cwd":       cwd,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"message":   map[string]any{"role": "user", "content": "hello close test"},
	}
	line, _ := json.Marshal(ev)
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatalf("write fake session: %v", err)
	}
}

// waitSessionLive 轮询扫描快照,直到 sid 出现且被判 live(容忍 800ms memo TTL)。
func waitSessionLive(t *testing.T, sid string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snaps, err := scanClaudeSessions()
		if err == nil {
			panes := listTmuxPanes()
			own := ownershipFor(panes, loadPaneMap())
			if liveSessionIDs(panes, snaps, own)[sid] {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("会话 %s 迟迟未变 live", sid)
}

func postClose(t *testing.T, req closeReq) map[string]any {
	t.Helper()
	body, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/close", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handleClose(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("close 返回 %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析回执: %v (%s)", err, w.Body.String())
	}
	return resp
}

func closedSids(resp map[string]any) []string {
	out := []string{}
	if arr, ok := resp["closed"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func skipReasons(resp map[string]any) map[string]string {
	out := map[string]string{}
	if arr, ok := resp["skipped"].([]any); ok {
		for _, v := range arr {
			if m, ok := v.(map[string]any); ok {
				sid, _ := m["sid"].(string)
				reason, _ := m["reason"].(string)
				out[sid] = reason
			}
		}
	}
	return out
}

func TestHandleCloseKillsLiveSession(t *testing.T) {
	tmuxAvailable(t)

	root := t.TempDir()
	SetClaudeDir(root)
	t.Cleanup(func() { SetClaudeDir("") })

	sid := "dead0001-0000-4000-8000-000000000001"
	cwd := t.TempDir()
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeSession(t, filepath.Join(proj, sid+".jsonl"), sid, cwd)

	name := "cc-" + sid[:8] // cc-dead0001
	_ = tmuxCmd("kill-session", "-t", "="+name).Run()
	if out, err := tmuxCmd("new-session", "-d", "-s", name, "-c", cwd, "sleep 600").CombinedOutput(); err != nil {
		t.Fatalf("建 tmux 会话失败: %v %s", err, out)
	}
	t.Cleanup(func() { _ = tmuxCmd("kill-session", "-t", "="+name).Run() })

	waitSessionLive(t, sid)

	// dedup:同一 sid 传两次,只应关一次。
	resp := postClose(t, closeReq{Sessions: []string{sid, sid}})
	if got := closedSids(resp); len(got) != 1 || got[0] != sid {
		t.Fatalf("closed 期望恰含一个 %s,得到 %v(完整回执 %v)", sid, got, resp)
	}
	if tmuxSessionLive(name) {
		t.Fatalf("/close 后 tmux 会话 %s 仍在跑(没杀掉)", name)
	}
}

func TestHandleCloseSkipsUnknownSession(t *testing.T) {
	tmuxAvailable(t)

	root := t.TempDir()
	SetClaudeDir(root)
	t.Cleanup(func() { SetClaudeDir("") })

	// 隔离 dir 里一个会话都没有 → 任何 sid 都非 live。
	unknown := "beef0002-0000-4000-8000-000000000002"
	// 让 memo 反映空 dir。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if snaps, err := scanClaudeSessions(); err == nil {
			found := false
			for _, s := range snaps {
				if s.SessionID == unknown {
					found = true
				}
			}
			if !found {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	resp := postClose(t, closeReq{Sessions: []string{unknown}})
	if len(closedSids(resp)) != 0 {
		t.Fatalf("未知 sid 不该被关: %v", resp)
	}
	if r := skipReasons(resp)[unknown]; r != "not_live" {
		t.Fatalf("未知 sid 应跳过 not_live,得到 %q(完整 %v)", r, resp)
	}
}

func TestHandleCloseRejectsEmpty(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/close", bytes.NewReader([]byte(`{"sessions":[]}`)))
	w := httptest.NewRecorder()
	handleClose(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空 sessions 应 400,得到 %d", w.Code)
	}
}
