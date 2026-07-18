package hostagent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeDocker(t *testing.T) (string, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "docker.log")
	script := filepath.Join(t.TempDir(), "docker")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DOCKER_TEST_LOG\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_TEST_LOG", logPath)
	return script, logPath
}

func TestClearDataKeepsIdentityAndRestarts(t *testing.T) {
	bin, logPath := fakeDocker(t)
	h := Handler(Config{Docker: bin})
	req := httptest.NewRequest(http.MethodPost, "/clear-data", strings.NewReader(`{"device_id":"dev-1"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clear-data: %d %s", w.Code, w.Body.String())
	}
	var got map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || !got["ok"] {
		t.Fatalf("bad response: err=%v body=%s", err, w.Body.String())
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logBytes)
	for _, want := range []string{
		"start ccfly-dev-1",
		"exec -u app ccfly-dev-1 sh -c",
		"find /home/app/workspace -mindepth 1 -delete",
		"find /home/app/.claude -mindepth 1 -delete",
		"rm -f /home/app/.claude.json /home/app/.ccfly/panemap.json",
		"restart ccfly-dev-1",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("docker log missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "conn-") {
		t.Fatalf("clear-data must preserve the instance connection identity:\n%s", log)
	}
}

func TestClearDataRequiresIdentity(t *testing.T) {
	bin, _ := fakeDocker(t)
	h := Handler(Config{Docker: bin})
	req := httptest.NewRequest(http.MethodPost, "/clear-data", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", w.Code, w.Body.String())
	}
}
