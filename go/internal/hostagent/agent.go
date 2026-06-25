// Package hostagent 实现 ccfly 主机代理(ccfly-hostd)的控制面:一个很小的 HTTP 接口
// (spawn / stop / instances),由 ccfly-hostd 经 overlay expose 桥暴露给 cloud 调用,
// 据此在本 VM 上 `docker run` 每用户的 ccfly 实例容器。它只 shell out 到 `docker`,
// 不含任何 Claude / tmux / control-service 代码。
//
// 鉴权:网络层已由 ccfly-hostd 的 expose 桥(源前缀 100.64.0.1/32)只放行 cloud 网关;
// 本层再叠加一个可选 Bearer 令牌(CCFLY_HOST_AGENT_TOKEN)。经 expose 反代后 RemoteAddr
// 恒为 127.0.0.1(源 IP 已在网络层过滤),故 per-request 硬鉴权靠令牌。
package hostagent

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Config 配置 host-agent 的 HTTP 处理器。
type Config struct {
	Token  string // Bearer 令牌;空 = 不校验(仅靠 overlay 源 IP 白名单兜底)
	Docker string // docker 可执行名;空 = env CCFLY_HOST_DOCKER 或 "docker"
}

// LoadToken 读 spawn API 的 Bearer 令牌:优先 env CCFLY_HOST_AGENT_TOKEN,其次 ~/.ccfly/hostd-token(建议 0600)。
func LoadToken() string {
	if v := strings.TrimSpace(os.Getenv("CCFLY_HOST_AGENT_TOKEN")); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		if b, e := os.ReadFile(filepath.Join(home, ".ccfly", "hostd-token")); e == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

func dockerBin(cfg Config) string {
	if v := strings.TrimSpace(cfg.Docker); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("CCFLY_HOST_DOCKER")); v != "" {
		return v
	}
	return "docker"
}

// Handler 构造 host-agent 的 HTTP 处理器。
func Handler(cfg Config) http.Handler {
	d := &docker{bin: dockerBin(cfg)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("POST /spawn", cfg.auth(func(w http.ResponseWriter, r *http.Request) { handleSpawn(w, r, d) }))
	mux.HandleFunc("POST /stop", cfg.auth(func(w http.ResponseWriter, r *http.Request) { handleStop(w, r, d) }))
	mux.HandleFunc("GET /instances", cfg.auth(func(w http.ResponseWriter, r *http.Request) { handleList(w, r, d) }))
	return mux
}

// auth 是 Bearer 令牌中间件(令牌为空则放行 —— 仅靠 overlay 源 IP 白名单兜底)。
func (cfg Config) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.Token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(cfg.Token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		h(w, r)
	}
}

// spawnReq 是 cloud → host-agent 的起容器请求。
type spawnReq struct {
	DeviceID      string            `json:"device_id"`      // 实例 device id(容器命名用 ccfly-<id>)
	ConnectTarget string            `json:"connect_target"` // cc.hn/<连接码>,注入 CCFLY_CONNECT_TARGET
	Image         string            `json:"image"`          // 实例镜像;空 = ccfly:instance
	Env           map[string]string `json:"env"`            // 用户凭证 env(白名单过滤后注入容器)
	Name          string            `json:"name"`           // 容器名;空 = ccfly-<device_id>
}

// envAllowed 白名单:只把 ANTHROPIC_*/AWS_*(Bedrock)与少量已知 CCFLY_*/CLAUDE_* 透传进容器。
// 显式挡掉 CCFLY_PROFILE(防降/升档)、PATH/LD_*(防注入)等。
func envAllowed(k string) bool {
	if strings.HasPrefix(k, "ANTHROPIC_") || strings.HasPrefix(k, "AWS_") {
		return true
	}
	switch k {
	case "CCFLY_WORKSPACE", "CCFLY_AUTOSTART", "CCFLY_SKIP_PERMISSIONS", "CCFLY_PERMISSION_MODE",
		"DISABLE_AUTOUPDATER", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
		"CLOUD_ML_REGION", "GOOGLE_APPLICATION_CREDENTIALS":
		return true
	}
	return false
}

func handleSpawn(w http.ResponseWriter, r *http.Request, d *docker) {
	var req spawnReq
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.ConnectTarget) == "" {
		http.Error(w, "bad request: connect_target required", http.StatusBadRequest)
		return
	}
	name := req.Name
	if name == "" {
		name = "ccfly-" + req.DeviceID
	}
	image := req.Image
	if image == "" {
		image = "ccfly:instance"
	}
	// CCFLY_CONNECT_TARGET 必注入;用户 env 经白名单过滤。
	env := map[string]string{"CCFLY_CONNECT_TARGET": req.ConnectTarget}
	for k, v := range req.Env {
		if envAllowed(k) {
			env[k] = v
		}
	}
	id, err := d.run(name, image, env)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"container_id": id, "name": name})
}

type stopReq struct {
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
}

func handleStop(w http.ResponseWriter, r *http.Request, d *docker) {
	var req stopReq
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := req.Name
	if name == "" && req.DeviceID != "" {
		name = "ccfly-" + req.DeviceID
	}
	if name == "" {
		http.Error(w, "name or device_id required", http.StatusBadRequest)
		return
	}
	if err := d.stop(name); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func handleList(w http.ResponseWriter, _ *http.Request, d *docker) {
	out, err := d.list()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": out})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
