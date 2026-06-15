package control

// proxyenv.go — 给 ccfly 创建的 tmux 会话默认注入「代理 + 局域网 bypass」环境变量。
//
// 场景:用户把出网流量经 ccfly 的本地 forward(如 127.0.0.1:2080 → mesh 出口)代理出去。
// 但全局代理会把 claude 起的 localhost 服务、局域网其它机器的请求也吞进 mesh → 够不到。
// 让 ccfly 创建会话(new / attach / /term / /start)时自动设好 http(s)_proxy/all_proxy +
// no_proxy(loopback + 私网 + link-local + mDNS 直连),会话里的 claude 及其子进程、用户手敲
// 的命令就「出网走代理、本机/局域网直连」,无需逐会话手动 export。
//
// 开关:仅当 CCFLY_TMUX_PROXY 配了代理 URL 才注入;未配 → 零行为变化(不影响默认部署)。
//   CCFLY_TMUX_PROXY     代理 URL,如 http://127.0.0.1:2080 或 socks5h://127.0.0.1:2080
//   CCFLY_TMUX_NO_PROXY  覆盖 bypass 列表(默认 defaultTmuxNoProxy)
// 注:tmux new-session -e 需要 tmux ≥ 3.2(2021)。

import (
	"os"
	"strings"
)

// defaultTmuxNoProxy 默认 bypass:loopback + 全部 RFC1918 私网(含 Docker 默认网桥 172.16/12)+
// link-local(169.254)+ mDNS(*.local)。覆盖「claude 起的 localhost 服务 / 局域网其它机器」两类。
// 说明:no_proxy 的 CIDR 支持因工具而异 —— Go 系工具与多数现代 CLI 认 CIDR;curl 只按主机/域后缀
// 匹配(localhost/127.0.0.1 一定生效,LAN IP 段 best-effort)。故 loopback 用裸地址、私网用 CIDR 兼顾。
const defaultTmuxNoProxy = "localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,169.254.0.0/16,*.local"

// tmuxProxyEnvArgs 返回注入 `tmux new-session` 的 `-e KEY=VAL` 参数对(无配置 → nil)。
// 大小写两套都设(http_proxy 与 HTTP_PROXY 等),兼容只认其一的工具。
func tmuxProxyEnvArgs() []string {
	proxy := strings.TrimSpace(os.Getenv("CCFLY_TMUX_PROXY"))
	if proxy == "" {
		return nil // 未配置:不注入,默认部署零影响
	}
	noProxy := strings.TrimSpace(os.Getenv("CCFLY_TMUX_NO_PROXY"))
	if noProxy == "" {
		noProxy = defaultTmuxNoProxy
	}
	kv := [][2]string{
		{"http_proxy", proxy}, {"https_proxy", proxy}, {"all_proxy", proxy},
		{"HTTP_PROXY", proxy}, {"HTTPS_PROXY", proxy}, {"ALL_PROXY", proxy},
		{"no_proxy", noProxy}, {"NO_PROXY", noProxy},
	}
	args := make([]string, 0, len(kv)*2+2)
	for _, p := range kv {
		args = append(args, "-e", p[0]+"="+p[1]) // tmux 直接按 argv 取值,URL 的 :// 无需转义
	}
	// 出口若做 MITM(byway -bump),会话里的 claude 经代理访问 AI 会撞到出口重签的证书 —— 默认信任库
	// 不认 → TLS 校验失败。CCFLY_TMUX_PROXY_CA 指向云端下发并落盘的出口 CA bundle(见 mesh.applyProxyCA),
	// 注入 NODE_EXTRA_CA_CERTS(Node/claude code 用,且是**叠加**信任、不会顶掉系统根证书)。仅在配了
	// 代理时才注入(无代理则无需信任出口 CA)。
	if ca := strings.TrimSpace(os.Getenv("CCFLY_TMUX_PROXY_CA")); ca != "" {
		args = append(args, "-e", "NODE_EXTRA_CA_CERTS="+ca)
	}
	return args
}

// TmuxProxyEnvArgs 导出给 cmd/ccfly(ccfly new / attach)复用同一注入逻辑。
func TmuxProxyEnvArgs() []string { return tmuxProxyEnvArgs() }
