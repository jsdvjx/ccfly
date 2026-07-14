package control

// snistatus.go — 本地自检端点 GET /sni-status,回答「这台设备的 SNI proxy 起没起、通不通」。
//
// control 不能 import mesh(会构成 import 环:mesh → control),故用注入的 SNIStatusFn:
// cmd/ccfly 在两包都可见处 SNIStatusFn = mesh.SNIStatusJSON。未接线(如独立 `ccfly serve`
// 无 mesh)→ 返回 503 unknown。GET /sni-status?probe=1 同步跑一次实时探测(人工核验用)。

import "net/http"

// SNIStatusFn 由 cmd/ccfly 注入为 mesh.SNIStatusJSON(fresh bool) any。nil = 本进程未跑 mesh。
var SNIStatusFn func(fresh bool) any

func handleSNIStatus(w http.ResponseWriter, r *http.Request) {
	if SNIStatusFn == nil {
		ctrlJSON(w, http.StatusServiceUnavailable, map[string]any{
			"available": false,
			"reason":    "mesh not running in this process (SNI arm 只在 `ccfly connect` 进程内)",
		})
		return
	}
	fresh := r.URL.Query().Get("probe") == "1"
	ctrlJSON(w, http.StatusOK, SNIStatusFn(fresh))
}
