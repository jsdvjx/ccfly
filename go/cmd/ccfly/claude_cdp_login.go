package main

// claude_cdp_login.go implements the second device login mode:
//
//   local ccfly -> local `claude auth login` -> OAuth authorize URL
//               -> ccfly-cloud -> account cloud browser (CDP)
//               <- one-shot code#state
//   local ccfly -> paste code into the same local Claude process
//               -> Claude persists its own credential locally
//
// Unlike credential delivery, no .credentials.json is generated in or returned
// from the cloud. The existing web account console remains the human surface for
// hCaptcha and the final Authorize confirmation on the remote browser.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jsdvjx/ccfly/go/internal/control"
	"github.com/jsdvjx/ccfly/go/internal/mesh"
)

const (
	claudeCDPLoginTimeout = 20 * time.Minute
	oauthURLTimeout       = 75 * time.Second
	localAuthWriteTimeout = 2 * time.Minute
)

var oauthURLCandidateRE = regexp.MustCompile(`https://[^\x00-\x20\x7f\x1b]+`)

type oauthURLCapture struct {
	mu    sync.Mutex
	buf   string
	urlCh chan string
	once  sync.Once
}

func newOAuthURLCapture() *oauthURLCapture {
	return &oauthURLCapture{urlCh: make(chan string, 1)}
}

func (c *oauthURLCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.buf += string(p)
	if len(c.buf) > 128<<10 {
		c.buf = c.buf[len(c.buf)-(64<<10):]
	}
	buffer := c.buf
	c.mu.Unlock()
	if raw := extractClaudeOAuthURL(buffer); raw != "" {
		c.once.Do(func() { c.urlCh <- raw })
	}
	return len(p), nil
}

func extractClaudeOAuthURL(output string) string {
	for _, candidate := range oauthURLCandidateRE.FindAllString(output, -1) {
		candidate = strings.TrimRight(candidate, `"'<>),.;`)
		u, err := url.Parse(candidate)
		if err != nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
			continue
		}
		switch strings.ToLower(u.Hostname()) {
		case "claude.ai", "claude.com", "platform.claude.com", "console.anthropic.com":
		default:
			continue
		}
		if !strings.Contains(strings.ToLower(u.Path), "/oauth/authorize") || len(u.Query().Get("state")) < 8 {
			continue
		}
		return u.String()
	}
	return ""
}

func envSet(env []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if strings.HasPrefix(strings.ToUpper(item), prefix) {
			continue
		}
		out = append(out, item)
	}
	return append(out, key+"="+value)
}

type cdpLoginStartResp struct {
	Status        string `json:"status"`
	Email         string `json:"email"`
	RequestID     string `json:"request_id"`
	SelectionID   string `json:"selection_id"`
	WorkbenchPath string `json:"workbench_path"`
}

type cdpLoginPollResp struct {
	Status      string `json:"status"`
	Email       string `json:"email"`
	RequestID   string `json:"request_id"`
	SelectionID string `json:"selection_id"`
	Phase       string `json:"phase"`
	Detail      string `json:"detail"`
	FlowNode    string `json:"flow_node"`
	Code        string `json:"code"`
}

func cdpLoginStart(ctx context.Context, tgt mesh.LoginTarget, email, oauthURL string) (cdpLoginStartResp, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "oauth_url": oauthURL})
	var out cdpLoginStartResp
	if err := loginDo(ctx, tgt, "POST", "/api/device/login/cdp/start", nil, body, &out); err != nil {
		return cdpLoginStartResp{}, err
	}
	if out.Status == "selection_required" && out.SelectionID != "" {
		return out, nil
	}
	if out.Email == "" || out.RequestID == "" {
		return cdpLoginStartResp{}, errors.New("云端 CDP 登录未返回账号或 request_id")
	}
	return out, nil
}

func cdpLoginPoll(ctx context.Context, tgt mesh.LoginTarget, email, requestID, selectionID string) (cdpLoginPollResp, error) {
	var out cdpLoginPollResp
	q := url.Values{}
	if selectionID != "" {
		q.Set("selection_id", selectionID)
	} else {
		q.Set("email", email)
		q.Set("request_id", requestID)
	}
	if err := loginDo(ctx, tgt, "GET", "/api/device/login/cdp/poll", q, nil, &out); err != nil {
		return cdpLoginPollResp{}, err
	}
	return out, nil
}

func cdpLoginComplete(ctx context.Context, tgt mesh.LoginTarget, email, requestID, selectionID string) error {
	body, _ := json.Marshal(map[string]string{
		"email": email, "request_id": requestID, "selection_id": selectionID,
	})
	return loginDo(ctx, tgt, "POST", "/api/device/login/cdp/complete", nil, body, nil)
}

