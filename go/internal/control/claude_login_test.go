package control

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func withClaudeLoginHooks(t *testing.T, start func(string, string, string) error, status func() (any, error)) {
	t.Helper()
	oldStart, oldStatus := ClaudeLoginStartFn, ClaudeLoginStatusFn
	ClaudeLoginStartFn, ClaudeLoginStatusFn = start, status
	t.Cleanup(func() { ClaudeLoginStartFn, ClaudeLoginStatusFn = oldStart, oldStatus })
}

func TestClaudeLoginPushStartsExistingDeviceFlow(t *testing.T) {
	var gotHost, gotEmail, gotHandoff string
	handoff := strings.Repeat("A", 32)
	withClaudeLoginHooks(t, func(host, email, handoff string) error {
		gotHost, gotEmail, gotHandoff = host, email, handoff
		return nil
	}, nil)
	req := httptest.NewRequest(http.MethodPost, "/claude/login", strings.NewReader(`{"host":"cc.hn","email":"Owner@Example.com","handoff":"`+handoff+`"}`))
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if gotHost != "cc.hn" || gotEmail != "owner@example.com" || gotHandoff != handoff {
		t.Fatalf("start got host=%q email=%q handoff=%q", gotHost, gotEmail, gotHandoff)
	}
}

func TestClaudeLoginPushRejectsInvalidInputAndBusyDevice(t *testing.T) {
	withClaudeLoginHooks(t, func(_, _, _ string) error { return errors.New("已有登录任务") }, nil)
	for _, body := range []string{
		`{"host":"https://cc.hn","email":"owner@example.com"}`,
		`{"host":"cc.hn","email":"not-an-email"}`,
		`{"host":"cc.hn","email":"owner@example.com","handoff":"too-short"}`,
	} {
		w := httptest.NewRecorder()
		Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/claude/login", strings.NewReader(body)))
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid body %s: status=%d", body, w.Code)
		}
	}
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/claude/login", strings.NewReader(`{"host":"cc.hn","email":"owner@example.com"}`)))
	if w.Code != http.StatusConflict {
		t.Fatalf("busy status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestClaudeLoginStatusUsesInjectedDeviceState(t *testing.T) {
	withClaudeLoginHooks(t, nil, func() (any, error) {
		return map[string]any{"phase": "polling", "email": "owner@example.com", "alive": true}, nil
	})
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/claude/login/status", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"phase":"polling"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
