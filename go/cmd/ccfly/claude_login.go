package main

// claude_login.go — `ccfly claude login`:经 ccfly-cloud 的 Claude account 模块登录,把凭证拉回本机。
//
// 流程(对应 cloud login.go 的设备侧三端点,mesh_token 鉴权):
//   POST /api/device/login/start  {email?[, out_node_ip, egress_v6]} → {job_id, account_email, egress_v6, out_node_ip}
//        email 留空 = 云端在你可访问的共享账号里按 claude 用量 + 分配次数自动选号;给 email 则登该
//        (自有)账号。job 绑定本设备(DeviceID + DevicePubKey=本机 WG 公钥);worker 登进 claude.com、
//        把 .credentials.json 用 NaCl sealed-box 封装到该公钥。
//   GET  /api/device/login/poll   ?job_id= → {status, account_email, egress_v6, out_node_ip[, ciphertext_b64 | reason]}
//   (succeeded)用本机 WG 私钥 box.OpenAnonymous 解开密文 → 落地 ~/.claude/.credentials.json,并把
//        分配结果(账号 + /128 出口)落盘到 ~/.ccfly/claude-login-<host>.json(按账号 /128 路由用,见 mesh)。
//   POST /api/device/login/ack    ?job_id= → 云端抹除密文
//
// 自动选号(无 --email):账号须已被共享给本用户且就绪;给 --email 则复用其控制台已 provision 的出口,
// 或加 --node + --egress 一次性 provision+登录。首次新账号的邮箱验证码由运维在控制台提交(本命令只轮询、提示)。
// `ccfly claude logout` 清掉路由上下文(不删凭证)。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/crypto/nacl/box"

	"github.com/jsdvjx/ccfly/go/internal/control"
	"github.com/jsdvjx/ccfly/go/internal/mesh"
)

// runClaude 分发 `ccfly claude <sub>`。
func runClaude(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(os.Stderr, "Usage: ccfly claude login [--email <e>] [--node <ip>] [--egress <v6>] [--host <cloud>]")
		fmt.Fprintln(os.Stderr, "       ccfly claude logout [--host <cloud>]")
		fmt.Fprintln(os.Stderr, "       ccfly claude status [--host <cloud>]")
		return nil
	}
	switch args[0] {
	case "login":
		return runClaudeLogin(args[1:])
	case "logout":
		return runClaudeLogout(args[1:])
	case "status":
		return runClaudeStatus(args[1:])
	default:
		return fmt.Errorf("unknown subcommand: ccfly claude %s (try: login | logout | status)", args[0])
	}
}

func runClaudeLogin(args []string) error {
	fs := flag.NewFlagSet("claude login", flag.ExitOnError)
	email := fs.String("email", "", "Claude account email (省略 = 云端在你可访问的共享账号里按用量自动选号)")
	node := fs.String("node", "", "out node overlay IP (omit to reuse the account's provisioned egress)")
	egress := fs.String("egress", "", "egress /128 (omit to reuse)")
	host := fs.String("host", "", "cloud host (default: the only enrolled cloud)")
	bg := fs.Bool("_bg", false, "")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ccfly claude login [--email <e>] [--node <ip>] [--egress <v6>] [--host <cloud>]")
		fmt.Fprintln(os.Stderr, "  省略 --email:云端自动选号(共享账号里按 claude 用量 + 分配次数挑一个)。")
		fmt.Fprintln(os.Stderr, "  给 --email:登该(自有)账号,复用其已 provision 的出口;或加 --node+--egress 一次性创建+登录。")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	tgt, err := pickLoginTarget(*host)
	if err != nil {
		return err
	}

	// 内部功能门控:云端 device/config 明确下发 can_use_claude=false 时,本机不暴露 claude 登录
	// (未登录 / 不在准入组)。nil=未知(老云端/未刷新)一律放行。真正鉴权仍在云端(会 403)。
	if !mesh.ClaudeLoginAllowed(tgt.Host) {
		return fmt.Errorf("该账号未获授权使用 Claude 账号功能(内部功能);请联系管理员将你加入准入用户组")
	}

	if *bg {
		return runClaudeLoginBackground(tgt, strings.TrimSpace(*email), strings.TrimSpace(*node), strings.TrimSpace(*egress))
	}
	return forkLoginBackground(tgt, strings.TrimSpace(*email), strings.TrimSpace(*node), strings.TrimSpace(*egress))
}

