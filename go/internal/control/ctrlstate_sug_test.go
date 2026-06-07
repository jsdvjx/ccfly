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
			// F6 CLAUDE select(单选)—— 编号选项 + "Enter to confirm · esc to cancel" 底栏 → select。
			name: "F6_claude_select",
			raw: "Do you want to proceed?\n" +
				"❯ 1. Yes\n" +
				"  2. No\n" +
				"  Enter to confirm · esc to cancel\n",
			want: "select",
		},
		{
			// F7 CLAUDE multi-select —— 编号选项带复选框字形 + "Space to select" 底栏 → 仍是 select。
			// 这是多选菜单的回归护栏:reOpt 的可选复选框分组须命中,detectState 不能因复选框而漏判。
			name: "F7_claude_multiselect",
			raw: "Which checks should run?\n" +
				"❯ 1. ◉ Lint\n" +
				"  2. ◯ Typecheck\n" +
				"  3. ◉ Unit tests\n" +
				"  Space to select · Enter to confirm · esc to cancel\n",
			want: "select",
		},
		{
			// F8 CLAUDE permission prompt mid-turn —— 回合进行中弹出的权限/确认菜单同时保留
			// "esc to interrupt" 行。busy 不得抢清晰 select:既有从 1 起带 ❯ 的编号菜单,判 select。
			// 这是「busy 误报」(权限弹窗被误显示成 忙碌+中断、菜单无从操作)的回归护栏。
			name: "F8_permission_prompt_with_interrupt_footer",
			raw: "Do you want to make this edit to ctrlstate.go?\n" +
				"❯ 1. Yes\n" +
				"  2. Yes, and don't ask again this session\n" +
				"  3. No, and tell Claude what to do differently\n" +
				"  Enter to confirm · esc to interrupt\n",
			want: "select",
		},
		{
			// F9 真 busy(无菜单)—— 仅 spinner + interrupt 行,无「从 1 起带游标的编号菜单」→ 仍 busy。
			// 护栏:looksLikeSelect 分流不得误把真 busy 帧判成 select(它不含编号菜单)。
			name: "F9_busy_no_menu_stays_busy",
			raw: "✻ Cogitating… (12s · ↓ 3.1k tokens · esc to interrupt)\n" +
				"  Tip: use /compact to free up context\n",
			want: "busy",
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

// TestDetectSelectOptions — 选项解析的逐项断言:
//
//	单选(F6 形态):每项 Checked==nil(无复选框),actions 不含「切换」(无 Space)。
//	多选(F7 形态):复选框字形被解析成 Checked 三态(◉/●/☑→true、◯/○/□→false),
//	               actions 含「切换(Space)」,且标签里的复选框字形已被剥离。
//
// 这是「多选 checkbox 菜单未建模」修复的核心护栏:单选保持原样,多选新增 Checked + Space。
func TestDetectSelectOptions(t *testing.T) {
	// 单选:无复选框 → Checked 全 nil、无 Space 动作。
	t.Run("single_select_no_checkbox", func(t *testing.T) {
		st := detectState("Do you want to proceed?\n" +
			"❯ 1. Yes\n" +
			"  2. No\n" +
			"  Enter to confirm · esc to cancel\n")
		if st.Kind != "select" {
			t.Fatalf("Kind = %q, want select", st.Kind)
		}
		if len(st.Options) != 2 {
			t.Fatalf("len(Options) = %d, want 2", len(st.Options))
		}
		for _, o := range st.Options {
			if o.Checked != nil {
				t.Fatalf("single-select option %q got Checked=%v, want nil", o.Label, *o.Checked)
			}
		}
		if o := st.Options[0]; o.Label != "Yes" || !o.Cur {
			t.Fatalf("option[0] = {label:%q cur:%v}, want {Yes true}", o.Label, o.Cur)
		}
		if hasAction(st, "切换") {
			t.Fatalf("single-select should not expose a 切换(Space) action: %+v", st.Actions)
		}
		if !hasAction(st, "确认") {
			t.Fatalf("single-select should expose a 确认(Enter) action: %+v", st.Actions)
		}
	})

	// 多选:复选框字形 → Checked 三态、剥离标签里的字形、新增 切换(Space) 动作。
	t.Run("multi_select_checkboxes", func(t *testing.T) {
		st := detectState("Which checks should run?\n" +
			"❯ 1. ◉ Lint\n" +
			"  2. ◯ Typecheck\n" +
			"  3. ☑ Unit tests\n" +
			"  Space to select · Enter to confirm · esc to cancel\n")
		if st.Kind != "select" {
			t.Fatalf("Kind = %q, want select", st.Kind)
		}
		if len(st.Options) != 3 {
			t.Fatalf("len(Options) = %d, want 3", len(st.Options))
		}
		want := []struct {
			label   string
			checked bool
		}{{"Lint", true}, {"Typecheck", false}, {"Unit tests", true}}
		for i, w := range want {
			o := st.Options[i]
			if o.Label != w.label {
				t.Fatalf("option[%d].Label = %q, want %q (复选框字形未从标签剥离?)", i, o.Label, w.label)
			}
			if o.Checked == nil {
				t.Fatalf("option[%d] %q got Checked=nil, want %v", i, o.Label, w.checked)
			}
			if *o.Checked != w.checked {
				t.Fatalf("option[%d] %q got Checked=%v, want %v", i, o.Label, *o.Checked, w.checked)
			}
		}
		if !hasAction(st, "切换") {
			t.Fatalf("multi-select must expose a 切换(Space) action: %+v", st.Actions)
		}
		if !hasActionKey(st, "切换", "Space") {
			t.Fatalf("multi-select 切换 action must send Space: %+v", st.Actions)
		}
		if !hasAction(st, "确认") {
			t.Fatalf("multi-select must expose a 确认(Enter) action: %+v", st.Actions)
		}
	})

	// 仅靠底栏「Space to select」判多选(选项不带复选框字形,如纯文本多选)→ 仍给 切换 动作。
	t.Run("multi_select_by_footer_only", func(t *testing.T) {
		st := detectState("Pick tags:\n" +
			"❯ 1. alpha\n" +
			"  2. beta\n" +
			"  Space to select · Enter to confirm · esc to cancel\n")
		if st.Kind != "select" {
			t.Fatalf("Kind = %q, want select", st.Kind)
		}
		if !hasAction(st, "切换") {
			t.Fatalf("footer-only multi-select must expose a 切换(Space) action: %+v", st.Actions)
		}
	})
}

func hasAction(st ctrlState, label string) bool {
	for _, a := range st.Actions {
		if a.Label == label {
			return true
		}
	}
	return false
}

func hasActionKey(st ctrlState, label, key string) bool {
	for _, a := range st.Actions {
		if a.Label == label {
			for _, k := range a.Keys {
				if k == key {
					return true
				}
			}
		}
	}
	return false
}
