package mesh

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchDeviceConfigIncludesRuntimeSNI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/device/config" || r.URL.Query().Get("token") != "mesh token" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"sni":{"enabled":true,"account":"a@x.com","exit":{"host":"100.64.0.16","port":443},"intercept":["anthropic.com"]}}`))
	}))
	defer srv.Close()
	st := &State{Scheme: "http", Host: strings.TrimPrefix(srv.URL, "http://"), MeshToken: "mesh token"}
	c, err := fetchDeviceConfig(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if c.SNI == nil || !c.SNI.Enabled || c.SNI.Account != "a@x.com" || c.SNI.Exit.Host != "100.64.0.16" {
		t.Fatalf("unexpected SNI config: %+v", c.SNI)
	}
}
