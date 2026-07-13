// Package mesh implements the device side of ccfly's overlay: `ccfly connect
// <host>/<code>` enrolls the device with a ccfly-cloud control plane and holds a
// WebSocket tunnel (`/mesh`) open.
//
// Increment 1 (this file): X25519 key generation, the `/connect` enrollment
// handshake, local state persistence, and a self-healing /mesh tunnel that keeps
// the device marked online. Increment 2 will frame actual WireGuard packets over
// this tunnel (custom conn.Bind + netstack) so the cloud can reach the device's
// local ccfly control API over the overlay.
package mesh

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
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

	"github.com/coder/websocket"

	"github.com/jsdvjx/ccfly/go/internal/profile"
)

// State is the persisted per-host connection state (~/.ccfly/conn-<host>.json).
type State struct {
	Host           string `json:"host"`
	Scheme         string `json:"scheme"` // http | https (control plane)
	DeviceID       string `json:"device_id"`
	Name           string `json:"name"`
	Owner          string `json:"owner"`
	PrivateKey     string `json:"private_key"` // device WG private key (base64)
	PublicKey      string `json:"public_key"`
	OverlayIP      string `json:"overlay_ip"`
	OverlayCIDR    string `json:"overlay_cidr"`
	CloudPublicKey string `json:"cloud_public_key"`
	CloudOverlayIP string `json:"cloud_overlay_ip"`
	MeshURL        string `json:"mesh_url"`
	MeshToken      string `json:"mesh_token"`
	KeepaliveSec   int    `json:"keepalive_sec"`
	// 云端下发的出网代理策略(可选):设了 ProxyPort 即「该云端在 overlay 上跑着代理」,设备据此
	// 零配置自动起 127.0.0.1:<port> → cloud_overlay_ip:<port> 转发 + 给会话注入代理环境(见 applyMeshProxy)。
	ProxyPort   int    `json:"proxy_port,omitempty"`
	ProxyScheme string `json:"proxy_scheme,omitempty"`
	// 出口若做 MITM,设备需信任其 CA。云端把所有出口的 CA 合成一个 PEM bundle 下发,设备落盘到
	// ~/.ccfly/proxy-ca.pem 并给会话注入 NODE_EXTRA_CA_CERTS(见 applyProxyCA)。
	ProxyCA string `json:"proxy_ca,omitempty"`
	// 云端下发的「内部功能准入」标志(claude 账号 / 代理 / 组网)。三态指针:nil=未知(老云端/老
	// State 没这字段)→ 一律按「允许」处理,绝不误伤未开闸的部署;显式 false 才在设备侧「不暴露」
	// 相关命令(见 cmd/ccfly 的 claude 门控)。真正鉴权仍在云端(端点 403),这里只是本地 UX。
	CanUseClaude *bool `json:"can_use_claude,omitempty"`
	CanUseProxy  *bool `json:"can_use_proxy,omitempty"`
	CanUseMesh   *bool `json:"can_use_mesh,omitempty"`
	// 云端下发的 SNI 出口段(可选):设备装本地 DNS 拦截器 + :443 透传,把 AI 域流量经 overlay 送到
	// 账号出口 byway-sni(真证书,无 HTTP 代理/无 MITM)。nil/无段 = 不装/卸载(见 sni.go applySNI)。
	SNI *SNIConfig `json:"sni,omitempty"`
}

// connectResp mirrors ccfly-cloud's POST /connect response.
type connectResp struct {
	DeviceID       string `json:"device_id"`
	Name           string `json:"name"`
	Owner          string `json:"owner"`
	OverlayIP      string `json:"overlay_ip"`
	OverlayCIDR    string `json:"overlay_cidr"`
	CloudPublicKey string `json:"cloud_public_key"`
	CloudOverlayIP string `json:"cloud_overlay_ip"`
	MeshURL        string `json:"mesh_url"`
	MeshToken      string `json:"mesh_token"`
	KeepaliveSec   int    `json:"keepalive_sec"`
	ProxyPort      int    `json:"proxy_port,omitempty"`
	ProxyScheme    string `json:"proxy_scheme,omitempty"`
	ProxyCA        string `json:"proxy_ca,omitempty"`
}

// Connect 把设备接入 <target> 并持有 mesh 隧道,直到 ctx 取消。target 形态:
//   - "<host>/<code>"(及带 scheme 前缀)→ 走【既有 connect code 流程】(POST /connect)
//   - "<host>"(不含 "/")→ 走【无码配对流程】(/api/pair/start + 轮询);
//     但若本机对该 host 已有保存的设备身份(私钥+device_id+mesh_token),则直接用
//     旧身份重连,不再重新配对——这样 install 出来的 launchd/systemd 服务每次重启
//     都是同一台设备,而不是开机就刷一个新设备。
//
// loopback host 默认 http,其余 https(可被显式 scheme 覆盖),与下方 scheme 逻辑一致。
func Connect(ctx context.Context, target, version string) error {
	if !profile.Current().MeshJoin {
		return errors.New("受限档(profile=restricted):组网接入(mesh)已禁用")
	}
	if hasCode(target) {
		return connectWithCode(ctx, target, version)
	}
	return connectNoCode(ctx, target, version)
}

