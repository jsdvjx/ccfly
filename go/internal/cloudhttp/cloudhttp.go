// Package cloudhttp 是设备 agent 与 ccfly-cloud 之间所有直连流量(配对/控制面/mesh WSS/
// 会话同步/claude login)共用的 HTTP 客户端。
//
// 与 http.DefaultClient 的唯一区别:不读 HTTP(S)_PROXY / ALL_PROXY 环境变量,一律直连。
// 设备上残留的持久代理变量会把 agent 自身的云端链路整个导进一个可能已死的本地代理
// (Windows 最典型:交互 shell 里临时变量配对成功,计划任务却从注册表带出指向已退出
// 代理的变量,connect 稳定 EOF),且任务环境与交互 shell 不同步,极难排查 —— agent
// 自身的云端链路必须确定性直连,不受用户机器的代理配置摆布。
//
// 确需经代理才能到达云端的部署,用 CCFLY_PROXY=<url>(如 http://127.0.0.1:7890)显式
// 指定,只影响本包客户端;给会话注入的代理环境、UI 包下载(npm registry)等仍走原有逻辑。
package cloudhttp

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

// Client is the shared HTTP client for all agent→cloud traffic.
var Client = &http.Client{Transport: newTransport()}

func newTransport() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = func(*http.Request) (*url.URL, error) { return proxyURL(), nil }
	return t
}

var proxyURL = sync.OnceValue(func() *url.URL { return parseProxy(os.Getenv("CCFLY_PROXY")) })

func parseProxy(raw string) *url.URL {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		log.Printf("ccfly: CCFLY_PROXY=%q 无法解析,忽略并直连: %v", raw, err)
		return nil
	}
	log.Printf("ccfly: agent 云端链路经 CCFLY_PROXY=%s", u.Redacted())
	return u
}
