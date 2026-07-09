package mesh

import "testing"

// entitlement_test.go — 设备侧「内部功能准入」标志的读取语义(ClaudeLoginAllowed)。
//
// 契约(三态,绝不误伤未开闸部署):
//   - conn 文件无该字段(nil,老云端/尚未刷新)→ 放行(true)
//   - 云端明确下发 can_use_claude=false → 拒绝(false)
//   - 明确 true → 放行
//   - 完全没有该 host 的 conn 文件 → 放行(交给下游报「未入网」)

func boolPtr(b bool) *bool { return &b }

func TestClaudeLoginAllowed(t *testing.T) {
	isolateHome(t)

	// 未知 host,无 conn 文件 → 放行。
	if !ClaudeLoginAllowed("cc.hn") {
		t.Fatal("无 conn 文件应放行(nil 语义)")
	}

	// nil(老云端没下发该字段)→ 放行。
	writeConn(t, State{Host: "cc.hn", MeshToken: "m1"})
	if !ClaudeLoginAllowed("cc.hn") {
		t.Fatal("can_use_claude 未设(nil)应放行,绝不误伤未开闸部署")
	}

	// 明确 false → 拒绝。
	writeConn(t, State{Host: "cc.hn", MeshToken: "m1", CanUseClaude: boolPtr(false)})
	if ClaudeLoginAllowed("cc.hn") {
		t.Fatal("can_use_claude=false 应拒绝(不暴露 claude login)")
	}

	// 明确 true → 放行。
	writeConn(t, State{Host: "cc.hn", MeshToken: "m1", CanUseClaude: boolPtr(true)})
	if !ClaudeLoginAllowed("cc.hn") {
		t.Fatal("can_use_claude=true 应放行")
	}
}

// sameBool 的三态比较(refreshConfig 用它判断是否需要落盘)。
func TestSameBool(t *testing.T) {
	cases := []struct {
		a, b *bool
		want bool
	}{
		{nil, nil, true},
		{nil, boolPtr(true), false},
		{boolPtr(true), nil, false},
		{boolPtr(true), boolPtr(true), true},
		{boolPtr(true), boolPtr(false), false},
		{boolPtr(false), boolPtr(false), true},
	}
	for i, c := range cases {
		if got := sameBool(c.a, c.b); got != c.want {
			t.Fatalf("case %d: sameBool=%v want %v", i, got, c.want)
		}
	}
}
