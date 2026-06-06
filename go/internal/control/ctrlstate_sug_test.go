package control

import "testing"

// 真机 claude 2.1.x 抓屏(tmux -e)实测的输入行三态:鬼影建议 / 真实键入 / 清空。
// 鬼影建议 = ❯+U+00A0 后接 SGR7 首字 + SGR2(dim)余文;真实键入 = 常规属性、光标块在尾部。
func TestParseSuggestANSI(t *testing.T) {
	const NBSP = " "
	cases := []struct {
		name string
		line string
		want string
	}{
		{
			name: "ghost suggestion",
			line: "\x1b[39m❯" + NBSP + "\x1b[7mg\x1b[0;2mo ahead\x1b[0m",
			want: "go ahead",
		},
		{
			name: "real typed input (no suggestion)",
			line: "\x1b[39m❯" + NBSP + "real typed input\x1b[7m \x1b[0m",
			want: "",
		},
		{
			name: "empty box cursor only (no suggestion)",
			line: "\x1b[39m❯" + NBSP + "\x1b[7m \x1b[0m",
			want: "",
		},
		{
			name: "no input line at all",
			line: "\x1b[2msome dim prose\x1b[0m",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			screen := "──────────\n" + c.line + "\n──────────\n"
			got := parseSuggestANSI(screen)
			if got != c.want {
				t.Fatalf("parseSuggestANSI = %q, want %q", got, c.want)
			}
		})
	}
}

// TestDetectStateShellVsClaude — detectState 必须把「停在 zsh '❯ ' 的 pane」判成 offline,
// 而把 claude 各空闲/忙/菜单态正确归类。这是「斜杠命令被打进 zsh」bug 的回归护栏。
// F2/F3/F4 的当前输入行用 NBSP(claude 真机渲染);F1 的 '❯ ' 用普通空格(shell 提示符)。
func TestDetectStateShellVsClaude(t *testing.T) {
	const NBSP = " "
	const bar = "────────────────────────────────────────────────────────────────────────────────"
	cases := []struct {
		name string
		raw  string
		want string // 期望 ctrlState.Kind
	}{
		{
			// F1 SHELL —— claude 没在跑:无 ─── 边框、无提示行、'❯ ' 是普通空格 → offline。
			name: "F1_shell_zsh_caret",
			raw: "zsh: no such file or directory: /context\n" +
				"❯ /context\n" +
				"zsh: command not found: context\n" +
				"    ~/Jarvis  on   main !8 ?13                                              127 ✘  took 9s   at 13:53:43\n",
			want: "offline",
		},
		{
			// F2 CLAUDE idle —— 纯 ─── 边框 + 提示行 "← for agents" + ❯+NBSP 行 → input。
			name: "F2_claude_idle",
			raw: bar + "\n" +
				"❯" + NBSP + "\n" +
				bar + "\n" +
				"  ⏵⏵ auto mode on · 1 shell · ← for agents · ↓ to manage\n",
			want: "input",
		},
		{
			// F3 CLAUDE after /context —— 顶部静态 ⛶ 区块,但底部仍有边框 + "? for shortcuts" → input。
			name: "F3_claude_after_context",
			raw: "     ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶ ⛶   ⛁ System prompt: 6.4k tokens (3.2%)\n" +
				"     /context all to expand\n" +
				bar + "\n" +
				"❯" + NBSP + "\n" +
				bar + "\n" +
				"  ? for shortcuts · ← for agents\n",
			want: "input",
		},
		{
			// F4 CLAUDE after /model —— '❯ /model' 历史行是普通空格,但底部边框 + 提示行仍在 → input。
			name: "F4_claude_after_model",
			raw: "❯ /model\n" +
				"  ⎿  Kept model as Haiku 4.5\n" +
				bar + "\n" +
				"❯" + NBSP + "\n" +
				bar + "\n" +
				"  ? for shortcuts · ← for agents\n",
			want: "input",
		},
		{
			// F5 CLAUDE busy —— "esc to interrupt" 命中 reBusy(在 input 分支前)→ busy。
			name: "F5_claude_busy",
			raw:  "✻ Cogitating… (12s · ↓ 3.1k tokens · esc to interrupt)\n",
			want: "busy",
		},
		{
			// F6 CLAUDE select —— 编号选项 + "Enter to confirm · esc to cancel" 底栏 → select。
			name: "F6_claude_select",
			raw: "Do you want to proceed?\n" +
				"❯ 1. Yes\n" +
				"  2. No\n" +
				"  Enter to confirm · esc to cancel\n",
			want: "select",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := detectState(c.raw).Kind
			if got != c.want {
				t.Fatalf("detectState(%s).Kind = %q, want %q", c.name, got, c.want)
			}
		})
	}
}
