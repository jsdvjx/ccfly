package mesh

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestConfiguredMeshEndpointsEnvironmentFirstLegacyFallback(t *testing.T) {
	t.Setenv(meshEndpointsEnv, `[
		{"url":"wss://114.132.213.6/mesh","dial_addr":"138.252.163.7:443"},
		{"url":"wss://backup.example/mesh"}
	]`)
	want := []MeshEndpoint{
		{URL: "wss://114.132.213.6/mesh", DialAddr: "138.252.163.7:443"},
		{URL: "wss://backup.example/mesh"},
		{URL: "wss://cc.hn/mesh"},
	}
	if got := configuredMeshEndpoints(nil, "wss://cc.hn/mesh"); !reflect.DeepEqual(got, want) {
		t.Fatalf("configuredMeshEndpoints() = %#v, want %#v", got, want)
	}
}

func TestConfiguredMeshEndpointsInvalidEnvironmentKeepsLegacy(t *testing.T) {
	t.Setenv(meshEndpointsEnv, `[{"url":"https://not-websocket.example/mesh"}]`)
	want := []MeshEndpoint{{URL: "wss://cc.hn/mesh"}}
	if got := configuredMeshEndpoints(nil, "wss://cc.hn/mesh"); !reflect.DeepEqual(got, want) {
		t.Fatalf("configuredMeshEndpoints() = %#v, want %#v", got, want)
	}
}

func TestParseMeshEndpointsRejectsInvalidDialAddress(t *testing.T) {
	_, err := parseMeshEndpoints(`[{"url":"wss://114.132.213.6/mesh","dial_addr":"138.252.163.7"}]`)
	if err == nil || !strings.Contains(err.Error(), "host:port") {
		t.Fatalf("parseMeshEndpoints() error = %v, want host:port error", err)
	}
}

func TestMeshURLWithTokenPreservesExistingQuery(t *testing.T) {
	got, err := meshURLWithToken("wss://example.test/mesh?region=ix&token=old", "a token/+?")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("region") != "ix" || u.Query().Get("token") != "a token/+?" {
		t.Fatalf("unexpected query in %q", got)
	}
}

func TestMeshTransportDialsOverrideButVerifiesURLIdentity(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	base := server.Client().Transport.(*http.Transport)
	dialAddr := strings.TrimPrefix(server.URL, "https://")
	transport, err := meshTransport(base, dialAddr)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transport}
	// Port 1 is deliberately not listening. Success proves DialContext used the
	// override, while the URL host 127.0.0.1 remains the TLS certificate identity.
	resp, err := client.Get("https://127.0.0.1:1/probe")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
}
