package control

import (
	"encoding/json"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
)

// ClaudeLoginStartFn and ClaudeLoginStatusFn are injected by cmd/ccfly. The
// control package cannot import mesh (mesh already imports control), while the
// command owns both the enrolled-cloud identity and the existing login flow.
//
// These endpoints do not introduce a second credential protocol: POST merely
// starts the same `ccfly claude login` path a person can run locally. Through
// cc.hn it is protected by the existing owner-scoped /x/{device} gateway.
var (
	ClaudeLoginStartFn  func(host, email, handoff string) error
	ClaudeLoginStatusFn func() (any, error)
)

const maxClaudeLoginPushBody = 4 << 10

func validClaudeLoginHost(raw string) bool {
	host := strings.TrimSpace(raw)
	if host == "" || len(host) > 255 || strings.Contains(host, "://") || strings.ContainsAny(host, "/\\?#\r\n\t ") {
		return false
	}
	u, err := url.Parse("https://" + host)
	return err == nil && u.Host == host && u.Hostname() != "" && u.User == nil && u.Path == ""
}

func normalizeClaudeLoginEmail(raw string) (string, bool) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > 320 {
		return "", false
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return "", false
	}
	return email, true
}

func validClaudeLoginHandoff(handoff string) bool {
	if handoff == "" {
		return true
	}
	if len(handoff) != 32 {
		return false
	}
	for _, char := range handoff {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func handleClaudeLoginPush(w http.ResponseWriter, r *http.Request) {
	if ClaudeLoginStartFn == nil {
		ctrlErr(w, http.StatusNotImplemented, "该设备版本不支持网页推送 Claude 凭据")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxClaudeLoginPushBody)
	var body struct {
		Host    string `json:"host"`
		Email   string `json:"email"`
		Handoff string `json:"handoff,omitempty"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		ctrlErr(w, http.StatusBadRequest, "bad json")
		return
	}
	body.Host = strings.TrimSpace(body.Host)
	body.Handoff = strings.TrimSpace(body.Handoff)
	email, ok := normalizeClaudeLoginEmail(body.Email)
	if !validClaudeLoginHost(body.Host) || !ok {
		ctrlErr(w, http.StatusUnprocessableEntity, "host 或 email 无效")
		return
	}
	if !validClaudeLoginHandoff(body.Handoff) {
		ctrlErr(w, http.StatusUnprocessableEntity, "handoff 无效")
		return
	}
	if err := ClaudeLoginStartFn(body.Host, email, body.Handoff); err != nil {
		ctrlErr(w, http.StatusConflict, err.Error())
		return
	}
	ctrlJSON(w, http.StatusAccepted, map[string]any{"started": true, "email": email})
}

func handleClaudeLoginStatus(w http.ResponseWriter, _ *http.Request) {
	if ClaudeLoginStatusFn == nil {
		ctrlErr(w, http.StatusNotImplemented, "该设备版本不支持网页查询 Claude 登录状态")
		return
	}
	status, err := ClaudeLoginStatusFn()
	if err != nil {
		ctrlErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if status == nil {
		status = map[string]any{"phase": "idle", "alive": false}
	}
	ctrlJSON(w, http.StatusOK, status)
}
