package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jsdvjx/ccfly/go/internal/mesh"
)

const claudeAutoHandoffTTL = 30 * time.Minute

var claudeAutoNonceRE = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)

type claudeAutoHandoff struct {
	Version   int    `json:"version"`
	Host      string `json:"host"`
	DeviceID  string `json:"device_id"`
	Email     string `json:"email,omitempty"`
	Handoff   string `json:"handoff"`
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"`
}

func claudeAutoHandoffDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".ccfly", "claude-auto-handoffs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func normalizeAutoEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", nil
	}
	parsed, err := mail.ParseAddress(email)
	if len(email) > 320 || err != nil || parsed.Address != email {
		return "", fmt.Errorf("--email 无效")
	}
	return email, nil
}

func createClaudeAutoHandoff(tgt mesh.LoginTarget, email string, now time.Time) (string, error) {
	if tgt.DeviceID == "" {
		return "", fmt.Errorf("本机云端身份缺少 device_id，请先重新连接 %s", tgt.Host)
	}
	normalizedEmail, err := normalizeAutoEmail(email)
	if err != nil {
		return "", err
	}
	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("生成登录接力码失败: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	handoff := claudeAutoHandoff{
		Version: 1, Host: tgt.Host, DeviceID: tgt.DeviceID, Email: normalizedEmail,
		Handoff: nonce, IssuedAt: now.Unix(), ExpiresAt: now.Add(claudeAutoHandoffTTL).Unix(),
	}
	dir, err := claudeAutoHandoffDir()
	if err != nil {
		return "", err
	}
	cleanupClaudeAutoHandoffs(dir, now)
	record, err := json.Marshal(handoff)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, nonce+".json")
	if err := os.WriteFile(path, record, 0o600); err != nil {
		return "", fmt.Errorf("保存登录接力状态失败: %w", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(record)
	scheme := strings.TrimSpace(tgt.Scheme)
	if scheme == "" {
		scheme = "https"
	}
	u := url.URL{Scheme: scheme, Host: tgt.Host, Path: "/accounts"}
	q := u.Query()
	q.Set("claude_auto", payload)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func validateClaudeAutoHandoff(tgt mesh.LoginTarget, email, nonce string, now time.Time) (string, error) {
	if !claudeAutoNonceRE.MatchString(nonce) {
		return "", fmt.Errorf("登录接力码无效")
	}
	dir, err := claudeAutoHandoffDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, nonce+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("登录接力码不存在或已使用")
		}
		return "", err
	}
	var saved claudeAutoHandoff
	if json.Unmarshal(data, &saved) != nil || saved.Version != 1 || saved.Handoff != nonce || saved.DeviceID == "" {
		return "", fmt.Errorf("登录接力状态损坏")
	}
	if saved.ExpiresAt <= now.Unix() {
		_ = os.Remove(path)
		return "", fmt.Errorf("登录接力链接已过期，请重新运行 ccfly claude login --auto")
	}
	if !strings.EqualFold(saved.Host, tgt.Host) || saved.DeviceID != tgt.DeviceID {
		return "", fmt.Errorf("登录接力链接不属于当前设备")
	}
	normalizedEmail, err := normalizeAutoEmail(email)
	if err != nil || normalizedEmail == "" {
		return "", fmt.Errorf("登录接力账号无效")
	}
	if saved.Email != "" && saved.Email != normalizedEmail {
		return "", fmt.Errorf("登录接力链接指定了另一个账号")
	}
	return path, nil
}

func cleanupClaudeAutoHandoffs(dir string, now time.Time) {
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		var saved claudeAutoHandoff
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &saved) != nil || saved.ExpiresAt <= now.Unix() {
			_ = os.Remove(path)
		}
	}
}