func forkLoginBackground(tgt mesh.LoginTarget, email, node, egress string) error {
	if s, _ := mesh.LoadLoginStatus(); s != nil && !s.Terminal() && s.PID > 0 {
		if processRunning(s.PID) {
			fmt.Fprintf(os.Stderr, "ccfly: 已有后台登录进程在运行 (PID %d)，用 `ccfly claude status` 查看进度。\n", s.PID)
			return nil
		}
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find self executable: %w", err)
	}
	cliArgs := []string{"claude", "login", "--_bg", "--host", tgt.Host}
	if email != "" {
		cliArgs = append(cliArgs, "--email", email)
	}
	if node != "" {
		cliArgs = append(cliArgs, "--node", node)
	}
	if egress != "" {
		cliArgs = append(cliArgs, "--egress", egress)
	}

	home, _ := os.UserHomeDir()
	logPath := filepath.Join(home, ".ccfly", "claude-login.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("cannot create log file: %w", err)
	}

	cmd := exec.Command(self, cliArgs...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setSysProcDetach(cmd)
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("failed to start background login: %w", err)
	}
	logFile.Close()

	_ = mesh.SaveLoginStatus(mesh.LoginStatus{
		Phase:     mesh.LoginPhaseInit,
		Host:      tgt.Host,
		Email:     email,
		PID:       cmd.Process.Pid,
		StartedAt: time.Now().Unix(),
	})

	fmt.Fprintf(os.Stderr, "ccfly: 登录任务已在后台启动 (PID %d)\n", cmd.Process.Pid)
	fmt.Fprintf(os.Stderr, "  用 `ccfly claude status` 查看进度;日志: %s\n", logPath)
	return nil
}

