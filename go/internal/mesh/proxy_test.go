package mesh

import "testing"

// TestAddAutoForwardDedup:云端下发的自动转发不得覆盖/重复用户已配的同 localPort 转发。
func TestAddAutoForwardDedup(t *testing.T) {
	forwardSpecs = nil // 干净起点(包级变量)
	t.Cleanup(func() { forwardSpecs = nil })

	// 用户已配 2080(--overlay-forward 2080:100.64.0.5:9000)。
	forwardSpecs = []forwardSpec{{localPort: 2080, dstPort: 9000}}
	addAutoForward(2080, "100.64.0.1", 2080) // 云端策略也想要 2080 → 应跳过,不动用户的
	if len(forwardSpecs) != 1 || forwardSpecs[0].dstPort != 9000 {
		t.Fatalf("同 localPort 已存在应跳过、保留用户配置,得 %+v", forwardSpecs)
	}

	// 新的 localPort → 追加。
	addAutoForward(3128, "100.64.0.1", 3128)
	if len(forwardSpecs) != 2 || forwardSpecs[1].localPort != 3128 || forwardSpecs[1].dst.String() != "100.64.0.1" {
		t.Fatalf("新端口应追加,得 %+v", forwardSpecs)
	}

	// 坏 IP → 失败安全,不追加、不 panic。
	addAutoForward(9999, "not-an-ip", 9999)
	if len(forwardSpecs) != 2 {
		t.Fatalf("坏 IP 不应追加,得 %+v", forwardSpecs)
	}
}