// ── code 流程(既有,逻辑保持不变)──

// connectWithCode 用连接码把设备登记到 <host>/<code> 并持有隧道。
func connectWithCode(ctx context.Context, target, version string) error {
	scheme, host, code, err := parseTarget(target)
	if err != nil {
		return err
	}

	// 复用本 host 的密钥身份(若之前连过);否则现生成一对。
	st, _ := loadState(host)
	if st == nil || st.PrivateKey == "" {
		priv, pub, err := newKeypair()
		if err != nil {
			return fmt.Errorf("generate key: %w", err)
		}
		st = &State{PrivateKey: priv, PublicKey: pub}
	}
	st.Host, st.Scheme = host, scheme

	resp, err := enroll(ctx, scheme, host, code, st.PublicKey, version)
	if err != nil {
		return err
	}
	return applyEnrollAndHold(ctx, st, resp)
}

// applyEnroll 把云端返回的登记结果(connectResp)写进 State 并落盘。code / 无码两条路径
// 共享它——区别只在前面怎么拿到这份 connectResp(POST /connect 还是 pair 轮询的
// approved)、以及之后是否持有隧道。
func applyEnroll(st *State, resp *connectResp) error {
	st.DeviceID = resp.DeviceID
	st.Name = resp.Name
	st.Owner = resp.Owner
	st.OverlayIP = resp.OverlayIP
	st.OverlayCIDR = resp.OverlayCIDR
	st.CloudPublicKey = resp.CloudPublicKey
	st.CloudOverlayIP = resp.CloudOverlayIP
	st.MeshURL = resp.MeshURL
	st.MeshToken = resp.MeshToken
	st.KeepaliveSec = resp.KeepaliveSec
	st.ProxyPort = resp.ProxyPort // 云端下发的代理策略,持久化:CLI(ccfly new/a)与下次重连都据此自动配
	st.ProxyScheme = resp.ProxyScheme
	st.ProxyCA = resp.ProxyCA // 出口 MITM CA bundle,落盘+注入会话信任(见 applyProxyCA)
	if err := saveState(st); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	log.Printf("ccfly: enrolled as %q (device %s) overlay %s on %s", st.Name, st.DeviceID, st.OverlayIP, st.Host)
	return nil
}

// applyEnrollAndHold = applyEnroll + 打印 + 持有隧道。code 流程的尾段。
func applyEnrollAndHold(ctx context.Context, st *State, resp *connectResp) error {
	if err := applyEnroll(st, resp); err != nil {
		return err
	}
	fmt.Printf("✓ connected to %s — device %q, overlay IP %s\n  holding mesh tunnel (Ctrl-C to stop)\n", st.Host, st.Name, st.OverlayIP)
	return runTunnel(ctx, st)
}

// ── enrollment ──

func enroll(ctx context.Context, scheme, host, code, pubkey, version string) (*connectResp, error) {
	hostname, _ := os.Hostname()
	body, _ := json.Marshal(map[string]string{
		"code":       code,
		"public_key": pubkey,
		"hostname":   hostname,
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
		"version":    version, // 上报客户端版本,云端落到 Device 记录,便于在 web 看「设备是否升到新版」
	})
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodPost, scheme+"://"+host+"/connect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", host, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		return nil, fmt.Errorf("enrollment rejected (%d): %s", resp.StatusCode, e.Error)
	}
	var cr connectResp
	if err := json.Unmarshal(data, &cr); err != nil {
		return nil, fmt.Errorf("bad enrollment response: %w", err)
	}
	if cr.MeshURL == "" || cr.MeshToken == "" {
		return nil, errors.New("enrollment response missing mesh url/token")
	}
	return &cr, nil
}

// ── 无码配对流程(device authorization 风格)──

// pairStartResp 对应云端 POST /api/pair/start 的返回。
type pairStartResp struct {
	PairID       string `json:"pairId"`
	LinkURL      string `json:"linkUrl"`
	PollToken    string `json:"pollToken"`
	ExpiresInSec int    `json:"expiresInSec"`
}

// pairPollResp 对应云端 GET /api/pair/poll 的返回。status=pending|approved|denied|
// expired;approved 时还内联整份登记结果(connectResp 的超集字段,至少含
// overlay_ip / mesh_url / mesh_token / device_id),供设备直接复用 code 流程的落盘逻辑。
type pairPollResp struct {
	Status string `json:"status"`
	connectResp
}

const (
	pairPollInterval = 3 * time.Second  // 轮询间隔,与文案「每 3s」一致
	pairLocalTimeout = 10 * time.Minute // 本地兜底超时(云端 pair 记录同为 10min)
)

// Pair 只做无码配对并把设备身份落盘,然后返回(不持有隧道)。供 `ccfly install <host>`
// 在安装服务前先交互式配对一次用——配对成功后安装的 launchd/systemd 服务跑
// `connect <host>`,凭这份已保存身份重连,不会开机就重新配对。若本机对该 host 已有完整
// 身份则直接跳过配对(幂等)。
func Pair(ctx context.Context, target, version string) error {
	if !profile.Current().MeshJoin {
		return errors.New("受限档(profile=restricted):组网接入(mesh)已禁用")
	}
	scheme, host := parseHost(target)
	st, err := ensurePaired(ctx, scheme, host, version)
	if err != nil {
		return err
	}
	fmt.Printf("✓ 已绑定 — device %q, overlay IP %s\n", st.Name, st.OverlayIP)
	return nil
}