func runClaudeLoginBackground(tgt mesh.LoginTarget, email, node, egress string) error {
	preserveStartedAt := func() int64 {
		if prev, _ := mesh.LoadLoginStatus(); prev != nil && prev.StartedAt > 0 {
			return prev.StartedAt
		}
		return time.Now().Unix()
	}
	startedAt := preserveStartedAt()

	updateStatus := func(phase, cloudStatus, errMsg, jobID, acctEmail, egressV6 string) {
		_ = mesh.SaveLoginStatus(mesh.LoginStatus{
			Phase:       phase,
			JobID:       jobID,
			Email:       acctEmail,
			EgressV6:    egressV6,
			Host:        tgt.Host,
			PID:         os.Getpid(),
			Error:       errMsg,
			StartedAt:   startedAt,
			CloudStatus: cloudStatus,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	updateStatus(mesh.LoginPhaseCheckChannel, "", "", "", email, "")
	if err := waitForChannel(ctx, tgt, email, updateStatus); err != nil {
		updateStatus(mesh.LoginPhaseFailed, "", err.Error(), "", email, "")
		return err
	}

	updateStatus(mesh.LoginPhaseStarting, "", "", "", email, "")
	var started loginStartResp
	startBackoff := 5 * time.Second
	for {
		var startErr error
		started, startErr = loginStart(ctx, tgt, email, node, egress)
		if startErr == nil {
			break
		}
		if strings.Contains(startErr.Error(), "HTTP 409") {
			log.Printf("ccfly-bg: login start 409, retrying in %s", startBackoff)
			updateStatus(mesh.LoginPhaseWaitChannel, "", "通道被占用,等待重试", "", email, "")
			select {
			case <-ctx.Done():
				updateStatus(mesh.LoginPhaseFailed, "", ctx.Err().Error(), "", email, "")
				return ctx.Err()
			case <-time.After(startBackoff):
			}
			if startBackoff < 30*time.Second {
				startBackoff *= 2
			}
			continue
		}
		updateStatus(mesh.LoginPhaseFailed, "", startErr.Error(), "", email, "")
		return startErr
	}

	acct := started.AccountEmail
	if acct == "" {
		acct = email
	}
	log.Printf("ccfly-bg: login job %s started (account %s, egress %s)", started.JobID, acct, started.EgressV6)
	updateStatus(mesh.LoginPhasePolling, "pending", "", started.JobID, acct, started.EgressV6)

	res, err := loginPoll(ctx, tgt, started.JobID)
	if err != nil {
		updateStatus(mesh.LoginPhaseFailed, "", err.Error(), started.JobID, acct, started.EgressV6)
		return err
	}

	updateStatus(mesh.LoginPhaseDecrypting, "succeeded", "", started.JobID, acct, started.EgressV6)
	plain, err := openSealed(tgt, res.CiphertextB64)
	if err != nil {
		updateStatus(mesh.LoginPhaseFailed, "", fmt.Sprintf("解密凭证失败: %v", err), started.JobID, acct, started.EgressV6)
		return err
	}

	updateStatus(mesh.LoginPhaseWriting, "succeeded", "", started.JobID, acct, started.EgressV6)
	path, err := writeCredentials(plain)
	if err != nil {
		updateStatus(mesh.LoginPhaseFailed, "", err.Error(), started.JobID, acct, started.EgressV6)
		return err
	}
	control.TrustFolder("") // 凭证有了还不够:标记 TUI 首启引导已完成(dir 空=只 onboarding),否则新设备起会话被引导界面挡住

	_ = loginAck(ctx, tgt, started.JobID)

	finalAcct := firstNonEmpty(res.AccountEmail, started.AccountEmail, email)
	v6 := firstNonEmpty(res.EgressV6, started.EgressV6)
	outIP := firstNonEmpty(res.OutNodeIP, started.OutNodeIP, node)

	if finalAcct != "" || v6 != "" {
		_ = mesh.SaveClaudeLoginContext(mesh.ClaudeLoginContext{
			Host: tgt.Host, AccountEmail: finalAcct, EgressV6: v6, OutNodeIP: outIP,
			ProxyURL: res.AccountProxyURL,
		})
	}

	updateStatus(mesh.LoginPhaseSucceeded, "succeeded", "", started.JobID, finalAcct, v6)
	log.Printf("ccfly-bg: login succeeded: %s (egress %s), credentials written to %s", finalAcct, v6, path)
	return nil
}

type channelStatusResp struct {
	Occupied  bool   `json:"occupied"`
	Source    string `json:"source"`
	Status    string `json:"status"`
	ExpiresAt int64  `json:"expires_at"`
}

func checkChannelStatus(ctx context.Context, tgt mesh.LoginTarget, email string) (channelStatusResp, error) {
	var resp channelStatusResp
	q := url.Values{"email": {email}}
	if err := loginDo(ctx, tgt, http.MethodGet, "/api/device/login/channel-status", q, nil, &resp); err != nil {
		return channelStatusResp{}, err
	}
	return resp, nil
}

func waitForChannel(ctx context.Context, tgt mesh.LoginTarget, email string, updateFn func(phase, cloudStatus, errMsg, jobID, email, egress string)) error {
	if email == "" {
		return nil
	}
	backoff := 5 * time.Second
	for {
		status, err := checkChannelStatus(ctx, tgt, email)
		if err != nil {
			log.Printf("ccfly-bg: channel check failed (proceeding): %v", err)
			return nil
		}
		if !status.Occupied {
			return nil
		}
		log.Printf("ccfly-bg: channel occupied by %s (%s), waiting %s", status.Source, status.Status, backoff)
		updateFn(mesh.LoginPhaseWaitChannel, status.Status, fmt.Sprintf("通道被 %s 占用 (%s)", status.Source, status.Status), "", email, "")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func runClaudeStatus(args []string) error {
	s, err := mesh.LoadLoginStatus()
	if err != nil {
		return err
	}
	if s == nil {
		fmt.Fprintln(os.Stderr, "ccfly: 没有进行中的登录任务。")
		return nil
	}

	alive := s.PID > 0 && processRunning(s.PID)

	labels := map[string]string{
		mesh.LoginPhaseInit:         "初始化",
		mesh.LoginPhaseCheckChannel: "检查通道",
		mesh.LoginPhaseWaitChannel:  "等待通道空闲",
		mesh.LoginPhaseStarting:     "发起登录",
		mesh.LoginPhasePolling:      "轮询中",
		mesh.LoginPhaseDecrypting:   "解密凭证",
		mesh.LoginPhaseWriting:      "写入凭证",
		mesh.LoginPhaseSucceeded:    "已完成",
		mesh.LoginPhaseFailed:       "失败",
	}
	label := labels[s.Phase]
	if label == "" {
		label = s.Phase
	}

	fmt.Fprintf(os.Stderr, "状态: %s", label)
	if !alive && !s.Terminal() {
		fmt.Fprint(os.Stderr, " (进程已退出)")
	}
	fmt.Fprintln(os.Stderr)
	if s.Host != "" {
		fmt.Fprintf(os.Stderr, "云端: %s\n", s.Host)
	}
	if s.Email != "" {
		fmt.Fprintf(os.Stderr, "账号: %s\n", s.Email)
	}
	if s.JobID != "" {
		fmt.Fprintf(os.Stderr, "任务: %s\n", s.JobID)
	}
	if s.EgressV6 != "" {
		fmt.Fprintf(os.Stderr, "出口: %s\n", s.EgressV6)
	}
	if s.CloudStatus != "" {
		fmt.Fprintf(os.Stderr, "云端状态: %s%s\n", s.CloudStatus, awaitHint(s.CloudStatus))
	}
	if s.Error != "" {
		fmt.Fprintf(os.Stderr, "错误: %s\n", s.Error)
	}
	if s.UpdatedAt > 0 {
		ago := time.Since(time.Unix(s.UpdatedAt, 0)).Truncate(time.Second)
		fmt.Fprintf(os.Stderr, "更新: %s 前\n", ago)
	}
	return nil
}

// runClaudeLogout 清掉本机的「按账号 /128 路由」上下文(可选 --host 只清某云端;默认清全部)。
// 不动 ~/.claude/.credentials.json —— 那是凭证,登出路由不等于退出 claude。
func runClaudeLogout(args []string) error {
	fs := flag.NewFlagSet("claude logout", flag.ExitOnError)
	host := fs.String("host", "", "只清某云端的登录上下文(默认:清全部)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ccfly claude logout [--host <cloud>]")
		fmt.Fprintln(os.Stderr, "  清除 `ccfly claude login` 落盘的按账号 /128 路由上下文(不删 ~/.claude 凭证)。")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	// 通知云端清掉本设备的「按账号 /128」源路由(best-effort;云端据此重写 sing-box,后续 claude 回退默认出网)。
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, t := range mesh.LoginTargets() {
		if h := strings.TrimSpace(*host); h != "" && t.Host != h {
			continue
		}
		_ = loginRouteClear(ctx, t)
	}
	n, err := mesh.ClearClaudeLoginContext(strings.TrimSpace(*host))
	if err != nil {
		return err
	}
	if n == 0 {
		fmt.Fprintln(os.Stderr, "ccfly: 无登录上下文可清。")
	} else {
		fmt.Fprintf(os.Stderr, "✓ 已清除 %d 份登录上下文(后续 claude 会话回退默认出网)。\n", n)
	}
	return nil
}

// loginRouteClear 通知云端清掉本设备的按账号源路由(对应 cloud handleLoginRouteClear)。
func loginRouteClear(ctx context.Context, t mesh.LoginTarget) error {
	return loginDo(ctx, t, http.MethodPost, "/api/device/login/route/clear", nil, nil, nil)
}

// firstNonEmpty 返回首个非空(trim 后)字符串。
func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// pickLoginTarget 选目标云端:--host 指定,或唯一已入网云端;多个则要求 --host。
func pickLoginTarget(host string) (mesh.LoginTarget, error) {
	ts := mesh.LoginTargets()
	if len(ts) == 0 {
		return mesh.LoginTarget{}, fmt.Errorf("本机未入网任何 ccfly 云端(先 `ccfly connect <cloud>`)")
	}
	if host != "" {
		for _, t := range ts {
			if t.Host == host {
				return t, nil
			}
		}
		return mesh.LoginTarget{}, fmt.Errorf("未找到已入网云端 %q", host)
	}
	if len(ts) > 1 {
		hs := make([]string, len(ts))
		for i, t := range ts {
			hs[i] = t.Host
		}
		return mesh.LoginTarget{}, fmt.Errorf("入网了多个云端,用 --host 指定其一:%s", strings.Join(hs, " "))
	}
	return ts[0], nil
}

func loginBase(t mesh.LoginTarget) string {
	scheme := t.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + t.Host
}

// loginStartResp 对应 cloud handleLoginStart 的返回:job_id + 云端最终选定的账号/出口。
// email 留空时云端自动选号,选中的账号经这些字段回带。
type loginStartResp struct {
	JobID        string `json:"job_id"`
	AccountEmail string `json:"account_email"`
	EgressV6     string `json:"egress_v6"`
	OutNodeIP    string `json:"out_node_ip"`
}

func loginStart(ctx context.Context, t mesh.LoginTarget, email, node, egress string) (loginStartResp, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "out_node_ip": node, "egress_v6": egress})
	var resp loginStartResp
	if err := loginDo(ctx, t, http.MethodPost, "/api/device/login/start", nil, body, &resp); err != nil {
		return loginStartResp{}, loginStartHint(err)
	}
	if resp.JobID == "" {
		return loginStartResp{}, fmt.Errorf("云端未返回 job_id")
	}
	return resp, nil
}

// loginStartHint 给 start 的 HTTP 错误补一句中文解释(403=无可访问账号、503=暂无就绪账号、
// 409=正在登录、401=token 失效),其余原样透传。
func loginStartHint(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "HTTP 403"):
		return fmt.Errorf("%w —— 没有可访问的 Claude 账号(让管理员把账号共享给你,或用 --email 指定自有账号)", err)
	case strings.Contains(msg, "HTTP 503"):
		return fmt.Errorf("%w —— 暂无就绪账号(账号未 provision,或都被限流);稍后再试或换 --email", err)
	case strings.Contains(msg, "HTTP 409"):
		return fmt.Errorf("%w —— 该账号正有一个登录在进行中,请稍后再试", err)
	case strings.Contains(msg, "HTTP 401"):
		return fmt.Errorf("%w —— mesh_token 无效(本机可能已掉线,试 `ccfly connect <cloud>` 重连)", err)
	default:
		return err
	}
}

// loginPollResp 对应 cloud handleLoginPoll 的返回。
type loginPollResp struct {
	Status          string `json:"status"`
	AccountEmail    string `json:"account_email"`
	EgressV6        string `json:"egress_v6"`
	OutNodeIP       string `json:"out_node_ip"`
	AccountProxyURL string `json:"account_proxy_url"` // 该账号 /128 的 byway 登录代理 URL → 灌进 CCFLY_TMUX_PROXY
	CiphertextB64   string `json:"ciphertext_b64"`
	Reason          string `json:"reason"`
}

// loginPoll 每 2s 轮询任务,打印状态变化,直到终态。succeeded 返回密文 + 最终分配结果。
func loginPoll(ctx context.Context, t mesh.LoginTarget, jobID string) (loginPollResp, error) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	last := ""
	for {
		var r loginPollResp
		err := loginDo(ctx, t, http.MethodGet, "/api/device/login/poll", url.Values{"job_id": {jobID}}, nil, &r)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  poll: %v(重试)\n", err) // 瞬时网络错不致命
		} else {
			if r.Status != last {
				fmt.Fprintf(os.Stderr, "  状态: %s%s\n", r.Status, awaitHint(r.Status))
				last = r.Status
			}
			switch r.Status {
			case "succeeded":
				if r.CiphertextB64 == "" {
					return loginPollResp{}, fmt.Errorf("任务成功但无密文(可能已被确认过)")
				}
				return r, nil
			case "failed":
				return loginPollResp{}, fmt.Errorf("登录失败: %s", r.Reason)
			case "canceled":
				return loginPollResp{}, fmt.Errorf("任务被取消")
			case "expired":
				return loginPollResp{}, fmt.Errorf("任务超时(>10min)")
			}
		}
		select {
		case <-ctx.Done():
			return loginPollResp{}, ctx.Err()
		case <-tick.C:
		}
	}
}

