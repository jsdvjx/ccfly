package control

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 经真实 Handler() 起服务:未接线 → 503 available:false;接线后 → 200,且 ?probe=1 透传到注入函数。
func TestSNIStatusEndpoint(t *testing.T) {
	old := SNIStatusFn
	t.Cleanup(func() { SNIStatusFn = old })

	srv := httptest.NewServer(Handler())
	defer srv.Close()

	get := func(path string) (int, map[string]any) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return resp.StatusCode, m
	}

	// 未接线(本进程无 mesh)→ 503 + available:false。
	SNIStatusFn = nil
	if code, m := get("/sni-status"); code != http.StatusServiceUnavailable || m["available"] != false {
		t.Fatalf("未接线应 503 available:false,得 %d %+v", code, m)
	}

	// 接线:注入把 fresh 回显为 probed,验证 ?probe=1 的透传。
	SNIStatusFn = func(fresh bool) any { return map[string]any{"armed": true, "probed": fresh} }
	if code, m := get("/sni-status"); code != http.StatusOK || m["armed"] != true || m["probed"] != false {
		t.Fatalf("默认应 200 probed:false,得 %d %+v", code, m)
	}
	if code, m := get("/sni-status?probe=1"); code != http.StatusOK || m["probed"] != true {
		t.Fatalf("?probe=1 应把 fresh 透传为 true,得 %d %+v", code, m)
	}
}
