package cloudhttp

import (
	"net/http"
	"testing"
)

// 设了 HTTP(S)_PROXY/ALL_PROXY,agent 云端链路也必须直连(Proxy 返回 nil)。
// 这正是 Windows 注册表残留代理变量把计划任务里的 connect 掐死的场景。
func TestClientIgnoresEnvProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1")
	req, _ := http.NewRequest("POST", "https://cc.hn/api/pair/start", nil)
	u, err := Client.Transport.(*http.Transport).Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("env proxy leaked into cloud client: %v", u)
	}
}

func TestParseProxy(t *testing.T) {
	if u := parseProxy(""); u != nil {
		t.Fatalf("empty: %v", u)
	}
	if u := parseProxy("  "); u != nil {
		t.Fatalf("blank: %v", u)
	}
	if u := parseProxy("http://127.0.0.1:7890"); u == nil || u.Host != "127.0.0.1:7890" {
		t.Fatalf("valid url: %v", u)
	}
	if u := parseProxy("://bad url"); u != nil {
		t.Fatalf("garbage should fall back to direct: %v", u)
	}
}