func cdpWorkbenchURL(tgt mesh.LoginTarget, started cdpLoginStartResp) string {
	path := strings.TrimSpace(started.WorkbenchPath)
	if !strings.HasPrefix(path, "/workbench?") {
		q := url.Values{}
		if started.SelectionID != "" {
			q.Set("ccfly_cdp_login", started.SelectionID)
		} else {
			q.Set("eu_workbench", started.Email)
			q.Set("eu_tool", "oauth")
		}
		path = "/workbench?" + q.Encode()
	}
	return loginBase(tgt) + path
}

// openCDPWorkbench is best-effort. The URL is also printed, so headless agents
// and machines without a desktop still have a complete manual fallback.
func openCDPWorkbench(rawURL string) {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{rawURL}
	case "linux":
		name, args = "xdg-open", []string{rawURL}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}
	default:
		return
	}
	bin, err := exec.LookPath(name)
	if err == nil {
		_ = exec.Command(bin, args...).Start()
	}
}

func terminalCDPLoginHTTPError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, code := range []string{"HTTP 400", "HTTP 401", "HTTP 403", "HTTP 404", "HTTP 409", "HTTP 410", "HTTP 422"} {
		if strings.Contains(message, code) {
			return true
		}
	}
	return false
}

func completeCDPLoginWithRetry(ctx context.Context, tgt mesh.LoginTarget, email, requestID, selectionID string) error {
	var last error
	for i, delay := range []time.Duration{0, 500 * time.Millisecond, time.Second, 2 * time.Second} {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		if last = cdpLoginComplete(ctx, tgt, email, requestID, selectionID); last == nil {
			return nil
		}
		if terminalCDPLoginHTTPError(last) {
			break
		}
	}
	return last
}

