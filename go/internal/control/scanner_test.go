package control

// scanner_test.go — Goal C 回收判定(scanner.go 的纯函数 shouldReap)的表驱动回归。
//
// 只测纯判定逻辑(不碰真 tmux):每条用例固定 now、构造一个 tmuxPane + strike,断言是否该杀。
// 各闸覆盖:命名空间(cc-)、在跑 claude、attach 数、新建宽限、连续 strike 阈值。

import "testing"

func TestShouldReap(t *testing.T) {
	const now int64 = 1_000_000
	old := now - reapGraceSec - 10 // 过了宽限期的创建时刻
	fresh := now - 5               // 仍在宽限期内

	cases := []struct {
		name   string
		pane   tmuxPane
		strike int
		want   bool
	}{
		{
			name:   "孤儿壳:cc-+无claude+过宽限+无attach+本拍达阈值",
			pane:   tmuxPane{Name: "cc-deadbeef", CurCmd: "zsh", Created: old, Attached: 0},
			strike: reapStrikes - 1, // strike+1 == reapStrikes
			want:   true,
		},
		{
			name:   "非 cc- 命名空间:绝不杀",
			pane:   tmuxPane{Name: "work", CurCmd: "zsh", Created: old, Attached: 0},
			strike: 99,
			want:   false,
		},
		{
			name:   "在跑 claude(版本号串):永不杀",
			pane:   tmuxPane{Name: "cc-deadbeef", CurCmd: "2.1.167", Created: old, Attached: 0},
			strike: 99,
			want:   false,
		},
		{
			name:   "有客户端 attach:不杀",
			pane:   tmuxPane{Name: "cc-deadbeef", CurCmd: "zsh", Created: old, Attached: 1},
			strike: 99,
			want:   false,
		},
		{
			name:   "新建宽限内:不杀",
			pane:   tmuxPane{Name: "cc-deadbeef", CurCmd: "zsh", Created: fresh, Attached: 0},
			strike: 99,
			want:   false,
		},
		{
			name:   "strike 不足(首次见到 claude-less):先不杀,记一笔",
			pane:   tmuxPane{Name: "cc-deadbeef", CurCmd: "zsh", Created: old, Attached: 0},
			strike: 0, // strike+1 == 1 < reapStrikes(2)
			want:   false,
		},
		{
			name:   "Created==0(创建时刻未知):失败安全,绝不杀(否则 #{session_created} 解析失败会误杀新建会话)",
			pane:   tmuxPane{Name: "cc-deadbeef", CurCmd: "bash", Created: 0, Attached: 0},
			strike: 99,
			want:   false,
		},
		{
			name:   "以 claude 启动(临时 shell 出长命令):不杀",
			pane:   tmuxPane{Name: "cc-deadbeef", CurCmd: "zsh", StartCmd: "claude --resume 1111-2222", Created: old, Attached: 0},
			strike: 99,
			want:   false,
		},
	}

	for _, c := range cases {
		if got := shouldReap(c.pane, now, c.strike); got != c.want {
			t.Errorf("%s: shouldReap=%v 期望 %v", c.name, got, c.want)
		}
	}
}
