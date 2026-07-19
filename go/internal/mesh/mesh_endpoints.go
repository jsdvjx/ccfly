package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jsdvjx/ccfly/go/internal/cloudhttp"
)

const meshEndpointsEnv = "CCFLY_MESH_ENDPOINTS"

// MeshEndpoint separates the HTTPS identity from the TCP destination. URL is
// used for the HTTP Host header and certificate verification; DialAddr, when
// set, is the host:port actually dialled. This is needed for an IP certificate
// whose publicly validated address differs from a private/IX transport address.
type MeshEndpoint struct {
	URL      string `json:"url"`
	DialAddr string `json:"dial_addr,omitempty"`
}

// configuredMeshEndpoints returns environment-configured endpoints first and
// always appends the legacy cloud-provided mesh_url as the final fallback.
// Invalid environment entries never take the legacy route down.
func configuredMeshEndpoints(configured []MeshEndpoint, legacyURL string) []MeshEndpoint {
	var endpoints []MeshEndpoint
	if raw := strings.TrimSpace(os.Getenv(meshEndpointsEnv)); raw != "" {
		parsed, err := parseMeshEndpoints(raw)
		if err != nil {
			log.Printf("ccfly: ignoring invalid %s: %v", meshEndpointsEnv, err)
		} else {
			endpoints = append(endpoints, parsed...)
		}
	}
	endpoints = append(endpoints, configured...)

	if legacyURL = strings.TrimSpace(legacyURL); legacyURL != "" {
		legacy := MeshEndpoint{URL: legacyURL}
		if _, err := normalizeMeshEndpoint(legacy); err == nil {
			endpoints = append(endpoints, legacy)
		}
	}

	seen := make(map[string]struct{}, len(endpoints))
	unique := endpoints[:0]
	for _, endpoint := range endpoints {
		normalized, err := normalizeMeshEndpoint(endpoint)
		if err != nil {
			continue
		}
		key := normalized.URL + "\x00" + normalized.DialAddr
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, normalized)
	}
	return unique
}

func parseMeshEndpoints(raw string) ([]MeshEndpoint, error) {
	var endpoints []MeshEndpoint
	if err := json.Unmarshal([]byte(raw), &endpoints); err != nil {
		return nil, fmt.Errorf("expected JSON array: %w", err)
	}
	if len(endpoints) == 0 {
		return nil, errors.New("endpoint list is empty")
	}
	for i, endpoint := range endpoints {
		normalized, err := normalizeMeshEndpoint(endpoint)
		if err != nil {
			return nil, fmt.Errorf("endpoint %d: %w", i, err)
		}
		endpoints[i] = normalized
	}
	return endpoints, nil
}

func normalizeMeshEndpoint(endpoint MeshEndpoint) (MeshEndpoint, error) {
	endpoint.URL = strings.TrimSpace(endpoint.URL)
	endpoint.DialAddr = strings.TrimSpace(endpoint.DialAddr)
	u, err := url.Parse(endpoint.URL)
	if err != nil || u.Host == "" {
		return MeshEndpoint{}, fmt.Errorf("invalid url %q", endpoint.URL)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return MeshEndpoint{}, fmt.Errorf("url scheme must be ws or wss: %q", endpoint.URL)
	}
	if u.User != nil || u.Fragment != "" {
		return MeshEndpoint{}, fmt.Errorf("url must not contain userinfo or fragment: %q", endpoint.URL)
	}
	if endpoint.DialAddr != "" {
		host, port, splitErr := net.SplitHostPort(endpoint.DialAddr)
		if splitErr != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
			return MeshEndpoint{}, fmt.Errorf("dial_addr must be host:port: %q", endpoint.DialAddr)
		}
	}
	return endpoint, nil
}

func meshURLWithToken(rawURL, token string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	query := u.Query()
	query.Set("token", token)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func meshHTTPClient(endpoint MeshEndpoint) (*http.Client, error) {
	if endpoint.DialAddr == "" {
		return cloudhttp.Client, nil
	}
	base, ok := cloudhttp.Client.Transport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("unsupported cloud HTTP transport %T", cloudhttp.Client.Transport)
	}
	transport, err := meshTransport(base, endpoint.DialAddr)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Transport:     transport,
		CheckRedirect: cloudhttp.Client.CheckRedirect,
		Jar:           cloudhttp.Client.Jar,
		Timeout:       cloudhttp.Client.Timeout,
	}, nil
}

func meshTransport(base *http.Transport, dialAddr string) (*http.Transport, error) {
	if _, _, err := net.SplitHostPort(dialAddr); err != nil {
		return nil, fmt.Errorf("invalid mesh dial address %q: %w", dialAddr, err)
	}
	transport := base.Clone()
	// A dial override names the exact network path. Sending it through an HTTP
	// proxy would make CONNECT target the certificate identity instead, defeating
	// the override and potentially leaking the mesh token to an unintended path.
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, dialAddr)
	}
	return transport, nil
}

func meshEndpointLabel(endpoint MeshEndpoint) string {
	if endpoint.DialAddr == "" {
		return endpoint.URL
	}
	return endpoint.URL + " dial=" + endpoint.DialAddr
}

func sameMeshEndpoints(a, b []MeshEndpoint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
