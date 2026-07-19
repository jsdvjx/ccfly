package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jsdvjx/ccfly/go/internal/mesh"
)

func TestExtractClaudeOAuthURLFromPlainAndOSCHyperlinkOutput(t *testing.T) {
	want := "https://claude.com/cai/oauth/authorize?code=true&state=abcdefgh12345678"
	for _, output := range []string{
		"If the browser did not open, visit: " + want + "\nPaste code here > ",
		"\x1b]8;;" + want + "\aOpen browser\x1b]8;;\a",
		"ignore https://evil.example/oauth/authorize?state=abcdefgh then " + want,
	} {
		if got := extractClaudeOAuthURL(output); got != want {
			t.Fatalf("extract=%q want=%q from %q", got, want, output)
		}
	}
	for _, output := range []string{
		"https://evil.example/oauth/authorize?state=abcdefgh",
		"https://claude.com/cai/oauth/authorize?state=short",
		"https://claude.com/login?state=abcdefgh12345678",
	} {
		if got := extractClaudeOAuthURL(output); got != "" {
			t.Fatalf("untrusted output extracted %q", got)
		}
	}
}

func TestCDPLoginDeviceProtocol(t *testing.T) {
	const requestID = "abcdefghijklmnopqrstuvwx"
	var completed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "mesh-secret" {
			t.Errorf("missing mesh token: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/device/login/cdp/start":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["email"] != "owner@example.com" || !strings.Contains(body["oauth_url"], "state=abcdefgh") {
				t.Errorf("start body=%+v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "started", "email": body["email"], "request_id": requestID,
				"workbench_path": "/workbench?eu_workbench=owner%40example.com&eu_tool=oauth",
			})
		case "/api/device/login/cdp/poll":
			if r.URL.Query().Get("email") != "owner@example.com" || r.URL.Query().Get("request_id") != requestID {
				t.Errorf("poll query=%s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "succeeded", "email": "owner@example.com", "request_id": requestID,
				"phase": "done", "code": "AUTH#STATE",
			})
		case "/api/device/login/cdp/complete":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			completed = body["email"] == "owner@example.com" && body["request_id"] == requestID
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	tgt := mesh.LoginTarget{Scheme: u.Scheme, Host: u.Host, MeshToken: "mesh-secret"}
	ctx := context.Background()

	started, err := cdpLoginStart(ctx, tgt, "owner@example.com", "https://claude.com/cai/oauth/authorize?state=abcdefgh")
	if err != nil {
		t.Fatal(err)
	}
	if started.RequestID != requestID || !strings.Contains(cdpWorkbenchURL(tgt, started), "/workbench?") {
		t.Fatalf("started=%+v url=%s", started, cdpWorkbenchURL(tgt, started))
	}
	polled, err := cdpLoginPoll(ctx, tgt, started.Email, started.RequestID, "")
	if err != nil || polled.Code != "AUTH#STATE" {
		t.Fatalf("poll=%+v err=%v", polled, err)
	}
	if err := cdpLoginComplete(ctx, tgt, started.Email, started.RequestID, ""); err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("complete body not delivered")
	}
}

func TestCDPLoginDeviceProtocolWaitsForWebAccountSelection(t *testing.T) {
	const (
		selectionID = "0123456789abcdef0123456789abcdef01234567"
		requestID   = "abcdefghijklmnopqrstuvwx"
	)
	var completed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/device/login/cdp/start":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["email"] != "" {
				t.Errorf("chooser start unexpectedly selected %q", body["email"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "selection_required", "selection_id": selectionID,
				"workbench_path": "/workbench?ccfly_cdp_login=" + selectionID,
			})
		case "/api/device/login/cdp/poll":
			if r.URL.Query().Get("selection_id") != selectionID || r.URL.Query().Get("email") != "" {
				t.Errorf("selection poll query=%s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "succeeded", "selection_id": selectionID,
				"email": "chosen@example.com", "request_id": requestID, "code": "AUTH#STATE",
			})
		case "/api/device/login/cdp/complete":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			completed = body["selection_id"] == selectionID && body["email"] == "chosen@example.com" && body["request_id"] == requestID
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	tgt := mesh.LoginTarget{Scheme: u.Scheme, Host: u.Host, MeshToken: "mesh-secret"}
	ctx := context.Background()

	started, err := cdpLoginStart(ctx, tgt, "", "https://claude.com/cai/oauth/authorize?state=abcdefgh")
	if err != nil || started.SelectionID != selectionID {
		t.Fatalf("started=%+v err=%v", started, err)
	}
	if got := cdpWorkbenchURL(tgt, started); !strings.Contains(got, "ccfly_cdp_login="+selectionID) {
		t.Fatalf("workbench=%s", got)
	}
	polled, err := cdpLoginPoll(ctx, tgt, "", "", started.SelectionID)
	if err != nil || polled.Email != "chosen@example.com" || polled.RequestID != requestID {
		t.Fatalf("poll=%+v err=%v", polled, err)
	}
	if err := cdpLoginComplete(ctx, tgt, polled.Email, polled.RequestID, started.SelectionID); err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("selection binding was not delivered on completion")
	}
}
