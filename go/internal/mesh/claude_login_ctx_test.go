package mesh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// isolateHome 把 HOME 指向一个临时目录,使 stateDir() 落到 t.TempDir()/.ccfly,
// 测试不碰真实 ~/.ccfly;并清理 EnsureTmuxProxyEnv 会读/写的环境变量。
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CCFLY_TMUX_PROXY", "")
	os.Unsetenv("CCFLY_TMUX_PROXY")
	t.Setenv("CCFLY_TMUX_PROXY_CA", "")
	os.Unsetenv("CCFLY_TMUX_PROXY_CA")
	return home
}

// writeConn 落一个 conn-<host>.json(只设测试关心的字段)。
func writeConn(t *testing.T, st State) {
	t.Helper()
	dir, err := stateDir()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "conn-"+st.Host+".json")
	data, _ := json.Marshal(st)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// 登录上下文 save → load → clear 闭环。
func TestClaudeLoginContextRoundTrip(t *testing.T) {
	isolateHome(t)

	if got := LoadClaudeLoginContexts(); len(got) != 0 {
		t.Fatalf("起点应无上下文,得 %d", len(got))
	}
	want := ClaudeLoginContext{Host: "cc.hn", AccountEmail: "a@b.co", EgressV6: "2001:db8::1", OutNodeIP: "100.64.0.9"}
	if err := SaveClaudeLoginContext(want); err != nil {
		t.Fatal(err)
	}
	got := LoadClaudeLoginContexts()
	if len(got) != 1 || got[0].AccountEmail != want.AccountEmail || got[0].EgressV6 != want.EgressV6 || got[0].OutNodeIP != want.OutNodeIP {
		t.Fatalf("load 不匹配: %+v", got)
	}
	if got[0].UpdatedAt == "" {
		t.Fatal("save 应填 UpdatedAt")
	}

	// clear 指定不存在的 host:不删任何东西、不报错。
	if n, err := ClearClaudeLoginContext("other.host"); err != nil || n != 0 {
		t.Fatalf("clear 不存在 host 应 0/nil,得 %d/%v", n, err)
	}
	if n, err := ClearClaudeLoginContext("cc.hn"); err != nil || n != 1 {
		t.Fatalf("clear 应删 1,得 %d/%v", n, err)
	}
	if got := LoadClaudeLoginContexts(); len(got) != 0 {
		t.Fatalf("clear 后应空,得 %d", len(got))
	}
	// 幂等:再 clear no-op。
	if n, _ := ClearClaudeLoginContext(""); n != 0 {
		t.Fatalf("再 clear 应 0,得 %d", n)
	}
}

func TestSaveClaudeLoginContextEmptyHost(t *testing.T) {
	isolateHome(t)
	if err := SaveClaudeLoginContext(ClaudeLoginContext{AccountEmail: "x"}); err == nil {
		t.Fatal("空 host 应报错")
	}
}

// 用户已显式设 CCFLY_TMUX_PROXY → EnsureTmuxProxyEnv 一律尊重不覆盖。
func TestEnsureTmuxProxyEnvRespectsUser(t *testing.T) {
	isolateHome(t)
	t.Setenv("CCFLY_TMUX_PROXY", "http://user:set@1.2.3.4:9")
	writeConn(t, State{Host: "cc.hn", ProxyPort: 2080, ProxyScheme: "http"})
	_ = SaveClaudeLoginContext(ClaudeLoginContext{Host: "cc.hn", ProxyURL: "http://acct@5.6.7.8:8443"})
	EnsureTmuxProxyEnv()
	if got := os.Getenv("CCFLY_TMUX_PROXY"); got != "http://user:set@1.2.3.4:9" {
		t.Fatalf("用户值应不被覆盖,得 %q", got)
	}
}

// overlay 代理与账号直连 URL 并存 → **overlay 优先**(按账号出口路由由云端 sing-box 按源 IP
// 处理;账号直连 URL 的出口按来源 IP 放行,设备家宽源会被 56ms 拒 400 —— 2026-07-03 实锤)。
func TestEnsureTmuxProxyEnvPrefersOverlayOverAccountProxy(t *testing.T) {
	isolateHome(t)
	writeConn(t, State{Host: "cc.hn", ProxyPort: 2080, ProxyScheme: "http"}) // overlay 代理在
	acctProxy := "http://deadbeef:secret@sg.example:8443"
	_ = SaveClaudeLoginContext(ClaudeLoginContext{Host: "cc.hn", AccountEmail: "a@b.co", ProxyURL: acctProxy})
	EnsureTmuxProxyEnv()
	if got := os.Getenv("CCFLY_TMUX_PROXY"); got != "http://127.0.0.1:2080" {
		t.Fatalf("应优先设备级 overlay 代理,得 %q", got)
	}
}

// 无 overlay 配置(conn 文件缺 ProxyPort)但登录上下文带 ProxyURL → 才回退按账号直连。
func TestEnsureTmuxProxyEnvAccountProxyAsLastResort(t *testing.T) {
	isolateHome(t)
	acctProxy := "http://deadbeef:secret@sg.example:8443"
	_ = SaveClaudeLoginContext(ClaudeLoginContext{Host: "cc.hn", AccountEmail: "a@b.co", ProxyURL: acctProxy})
	EnsureTmuxProxyEnv()
	if got := os.Getenv("CCFLY_TMUX_PROXY"); got != acctProxy {
		t.Fatalf("无 overlay 时应回退按账号代理 %q,得 %q", acctProxy, got)
	}
}

// 登录上下文无 ProxyURL(设备拿不到 byway secret 的现状)→ 安全回退设备级 overlay 代理,
// 不把会话打成无代理直连。
func TestEnsureTmuxProxyEnvFallsBackWhenNoAccountProxy(t *testing.T) {
	isolateHome(t)
	writeConn(t, State{Host: "cc.hn", ProxyPort: 2080, ProxyScheme: "http"})
	_ = SaveClaudeLoginContext(ClaudeLoginContext{Host: "cc.hn", AccountEmail: "a@b.co", EgressV6: "2001:db8::1"}) // 无 ProxyURL
	EnsureTmuxProxyEnv()
	if got := os.Getenv("CCFLY_TMUX_PROXY"); got != "http://127.0.0.1:2080" {
		t.Fatalf("应回退 overlay 代理,得 %q", got)
	}
}

// 既无登录上下文也无 overlay 代理策略 → no-op(零行为变化)。
func TestEnsureTmuxProxyEnvNoop(t *testing.T) {
	isolateHome(t)
	writeConn(t, State{Host: "cc.hn"}) // ProxyPort=0
	EnsureTmuxProxyEnv()
	if got := os.Getenv("CCFLY_TMUX_PROXY"); got != "" {
		t.Fatalf("无任何代理策略应 no-op,得 %q", got)
	}
}
