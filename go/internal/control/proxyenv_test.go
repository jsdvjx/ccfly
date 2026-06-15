package control

import (
	"strings"
	"testing"
)

func TestTmuxProxyEnvArgs(t *testing.T) {
	// 未配置 → nil(零行为变化,默认部署不注入)。
	t.Setenv("CCFLY_TMUX_PROXY", "")
	if got := tmuxProxyEnvArgs(); got != nil {
		t.Fatalf("未配置应返回 nil,得 %v", got)
	}

	// 配了代理 → 注入 8 个 -e 对(http/https/all × 大小写两套 + no_proxy 两套)。
	t.Setenv("CCFLY_TMUX_PROXY", "http://127.0.0.1:2080")
	got := tmuxProxyEnvArgs()
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"-e http_proxy=http://127.0.0.1:2080",
		"-e HTTPS_PROXY=http://127.0.0.1:2080",
		"-e all_proxy=http://127.0.0.1:2080",
		"-e no_proxy=" + defaultTmuxNoProxy,
		"-e NO_PROXY=" + defaultTmuxNoProxy,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺注入 %q;实得:%s", want, joined)
		}
	}
	// 每个 -e 后必跟一个值(偶数个元素,-e 与值成对)。
	if len(got)%2 != 0 {
		t.Fatalf("-e 与值应成对,得奇数个:%d", len(got))
	}

	// 自定义 bypass 覆盖默认。
	t.Setenv("CCFLY_TMUX_NO_PROXY", "10.0.0.0/8,*.corp")
	got = tmuxProxyEnvArgs()
	j := strings.Join(got, " ")
	if !strings.Contains(j, "-e no_proxy=10.0.0.0/8,*.corp") || strings.Contains(j, defaultTmuxNoProxy) {
		t.Fatalf("CCFLY_TMUX_NO_PROXY 应覆盖默认;实得:%s", j)
	}
}
