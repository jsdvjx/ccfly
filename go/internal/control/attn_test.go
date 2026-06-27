package control

import "testing"

// TestAttnKindFromState_Direct 直接喂 ctrlState,验证分类优先级(plan > permission > choice)与边界。
func TestAttnKindFromState_Direct(t *testing.T) {
	opt := func(label string) ctrlOption { return ctrlOption{Label: label} }
	cases := []struct {
		name string
		st   ctrlState
		want string
	}{
		{"non-select-busy", ctrlState{Kind: "busy"}, ""},
		{"non-select-input", ctrlState{Kind: "input"}, ""},
		{"non-select-offline", ctrlState{Kind: "offline"}, ""},
		{"plan-by-autoaccept", ctrlState{Kind: "select", Title: "Ready to code?", Options: []ctrlOption{
			opt("Yes, and auto-accept edits"), opt("Yes, and manually approve edits"), opt("No, keep planning"),
		}}, attnPlan},
		{"plan-beats-permission", ctrlState{Kind: "select", Title: "Do you want to proceed?", Options: []ctrlOption{
			opt("Yes, and auto-accept edits"), opt("No, keep planning"),
		}}, attnPlan},
		{"permission-by-title", ctrlState{Kind: "select", Title: "Do you want to make this edit?", Options: []ctrlOption{
			opt("Yes"), opt("No, and tell Claude what to do differently"),
		}}, attnPermission},
		{"permission-by-dontask", ctrlState{Kind: "select", Title: "Bash command", Options: []ctrlOption{
			opt("Yes"), opt("Yes, and don't ask again this session"), opt("No"),
		}}, attnPermission},
		{"choice-default", ctrlState{Kind: "select", Title: "Which database should we use?", Options: []ctrlOption{
			opt("PostgreSQL"), opt("MySQL"), opt("SQLite"),
		}}, attnChoice},
	}
	for _, c := range cases {
		if got := attnKindFromState(c.st); got != c.want {
			t.Errorf("%s: attnKindFromState = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestAttnKindFromState_ViaDetectState 端到端:真实框型文本 → detectState → 分类。
// 确认 attn.go 与 detectState 口径一致(select 解析出 Title/Options 后能被正确归类)。
func TestAttnKindFromState_ViaDetectState(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   string
	}{
		{
			name: "permission-edit",
			screen: "Do you want to make this edit to attn.go?\n" +
				"❯ 1. Yes\n" +
				"  2. Yes, and don't ask again this session\n" +
				"  3. No, and tell Claude what to do differently\n" +
				"\n" +
				"Enter to confirm · Esc to cancel\n",
			want: attnPermission,
		},
		{
			name: "plan-exitplanmode",
			screen: "Ready to code?\n" +
				"❯ 1. Yes, and auto-accept edits\n" +
				"  2. Yes, and manually approve edits\n" +
				"  3. No, keep planning\n" +
				"\n" +
				"Enter to confirm · Esc to cancel\n",
			want: attnPlan,
		},
		{
			name: "choice-askuserquestion",
			screen: "Which database should we use?\n" +
				"❯ 1. PostgreSQL\n" +
				"  2. MySQL\n" +
				"  3. SQLite\n" +
				"\n" +
				"Enter to confirm · Esc to cancel\n",
			want: attnChoice,
		},
	}
	for _, c := range cases {
		st := detectState(c.screen)
		if st.Kind != "select" {
			t.Fatalf("%s: detectState Kind = %q, want select (title=%q opts=%d)", c.name, st.Kind, st.Title, len(st.Options))
		}
		if got := attnKindFromState(st); got != c.want {
			t.Errorf("%s: attnKindFromState = %q, want %q (title=%q)", c.name, got, c.want, st.Title)
		}
	}
}
