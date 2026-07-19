package mesh

// coredns.go embeds a deliberately small CoreDNS build. Only bind, template
// and forward are linked: intercepted A/AAAA queries are synthesized to
// loopback, while every other query is handed to the configured upstreams.

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	_ "github.com/coredns/coredns/plugin/bind"
	_ "github.com/coredns/coredns/plugin/forward"
	_ "github.com/coredns/coredns/plugin/template"
)

const (
	defaultCoreDNSListenIP = "127.0.0.1"
	defaultCoreDNSPort     = 53
	maxCoreDNSUpstreams    = 15 // CoreDNS forward plugin limit.
)

var defaultSNIUpstreams = []string{"223.5.5.5:53", "223.6.6.6:53"}

// Variables make the privileged production endpoint replaceable in
// integration tests without weakening the production protocol.
var (
	sniCoreDNSListenIP = defaultCoreDNSListenIP
	sniCoreDNSPort     = defaultCoreDNSPort
)

type coreDNSService struct {
	instance *caddy.Instance
}

func init() {
	caddy.Quiet = true
	dnsserver.Quiet = true
}

func startCoreDNS(listenIP string, port int, domains, upstreams []string) (*coreDNSService, error) {
	corefile, err := renderCorefile(listenIP, port, domains, upstreams)
	if err != nil {
		return nil, err
	}
	inst, err := caddy.Start(caddy.CaddyfileInput{
		Contents:       []byte(corefile),
		Filepath:       "ccfly-memory-Corefile",
		ServerTypeName: "dns",
	})
	if err != nil {
		if inst != nil {
			_ = inst.Stop()
			_ = inst.ShutdownCallbacks()
		}
		return nil, fmt.Errorf("start CoreDNS on %s:%d: %w", listenIP, port, err)
	}
	return &coreDNSService{instance: inst}, nil
}

func (s *coreDNSService) Stop() error {
	if s == nil || s.instance == nil {
		return nil
	}
	inst := s.instance
	s.instance = nil
	stopErr := inst.Stop()
	callbackErrs := inst.ShutdownCallbacks()
	return errors.Join(append([]error{stopErr}, callbackErrs...)...)
}

func renderCorefile(listenIP string, port int, domains, upstreams []string) (string, error) {
	if net.ParseIP(listenIP) == nil {
		return "", fmt.Errorf("invalid CoreDNS listen IP %q", listenIP)
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid CoreDNS port %d", port)
	}
	domains = filterAllowedHosts(domains)
	if len(domains) == 0 || len(domains) > 128 {
		return "", fmt.Errorf("CoreDNS requires 1..128 valid intercept domains")
	}
	upstreams = normalizeDNSUpstreams(upstreams)
	if len(upstreams) == 0 {
		return "", fmt.Errorf("CoreDNS requires at least one valid IP upstream")
	}

	quotedDomains := make([]string, 0, len(domains))
	for _, domain := range domains {
		quotedDomains = append(quotedDomains, regexp.QuoteMeta(domain))
	}
	// request.Name() is always fully-qualified. This matches each apex and all
	// of its subdomains, but never lookalikes such as anthropic.com.evil.test.
	match := `^(?:[^.]+\.)*(?:` + strings.Join(quotedDomains, "|") + `)\.$`

	var b strings.Builder
	fmt.Fprintf(&b, ".:%d {\n", port)
	fmt.Fprintf(&b, "    bind %s\n", listenIP)
	fmt.Fprintf(&b, "    template IN A {\n        match %s\n        answer %q\n        fallthrough\n    }\n", match, "{{ .Name }} 30 IN A 127.0.0.1")
	fmt.Fprintf(&b, "    template IN AAAA {\n        match %s\n        answer %q\n        fallthrough\n    }\n", match, "{{ .Name }} 30 IN AAAA ::1")
	fmt.Fprintf(&b, "    forward . %s\n", strings.Join(upstreams, " "))
	b.WriteString("}\n")
	return b.String(), nil
}

// normalizeDNSUpstreams accepts only IP literals, optionally with a port.
// Returning canonical host:port tokens makes the generated Corefile immune to
// directive injection and also supports isolated high-port integration tests.
func normalizeDNSUpstreams(raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		var endpoint string
		if addr, err := netip.ParseAddr(value); err == nil {
			endpoint = net.JoinHostPort(addr.String(), "53")
		} else if addrPort, err := netip.ParseAddrPort(value); err == nil && addrPort.Port() != 0 {
			endpoint = net.JoinHostPort(addrPort.Addr().String(), strconv.Itoa(int(addrPort.Port())))
		} else {
			continue
		}
		if !seen[endpoint] {
			seen[endpoint] = true
			out = append(out, endpoint)
			if len(out) == maxCoreDNSUpstreams {
				break
			}
		}
	}
	return out
}

func upstreamIP(endpoint string) string {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return endpoint
	}
	return host
}
