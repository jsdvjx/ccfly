package control

import (
	"testing"
	"time"
)

// startMatches 锁定时区行为:基准是注册表的 startedAt(epoch 毫秒,无歧义),
// 与 ps lstart(本地渲染串,按本地时区解析)比对。回归防护:早先误把注册表的
// procStart(UTC 渲染串)当本地解析,在 UTC+8 下差 8h,当前会话进程永远定位不到。
func TestStartMatches(t *testing.T) {
	// 真实样本:pid 69579,startedAt=1781058559714ms。
	// 该 epoch 的本地渲染(取决于运行机器时区)就是 ps lstart 会给出的串。
	const startedAt = int64(1781058559714)
	localLstart := timeLocalLstart(startedAt)

	if !startMatches(startedAt, localLstart) {
		t.Fatalf("本地 lstart %q 应匹配 startedAt %d", localLstart, startedAt)
	}
	// 容差内(+3s)仍匹配。
	if !startMatches(startedAt, timeLocalLstart(startedAt+3000)) {
		t.Errorf("±5s 容差内应匹配")
	}
	// 偏 1 小时(模拟 pid 复用 / 时区错算)→ 不匹配。
	if startMatches(startedAt, timeLocalLstart(startedAt+3600_000)) {
		t.Errorf("偏 1h 不应匹配(防 pid 复用误杀)")
	}
	// 垃圾串 → 不匹配,不 panic。
	if startMatches(startedAt, "not a date") {
		t.Errorf("无法解析的 lstart 不应匹配")
	}
}

// timeLocalLstart 把 epoch 毫秒按本地时区渲染成 ps lstart 同格式串,模拟 `ps -o lstart=` 的输出。
func timeLocalLstart(ms int64) string {
	return time.UnixMilli(ms).Local().Format("Mon Jan _2 15:04:05 2006")
}