// connectNoCode 走无码配对:若本机对该 host 已有完整设备身份则直接重连(install 出来
// 的服务每次重启走这条,不再配对);否则发起 pair/start、打印并尝试打开授权链接、轮询
// 直到 approved/denied/expired 或本地超时。拿到身份后持有隧道。
func connectNoCode(ctx context.Context, target, version string) error {
	scheme, host := parseHost(target)
	st, err := ensurePaired(ctx, scheme, host, version)
	if err != nil {
		return err
	}
	fmt.Printf("✓ 已接入 — device %q, overlay IP %s\n  holding mesh tunnel (Ctrl-C to stop)\n", st.Name, st.OverlayIP)
	return runTunnel(ctx, st)
}

// ensurePaired 返回该 host 一份可用的、已落盘的设备身份:已有完整身份就直接复用(不配
// 对),否则发起配对(打印+尝试打开链接、轮询到 approved)并落盘。两条调用方(Pair /
// connectNoCode)共享它,差别只在拿到身份后是否持有隧道。
//
// 「完整身份」判定与 runTunnel 所需字段一致:私钥 + device_id + mesh_url + mesh_token。
func ensurePaired(ctx context.Context, scheme, host, version string) (*State, error) {
	if st, _ := loadState(host); st != nil && st.PrivateKey != "" &&
		st.DeviceID != "" && st.MeshURL != "" && st.MeshToken != "" {
		st.Host, st.Scheme = host, scheme
		log.Printf("ccfly: 复用已保存身份(device %s)对接 %s,跳过配对", st.DeviceID, host)
		return st, nil
	}

	// 没有可用身份 → 走配对。复用既有密钥(若有残留私钥)或现生成一对。
	st, _ := loadState(host)
	if st == nil || st.PrivateKey == "" {
		priv, pub, err := newKeypair()
		if err != nil {
			return nil, fmt.Errorf("generate key: %w", err)
		}
		st = &State{PrivateKey: priv, PublicKey: pub}
	}
	st.Host, st.Scheme = host, scheme

	start, err := pairStart(ctx, scheme, host, st.PublicKey, version)
	if err != nil {
		return nil, err
	}

	// 显眼地打印授权链接 + 尝试自动打开浏览器(headless / 无桌面时静默忽略错误)。
	fmt.Printf("\n  在浏览器里打开以下链接,登录后批准绑定本设备:\n\n      %s\n\n", start.LinkURL)
	openBrowser(start.LinkURL)
	mins := start.ExpiresInSec / 60
	if mins <= 0 {
		mins = 10
	}
	fmt.Printf("  等待网页端批准…(链接 %d 分钟内有效,Ctrl-C 取消)\n", mins)

	resp, err := pairPoll(ctx, scheme, host, start.PairID, start.PollToken)
	if err != nil {
		return nil, err
	}
	// 复用 code 流程的尾段:把 connectResp 落进 State 并保存(但不持有隧道)。
	if err := applyEnroll(st, resp); err != nil {
		return nil, err
	}
	return st, nil
}

// pairStart 发起 POST /api/pair/start(无 session 鉴权),上报本机指纹,拿到配对 id +
// 链接 + 轮询令牌。请求体字段与云端 handler 约定一致。
func pairStart(ctx context.Context, scheme, host, pubkey, version string) (*pairStartResp, error) {
	hostname, _ := os.Hostname()
	body, _ := json.Marshal(map[string]string{
		"pubkey":     pubkey,
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
		"version":    version,
		"hostname":   hostname,
		"machine_id": machineID(), // 稳定机器指纹:云端据此去重,同机重配对复用同一设备
	})
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodPost, scheme+"://"+host+"/api/pair/start", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发起配对失败(%s): %w", host, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("发起配对被拒(%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var ps pairStartResp
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("配对返回无法解析: %w", err)
	}
	if ps.PairID == "" || ps.LinkURL == "" || ps.PollToken == "" {
		return nil, errors.New("配对返回缺少 pairId/linkUrl/pollToken")
	}
	return &ps, nil
}