func runClaudeCDPLogin(parent context.Context, tgt mesh.LoginTarget, requestedEmail string) (runErr error) {
	email, err := normalizeAutoEmail(requestedEmail)
	if err != nil {
		return err
	}
	if current, _ := mesh.LoadLoginStatus(); current != nil && !current.Terminal() && current.PID > 0 && processRunning(current.PID) {
		return fmt.Errorf("已有 Claude 登录任务在运行 (PID %d)", current.PID)
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return errors.New("未找到 Claude CLI；请先安装并确保 `claude` 在 PATH 中")
	}

	ctx, cancel := context.WithTimeout(parent, claudeCDPLoginTimeout)
	defer cancel()
	startedAt := time.Now().Unix()
	currentRequestID := ""
	setStatus := func(phase, account, requestID, cloudStatus, errMessage string) {
		_ = mesh.SaveLoginStatus(mesh.LoginStatus{
			Phase: phase, Host: tgt.Host, Email: account, JobID: requestID,
			PID: os.Getpid(), Error: errMessage, StartedAt: startedAt, CloudStatus: cloudStatus,
		})
	}
	setStatus(mesh.LoginPhaseStarting, email, "", "local_oauth_starting", "")
	defer func() {
		if runErr != nil {
			setStatus(mesh.LoginPhaseFailed, email, currentRequestID, "cdp_failed", runErr.Error())
		}
	}()

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find ccfly executable: %w", err)
	}
	args := []string{"auth", "login", "--claudeai"}
	if email != "" {
		args = append(args, "--email", email)
	}
	authCtx, stopAuth := context.WithCancel(ctx)
	defer stopAuth()
	cmd := exec.CommandContext(authCtx, "claude", args...)
	cmd.Env = envSet(os.Environ(), "BROWSER", self)
	cmd.Env = envSet(cmd.Env, "CCFLY_CDP_LOGIN_BROWSER_SINK", "1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	capture := newOAuthURLCapture()
	cmd.Stdout, cmd.Stderr = capture, capture
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 Claude 本地登录失败: %w", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	defer func() {
		stopAuth()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	var oauthURL string
	select {
	case oauthURL = <-capture.urlCh:
	case authErr := <-waitCh:
		if authErr == nil {
			return errors.New("Claude CLI 已退出但未产生 OAuth 授权地址")
		}
		return fmt.Errorf("Claude CLI 在产生授权地址前退出: %w", authErr)
	case <-time.After(oauthURLTimeout):
		return errors.New("等待 Claude CLI OAuth 授权地址超时")
	case <-ctx.Done():
		return ctx.Err()
	}

	fmt.Fprintln(os.Stderr, "ccfly: 已取得本地 Claude OAuth 请求，正在连接账号的云端浏览器…")
	started, err := cdpLoginStart(ctx, tgt, email, oauthURL)
	if err != nil {
		return fmt.Errorf("启动云端 CDP 登录失败: %w", err)
	}
	selectionID := started.SelectionID
	email = started.Email
	currentRequestID = started.RequestID
	cloudStatus := "cdp_starting"
	if selectionID != "" {
		cloudStatus = "awaiting_account_selection"
	}
	setStatus(mesh.LoginPhasePolling, email, firstNonEmpty(currentRequestID, selectionID), cloudStatus, "")
	workbench := cdpWorkbenchURL(tgt, started)
	if selectionID != "" {
		fmt.Fprintln(os.Stderr, "ccfly: OAuth 请求已发送到云端。请在工作台选择本次登录使用的 Claude 账号：")
	} else {
		fmt.Fprintln(os.Stderr, "ccfly: 云端浏览器已启动。请在账号控制台完成 hCaptcha / Authorize：")
	}
	fmt.Fprintln(os.Stderr, "  "+workbench)
	openCDPWorkbench(workbench)

	lastProgress, lastPollError := "", ""
	selectionPrompted := selectionID == ""
	selectionStarted := selectionID == ""
	var oauthCode string
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for oauthCode == "" {
		polled, pollErr := cdpLoginPoll(ctx, tgt, email, currentRequestID, selectionID)
		if pollErr != nil {
			if terminalCDPLoginHTTPError(pollErr) {
				return fmt.Errorf("云端 CDP 登录轮询失败: %w", pollErr)
			}
			if pollErr.Error() != lastPollError {
				fmt.Fprintf(os.Stderr, "ccfly: 云端状态暂不可达，继续重试：%v\n", pollErr)
				lastPollError = pollErr.Error()
			}
		} else {
			lastPollError = ""
			if polled.Email != "" {
				email = polled.Email
			}
			if polled.RequestID != "" {
				currentRequestID = polled.RequestID
			}
			if email != "" && !selectionStarted {
				fmt.Fprintf(os.Stderr, "ccfly: 已选择 %s，正在启动云端浏览器…\n", email)
				selectionStarted = true
			}
			progress := strings.TrimSpace(polled.Phase + ":" + polled.Detail)
			if progress != ":" && progress != lastProgress {
				fmt.Fprintf(os.Stderr, "  云端浏览器：%s\n", strings.Trim(progress, ":"))
				lastProgress = progress
			}
			setStatus(mesh.LoginPhasePolling, email, firstNonEmpty(currentRequestID, selectionID), firstNonEmpty(polled.Phase, polled.Status), "")
			switch polled.Status {
			case "selection_required":
				if !selectionPrompted {
					fmt.Fprintln(os.Stderr, "ccfly: 等待你在云端工作台选择 Claude 账号…")
					selectionPrompted = true
				}
			case "succeeded":
				oauthCode = polled.Code
				if oauthCode == "" {
					return errors.New("云端 OAuth 已完成但未返回 code#state")
				}
			case "failed":
				return fmt.Errorf("云端浏览器登录失败: %s", firstNonEmpty(polled.Detail, polled.Phase, "unknown error"))
			}
		}
		if oauthCode != "" {
			break
		}
		select {
		case authErr := <-waitCh:
			if authErr == nil {
				return errors.New("Claude CLI 在云端授权完成前退出")
			}
			return fmt.Errorf("Claude CLI 在云端授权完成前退出: %w", authErr)
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if email == "" || currentRequestID == "" {
		return errors.New("云端 OAuth 完成但未返回所选账号或 request_id")
	}
	setStatus(mesh.LoginPhaseWriting, email, currentRequestID, "local_credential_write", "")
	fmt.Fprintln(os.Stderr, "ccfly: 云端授权完成，正在把一次性结果交回本地 Claude CLI…")
	if _, err := io.WriteString(stdin, oauthCode+"\n"); err != nil {
		return fmt.Errorf("回填 OAuth 结果失败: %w", err)
	}
	_ = stdin.Close()
	select {
	case authErr := <-waitCh:
		if authErr != nil {
			return fmt.Errorf("Claude CLI 未能保存本地登录: %w", authErr)
		}
	case <-time.After(localAuthWriteTimeout):
		return errors.New("Claude CLI 保存本地登录超时")
	case <-ctx.Done():
		return ctx.Err()
	}

	// Local durability is the commit point. Cloud cleanup/SNI refresh is retried,
	// but a transient cleanup failure must not misreport an already-working local login.
	if err := completeCDPLoginWithRetry(ctx, tgt, email, currentRequestID, selectionID); err != nil {
		fmt.Fprintf(os.Stderr, "ccfly: 警告：本地登录已完成，云端一次性结果将由 TTL 清理：%v\n", err)
	}
	control.TrustFolder("")
	if err := mesh.SaveClaudeLoginContext(mesh.ClaudeLoginContext{Host: tgt.Host, AccountEmail: email}); err != nil {
		fmt.Fprintf(os.Stderr, "ccfly: 警告：保存账号路由上下文失败：%v\n", err)
	}
	setStatus(mesh.LoginPhaseSucceeded, email, currentRequestID, "succeeded", "")
	fmt.Fprintf(os.Stderr, "✓ Claude 已通过云端 CDP 浏览器完成登录：%s\n", email)
	return nil
}