func awaitHint(status string) string {
	if status == "awaiting_code" {
		return " — 请到 cc.hn 控制台给该账号提交邮箱验证码 / 登录链接"
	}
	return ""
}

func loginAck(ctx context.Context, t mesh.LoginTarget, jobID string) error {
	return loginDo(ctx, t, http.MethodPost, "/api/device/login/ack", url.Values{"job_id": {jobID}}, nil, nil)
}

// loginDo 发一次带 mesh_token 的请求,2xx 解析到 out(可空);非 2xx 取云端 {error}。
func loginDo(ctx context.Context, t mesh.LoginTarget, method, path string, q url.Values, body []byte, out any) error {
	if q == nil {
		q = url.Values{}
	}
	q.Set("token", t.MeshToken)
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, loginBase(t)+path+"?"+q.Encode(), rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		msg := e.Error
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// openSealed 用本机 WG 私钥打开 NaCl sealed-box(匿名封装)密文。WG 密钥即 Curve25519,可直接用作
// nacl/box 密钥对。返回明文(.credentials.json 字节)。
func openSealed(t mesh.LoginTarget, ctB64 string) ([]byte, error) {
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		return nil, fmt.Errorf("密文 base64 解码失败: %w", err)
	}
	priv, err := base64.StdEncoding.DecodeString(t.PrivateKey)
	if err != nil || len(priv) != 32 {
		return nil, fmt.Errorf("设备 WG 私钥无效")
	}
	pub, err := base64.StdEncoding.DecodeString(t.PublicKey)
	if err != nil || len(pub) != 32 {
		return nil, fmt.Errorf("设备 WG 公钥无效")
	}
	var p, k [32]byte
	copy(p[:], pub)
	copy(k[:], priv)
	plain, ok := box.OpenAnonymous(nil, ct, &p, &k)
	if !ok {
		return nil, fmt.Errorf("sealed-box 验证失败")
	}
	return plain, nil
}

// writeCredentials 把解开的凭证原子写到 ~/.claude/.credentials.json(0600);已有则先备份 .bak。
func writeCredentials(data []byte) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ".credentials.json")
	if old, e := os.ReadFile(path); e == nil {
		_ = os.WriteFile(path+".bak", old, 0o600) // 备份既有凭证
	}
	tmp := path + ".ccfly-tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	// macOS:claude-code 以登录钥匙串(非本文件)为 OAuth 主源,文件会被钥匙串旧凭据遮蔽。放一个一次性
	// 「待 seed」标记,下次起 claude 会话时在解锁的 tmux 上下文把本凭据写进钥匙串(见 control.wrapClaudeCmd)。
	if runtime.GOOS == "darwin" {
		if cdir := filepath.Join(home, ".ccfly"); os.MkdirAll(cdir, 0o755) == nil {
			_ = os.WriteFile(filepath.Join(cdir, "keychain-seed-pending"), []byte("1"), 0o600)
		}
	}
	return path, nil
}