// pairPoll 每 ~3s 轮询 GET /api/pair/poll(凭 pollToken 鉴权),直到状态非 pending 或
// 本地超时。approved → 返回内联的登记结果;denied/expired → 明确报错(由调用方非零退出);
// 本地超时同样报错。403/404 等异常状态码直接报错终止。
//
// 安全:pollToken 是 secret,改走 `Authorization: Bearer` 请求头而非 URL query,
// 避免出现在浏览器历史、访问日志、Referer 里(云端兼容旧 query 形式,但新客户端走头)。
func pairPoll(ctx context.Context, scheme, host, pairID, pollToken string) (*connectResp, error) {
	deadline := time.Now().Add(pairLocalTimeout)
	pollURL := scheme + "://" + host + "/api/pair/poll?id=" + url.QueryEscape(pairID)
	t := time.NewTicker(pairPollInterval)
	defer t.Stop()
	for {
		pr, err := pairPollOnce(ctx, pollURL, pollToken)
		if err != nil {
			return nil, err
		}
		switch pr.Status {
		case "pending":
			// 继续轮询
		case "approved":
			if pr.MeshURL == "" || pr.MeshToken == "" {
				return nil, errors.New("配对已批准但返回缺少 mesh url/token")
			}
			fmt.Printf("✓ 已批准,正在接入…\n")
			cr := pr.connectResp
			return &cr, nil
		case "denied":
			return nil, errors.New("配对被拒绝(网页端点了「拒绝」)")
		case "expired":
			return nil, errors.New("配对已过期(超时未批准),请重新运行命令")
		default:
			return nil, fmt.Errorf("配对返回了未知状态: %q", pr.Status)
		}
		if time.Now().After(deadline) {
			return nil, errors.New("配对超时(本地等待已超过 10 分钟未获批准)")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// pairPollOnce 做一次轮询请求并解析。非 200 一律视为致命(403=令牌错、404=未知 id),
// 直接报错,避免在错误的链接上空转十分钟。pollToken 走 Authorization 头(不进 URL)。
func pairPollOnce(ctx context.Context, pollURL, pollToken string) (*pairPollResp, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, pollURL, nil)
	req.Header.Set("Authorization", "Bearer "+pollToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("轮询配对失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("轮询配对被拒(%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var pr pairPollResp
	if err := json.Unmarshal(data, &pr); err != nil {
		return nil, fmt.Errorf("轮询返回无法解析: %w", err)
	}
	return &pr, nil
}

// openBrowser 尽力打开 url(darwin:open,linux:xdg-open)。任何错误(headless、无桌面、
// 命令不存在)都静默忽略——链接已显眼打印,用户可手动复制。
func openBrowser(rawURL string) {
	var name string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "linux":
		name = "xdg-open"
	case "windows":
		name = "rundll32"
		bin, err := exec.LookPath(name)
		if err != nil {
			return
		}
		_ = exec.Command(bin, "url.dll,FileProtocolHandler", rawURL).Start()
		return
	default:
		return
	}
	bin, err := exec.LookPath(name)
	if err != nil {
		return
	}
	_ = exec.Command(bin, rawURL).Start()
}

// ── tunnel: dial /mesh, keepalive, self-heal ──

// applyMeshProxy 据云端下发的代理策略(st.ProxyPort)零配置自动配好两件事(幂等,只在首次生效):
//  1. 起本地转发 127.0.0.1:<port> → <cloud_overlay_ip>:<port>(若用户已用 --overlay-forward 配了同
//     localPort 则不重复加);
//  2. 给 ccfly 创建的 tmux 会话设 CCFLY_TMUX_PROXY=<scheme>://127.0.0.1:<port>(+ 默认局域网 bypass),
//     让会话里 claude 及子进程出网走代理、本机/局域网直连。
//
// 用户若已手动设了 CCFLY_TMUX_PROXY(服务 env / shell),尊重其值不覆盖。ProxyPort==0(云端未下发)→ no-op。
func applyMeshProxy(st *State) {
	if !profile.Current().MeshProxy {
		return
	}
	if st == nil || st.ProxyPort <= 0 || st.CloudOverlayIP == "" {
		return
	}
	addAutoForward(st.ProxyPort, st.CloudOverlayIP, st.ProxyPort)
	if os.Getenv("CCFLY_TMUX_PROXY") == "" {
		scheme := st.ProxyScheme
		if scheme == "" {
			scheme = "http"
		}
		_ = os.Setenv("CCFLY_TMUX_PROXY", fmt.Sprintf("%s://127.0.0.1:%d", scheme, st.ProxyPort))
	}
	applyProxyCA(st.ProxyCA)
}

// applyMeshSNI 据云端下发的 sni 段驱动 SNI arm 生命周期(有段→装,无段→卸;幂等)。
// 与 applyMeshProxy 并列,由 runTunnel 在 refreshConfig 后调用。gate 同 MeshProxy 能力档。
func applyMeshSNI(st *State) {
	if !profile.Current().MeshProxy {
		applySNI(nil) // 能力档不允许 → 确保卸载
		return
	}
	if st == nil {
		applySNI(nil)
		return
	}
	applySNI(st.SNI)
}

// applyProxyCA 把云端下发的「出口 MITM CA bundle」落盘到 ~/.ccfly/proxy-ca.pem,并把
// CCFLY_TMUX_PROXY_CA 指向它 —— proxyenv 据此给会话注入 NODE_EXTRA_CA_CERTS,使会话里的
// claude(Node)信任 MITM 出口的证书,否则经出口的 HTTPS 会证书校验失败。
// 空 CA / 用户已显式设 CCFLY_TMUX_PROXY_CA → no-op(尊重用户、零行为变化)。
func applyProxyCA(caPEM string) {
	if caPEM == "" || os.Getenv("CCFLY_TMUX_PROXY_CA") != "" {
		return
	}
	dir, err := stateDir()
	if err != nil {
		return
	}
	path := filepath.Join(dir, "proxy-ca.pem")
	if err := os.WriteFile(path, []byte(caPEM), 0o644); err != nil {
		return
	}
	_ = os.Setenv("CCFLY_TMUX_PROXY_CA", path)
}

// refreshConfig 在连接/重连前向云端拉一次「动态配置」(GET /api/device/config,凭 mesh_token),
// 更新并落盘 State 的 cloud_public_key / cloud_overlay_ip / proxy 策略。
// 关键价值:**保存身份重连的设备不重新 enroll**,云端后来才下发的 proxy_port、或轮换后的
// cloud_public_key,本来永远拿不到(只能手动改 State 或重新配对)。本函数让设备每次连接主动刷新,
// 真正做到「策略默认就生效、无需显式操作」,并顺带自愈 cloud 公钥漂移导致的数据面不通。
// 云端老版本(无此端点)/ 网络失败 → 静默沿用现有 State(优雅降级,绝不阻断连接)。
func refreshConfig(ctx context.Context, st *State) {
	if st.MeshToken == "" || st.Host == "" {
		return
	}
	scheme := st.Scheme
	if scheme == "" {
		scheme = "https"
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, "GET",
		scheme+"://"+st.Host+"/api/device/config?token="+url.QueryEscape(st.MeshToken), nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("ccfly: device config refresh failed: %v", err) // graceful: keep existing State
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("ccfly: device config refresh: HTTP %d", resp.StatusCode)
		return
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var c struct {
		CloudPublicKey string     `json:"cloud_public_key"`
		CloudOverlayIP string     `json:"cloud_overlay_ip"`
		ProxyPort      int        `json:"proxy_port"`
		ProxyScheme    string     `json:"proxy_scheme"`
		ProxyCA        string     `json:"proxy_ca"`
		CanUseClaude   *bool      `json:"can_use_claude"`
		CanUseProxy    *bool      `json:"can_use_proxy"`
		CanUseMesh     *bool      `json:"can_use_mesh"`
		SNI            *SNIConfig `json:"sni"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		log.Printf("ccfly: device config refresh: bad JSON: %v", err)
		return
	}
	changed := false
	// 内部功能准入标志:云端下发才更新(nil=老云端没发,保持既有值不动)。
	if c.CanUseClaude != nil && !sameBool(st.CanUseClaude, c.CanUseClaude) {
		st.CanUseClaude, changed = c.CanUseClaude, true
	}
	if c.CanUseProxy != nil && !sameBool(st.CanUseProxy, c.CanUseProxy) {
		st.CanUseProxy, changed = c.CanUseProxy, true
	}
	if c.CanUseMesh != nil && !sameBool(st.CanUseMesh, c.CanUseMesh) {
		st.CanUseMesh, changed = c.CanUseMesh, true
	}
	if c.CloudPublicKey != "" && c.CloudPublicKey != st.CloudPublicKey {
		st.CloudPublicKey, changed = c.CloudPublicKey, true
	}
	if c.CloudOverlayIP != "" && c.CloudOverlayIP != st.CloudOverlayIP {
		st.CloudOverlayIP, changed = c.CloudOverlayIP, true
	}
	if c.ProxyPort != st.ProxyPort {
		st.ProxyPort, changed = c.ProxyPort, true
	}
	if c.ProxyScheme != st.ProxyScheme {
		st.ProxyScheme, changed = c.ProxyScheme, true
	}
	if c.ProxyCA != st.ProxyCA {
		st.ProxyCA, changed = c.ProxyCA, true
	}
	if !sameSNI(st.SNI, c.SNI) {
		st.SNI, changed = c.SNI, true
	}
	if changed {
		_ = saveState(st)
		log.Printf("ccfly: refreshed device config (proxy_port=%d cloud_pub=%.8s…)", st.ProxyPort, st.CloudPublicKey)
	}
}

func runTunnel(ctx context.Context, st *State) error {
	go runSyncer(ctx, st)   // 后台把本地会话(摘要 + 全文)同步到云端归档;走公网控制面,与隧道状态无关
	defer applyMeshSNI(nil) // 进程退出时卸载 SNI arm(恢复 resolv.conf,不留指向死本地 DNS 的配置)
	backoff := time.Second
	for ctx.Err() == nil {
		// 每次(重)连接前拉云端动态配置并重应用策略:登录后云端新绑定的 sni 账号 / 后加的 proxy 无需
		// 重启进程,下次连接即生效(refreshConfig 优雅降级,与 dialOnce 顺序执行、无并发写 st)。
		refreshConfig(ctx, st)
		applyMeshProxy(st)
		applyMeshSNI(st)
		err := dialOnce(ctx, st)
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("ccfly: mesh disconnected: %v — retrying in %s", err, backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	return nil
}

func dialOnce(ctx context.Context, st *State) error {
	u := st.MeshURL + "?token=" + url.QueryEscape(st.MeshToken)
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	c, _, err := websocket.Dial(dctx, u, nil)
	cancel()
	if err != nil {
		return err
	}
	defer c.CloseNow()
	log.Printf("ccfly: mesh up (overlay %s via %s)", st.OverlayIP, st.Host)

	keepalive := time.Duration(st.KeepaliveSec) * time.Second
	if keepalive <= 0 {
		keepalive = 25 * time.Second
	}
	go func() {
		t := time.NewTicker(keepalive)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := c.Ping(pctx)
				cancel()
				if err != nil {
					// Dead/half-open conn (typically after the host sleeps): force
					// the read side to error so dialOnce returns and the outer
					// runTunnel loop reconnects promptly on wake. Without this,
					// c.Read can block on the half-open TCP indefinitely.
					c.CloseNow()
					return
				}
			}
		}
	}()

	// Increment 2: bring up the userspace WireGuard device whose transport IS
	// this WebSocket. If it fails to come up we fall back to merely draining
	// inbound frames so the tunnel (and online status) is unaffected, then let
	// the outer loop reconnect.
	sess, err := bringUpWG(ctx, st, c)
	if err != nil {
		log.Printf("ccfly: wireguard datapath unavailable (tunnel still up): %v", err)
		for {
			if _, _, rerr := c.Read(ctx); rerr != nil {
				return rerr
			}
		}
	}
	// pump owns the WS read side; it blocks until the conn drops (or ctx ends).
	// Tear the device down before returning so the outer loop can rebuild a
	// fresh device on the next dial.
	defer func() {
		sess.bind.detach(c)
		sess.close()
	}()
	return sess.bind.pump(ctx)
}

// ── target parsing ──

// hasCode 判定 target 是否走【既有 code 流程】:剥掉可选的 "scheme://" 前缀、再去掉
// 首尾多余的 "/" 后,只要 host 之后还跟着非空路径段(即含真正的 "/code"),就是 code
// 流程;否则(纯 host,如 "cc.hn")走无码配对。注意 "host/"(仅尾斜杠)算纯 host。
func hasCode(t string) bool {
	if i := strings.Index(t, "://"); i >= 0 {
		t = t[i+3:]
	}
	slash := strings.Index(t, "/")
	if slash < 0 {
		return false
	}
	return strings.Trim(t[slash+1:], "/") != ""
}

// parseHost 解析纯 host 形态的 target(无码配对用),返回 scheme + host。scheme 逻辑与
// parseTarget 完全一致:显式 "scheme://" 优先,否则 loopback→http、其余→https。尾随的
// "/" 一并剥除,容忍用户粘贴成 "cc.hn/"。
func parseHost(t string) (scheme, host string) {
	explicit := false
	scheme = "https"
	if i := strings.Index(t, "://"); i >= 0 {
		scheme = t[:i]
		t = t[i+3:]
		explicit = true
	}
	host = strings.Trim(t, "/")
	if !explicit && isLoopback(host) {
		scheme = "http"
	}
	return scheme, host
}

func parseTarget(t string) (scheme, host, code string, err error) {
	explicit := false
	scheme = "https"
	if i := strings.Index(t, "://"); i >= 0 {
		scheme = t[:i]
		t = t[i+3:]
		explicit = true
	}
	slash := strings.Index(t, "/")
	if slash < 0 {
		return "", "", "", errors.New(`expected "<host>/<code>" (e.g. ccfly connect example.com/Ab12Cd34Ef)`)
	}
	host = t[:slash]
	code = strings.Trim(t[slash+1:], "/")
	if host == "" || code == "" {
		return "", "", "", errors.New(`expected "<host>/<code>"`)
	}
	if !explicit && isLoopback(host) {
		scheme = "http"
	}
	return scheme, host, code, nil
}

func isLoopback(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h, "::") {
		h = h[:i] // strip :port
	}
	return h == "localhost" || strings.HasPrefix(h, "127.") || h == "[::1]" || h == "::1"
}

// ── X25519 keys (WireGuard-compatible, base64-std) ──

func newKeypair() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()),
		base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// EnsureTmuxProxyEnv 给 CLI(ccfly new / attach,独立进程、不入网)用:把会话出网代理
// 设进本进程环境(若用户未已设),这样 CLI 创建的 tmux 会话和服务进程一样默认带好代理 + 局域网
// bypass。优先级:
//  1. `ccfly claude login` 落盘的「按账号 /128 路由」上下文(~/.ccfly/claude-login.json)——
//     若它带了可用的按账号代理 URL,本机后续 claude 会话即从该账号的 /128 出网(见 claudeLoginProxyURL)。
//  2. 否则回退到云端下发并持久化的设备级 overlay 代理策略(State.ProxyPort)。
//
// 二者都没有 → no-op(零行为变化)。用户已显式设 CCFLY_TMUX_PROXY(shell/plist)→ 一律尊重不覆盖。
func EnsureTmuxProxyEnv() {
	if !profile.Current().MeshProxy {
		return // 非 full 档:不向 CLI 会话注入 mesh 代理 env(实例用用户自带 env)
	}
	if os.Getenv("CCFLY_TMUX_PROXY") != "" {
		return // 用户已显式设(shell/plist)→ 尊重,不覆盖
	}
	// 1) 设备级 overlay 代理优先(127.0.0.1:<ProxyPort> → 云端 sing-box):按账号出口路由
	//    已由云端按「设备源 IP → 账号 outbound」处理,该链路才是设备的正道。
	// 2) 账号直连 URL(byway 登录代理 158.x:8443,凭证内嵌)只作最后回退:出口按**来源 IP**
	//    放行(只认云端节点),设备家宽源发 CONNECT 会被 56ms 拒 400 —— 2026-07-03 Windows
	//    「API Error: 400 Bad Request」实锤根因。切勿再把它放在 overlay 之前。
	if applyOverlayProxyEnv() {
		return
	}
	applyClaudeLoginProxy()
}

// applyOverlayProxyEnv 扫 ~/.ccfly/conn-*.json,据云端下发并持久化的设备级代理策略(ProxyPort)
// 把 CCFLY_TMUX_PROXY 设进本进程环境。返回是否设上了(无任何带 ProxyPort 的状态 → false)。
func applyOverlayProxyEnv() bool {
	dir, err := stateDir()
	if err != nil {
		return false
	}
	files, _ := filepath.Glob(filepath.Join(dir, "conn-*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var st State
		if json.Unmarshal(data, &st) != nil || st.ProxyPort <= 0 {
			continue
		}
		scheme := st.ProxyScheme
		if scheme == "" {
			scheme = "http"
		}
		_ = os.Setenv("CCFLY_TMUX_PROXY", fmt.Sprintf("%s://127.0.0.1:%d", scheme, st.ProxyPort))
		applyProxyCA(st.ProxyCA) // 同步落盘 CA + 设 CCFLY_TMUX_PROXY_CA,供 proxyenv 注入会话信任
		return true
	}
	return false
}

// applyClaudeLoginProxy 据 `ccfly claude login` 落盘的登录上下文,把「该账号 /128 对应的代理」
// 设进 CCFLY_TMUX_PROXY,使本机后续 claude 会话从该账号的 /128 出网。返回是否设上了。
//
// 安全回退:仅当上下文里**已有可用的按账号代理 URL**(ProxyURL,经 claudeLoginProxyURL 解析)时
// 才接管;否则返回 false,让调用方回退到设备级 overlay 代理 —— 既不破坏现有出网,也不会因「拿不到
// 路由参数」而把会话憋成无代理直连。注:当前云端的设备侧端点(login/poll、device/config)并不下发
// byway 登录代理的 endpoint+secret,故 ProxyURL 通常为空、本函数为 no-op;待云端补下发后即自动生效
// (详见 claudeLoginProxyURL 与 SaveClaudeLoginContext 的注释)。
func applyClaudeLoginProxy() bool {
	ctxs := LoadClaudeLoginContexts()
	for _, c := range ctxs {
		proxy := claudeLoginProxyURL(c)
		if proxy == "" {
			continue // 这条上下文没有可用代理 URL → 跳过(由调用方回退 overlay 代理)
		}
		_ = os.Setenv("CCFLY_TMUX_PROXY", proxy)
		applyProxyCA(c.ProxyCA) // 出口若 MITM:同步落盘 CA + 设信任(登录代理是 blind-tunnel,通常无需,但兼容)
		return true
	}
	return false
}

// LoginTarget 是 `ccfly claude login` 需要的:某个已入网云端的控制面地址 + mesh_token(鉴权登录 API)
// + 设备 WG 密钥对(用私钥解开 worker 封装的 NaCl sealed-box 凭证)。
type LoginTarget struct {
	Host       string
	Scheme     string
	MeshToken  string
	PrivateKey string // 设备 WG 私钥(base64);开 sealed-box 用
	PublicKey  string
}

// LoginTargets 扫 ~/.ccfly/conn-*.json,返回所有已入网云端(有 mesh_token 的)。
func LoginTargets() []LoginTarget {
	dir, err := stateDir()
	if err != nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(dir, "conn-*.json"))
	var out []LoginTarget
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var st State
		if json.Unmarshal(data, &st) != nil || st.MeshToken == "" || st.Host == "" {
			continue
		}
		out = append(out, LoginTarget{
			Host: st.Host, Scheme: st.Scheme, MeshToken: st.MeshToken,
			PrivateKey: st.PrivateKey, PublicKey: st.PublicKey,
		})
	}
	return out
}

// sameBool 比较两个三态布尔指针(nil 视为「未设」)。
func sameBool(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ClaudeLoginAllowed 读某云端持久化的「内部功能准入」标志(can_use_claude),判断本机是否应
// 暴露 `ccfly claude login`。语义:仅当云端**明确**下发 false 时才拒绝;nil(老云端 / 尚未刷新
// device/config)一律放行,绝不误伤未开闸的部署。真正鉴权仍在云端(login/start 返回 403),
// 此处只是本地「不暴露命令」的纵深防御 / 更友好的即时报错。
func ClaudeLoginAllowed(host string) bool {
	st, err := loadState(host)
	if err != nil || st == nil || st.CanUseClaude == nil {
		return true
	}
	return *st.CanUseClaude
}

// ── claude login context: 按账号 /128 路由(~/.ccfly/claude-login.json,按 host 区分)──

// ClaudeLoginContext 是 `ccfly claude login` 成功后落盘的「本机当前登录的 Claude 账号 + 其出口」。
// 用途:让本机后续 claude 会话从**该账号的 /128**(EgressV6,在出口节点 OutNodeIP 上)出网。
//
// ProxyURL 是「直接可用的、把流量绑定到该账号 /128 的出网代理 URL」。它由 byway 的登录代理提供:
// 形如 http://<EgressV6 的 32 位 hex>:<login-secret>@<byway-login-endpoint>(见 ~/byway/login.go 的
// Basic-auth 用户名=/128 hex 机制,与 ~/ccfly-cloud/internal/server/login.go 的 loginProxyURL)。
//
// 关键现状:**设备目前拿不到 byway 登录代理的 endpoint + secret** —— 云端只在 worker-facing 的
// /api/worker/jobs/{id}/params(worker bearer 鉴权)里下发 proxy_url,设备侧的 login/poll 与
// device/config(mesh_token 鉴权)都不带它,且 endpoint(host:port)与 secret 均不可推导。故除非
// 云端额外下发(建议:poll 返回里加 account_proxy_url,或 device/config 下发 login endpoint+secret),
// 否则 ProxyURL 留空。留空时 EnsureTmuxProxyEnv 会安全回退到设备级 overlay 代理(见 applyClaudeLoginProxy)。
type ClaudeLoginContext struct {
	Host         string `json:"host"`                // 登录所经的云端(与 conn-<host>.json 对应)
	AccountEmail string `json:"account_email"`       // 云端最终分配/登录的账号邮箱
	EgressV6     string `json:"egress_v6"`           // 该账号的稳定出口 /128
	OutNodeIP    string `json:"out_node_ip"`         // 该 /128 所在出口节点的 overlay IP
	ProxyURL     string `json:"proxy_url,omitempty"` // 直接可用的按账号 /128 出网代理 URL(拿不到则空,见上)
	ProxyCA      string `json:"proxy_ca,omitempty"`  // 出口若 MITM 需信任的 CA(登录代理 blind-tunnel,通常空)
	UpdatedAt    string `json:"updated_at,omitempty"`
}

// claudeLoginPath 返回某云端的登录上下文文件路径(~/.ccfly/claude-login-<host>.json,按 host 区分)。
func claudeLoginPath(host string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	safe := strings.NewReplacer(":", "_", "/", "_").Replace(host)
	return filepath.Join(dir, "claude-login-"+safe+".json"), nil
}

// SaveClaudeLoginContext 原子落盘某云端的登录上下文(0600)。host 为空则 no-op。
func SaveClaudeLoginContext(c ClaudeLoginContext) error {
	if c.Host == "" {
		return errors.New("claude login context: empty host")
	}
	c.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	p, err := claudeLoginPath(c.Host)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// LoadClaudeLoginContexts 扫 ~/.ccfly/claude-login-*.json,返回所有已落盘的登录上下文。
func LoadClaudeLoginContexts() []ClaudeLoginContext {
	dir, err := stateDir()
	if err != nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(dir, "claude-login-*.json"))
	var out []ClaudeLoginContext
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var c ClaudeLoginContext
		if json.Unmarshal(data, &c) != nil {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ClearClaudeLoginContext 删除某云端的登录上下文(host 为空 → 删全部)。供 `ccfly claude logout` 用。
// 返回删掉的文件数。文件不存在不算错。
func ClearClaudeLoginContext(host string) (int, error) {
	dir, err := stateDir()
	if err != nil {
		return 0, err
	}
	var paths []string
	if host != "" {
		p, err := claudeLoginPath(host)
		if err != nil {
			return 0, err
		}
		paths = []string{p}
	} else {
		paths, _ = filepath.Glob(filepath.Join(dir, "claude-login-*.json"))
	}
	n := 0
	for _, p := range paths {
		if err := os.Remove(p); err == nil {
			n++
		} else if !os.IsNotExist(err) {
			return n, err
		}
	}
	return n, nil
}

// claudeLoginProxyURL 从登录上下文导出「该账号 /128 对应的出网代理 URL」。
// 当前只信任上下文里**显式落盘的、直接可用的** ProxyURL(由云端下发,见 ClaudeLoginContext 注释)。
// 没有就返回空 —— 不在设备侧凭 EgressV6 自行拼 byway URL,因为缺 login endpoint + secret,拼不出可用的;
// 凭空编造只会把会话出网打到一个连不上的代理上。空 → 调用方安全回退设备级 overlay 代理。
func claudeLoginProxyURL(c ClaudeLoginContext) string {
	return strings.TrimSpace(c.ProxyURL)
}

// ── state persistence (~/.ccfly/conn-<host>.json) ──

func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".ccfly")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func statePath(host string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	safe := strings.NewReplacer(":", "_", "/", "_").Replace(host)
	return filepath.Join(dir, "conn-"+safe+".json"), nil
}

func loadState(host string) (*State, error) {
	p, err := statePath(host)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func saveState(st *State) error {
	p, err := statePath(st.Host)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}
