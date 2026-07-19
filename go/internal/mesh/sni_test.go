package mesh

// sni_test.go — CoreDNS 数据面与 SNI 配置判等回归。

import (
	"net"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestEmbeddedCoreDNSInterceptsAndForwards(t *testing.T) {
	upstreamPort, stopUpstream := startTestDNSUpstream(t)
	defer stopUpstream()
	listenPort := freeCoreDNSTestPort(t)
	service, err := startCoreDNS("127.0.0.1", listenPort, []string{"anthropic.com", "claude.ai"},
		[]string{net.JoinHostPort("127.0.0.1", strconv.Itoa(upstreamPort))})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Stop()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(listenPort))
	assertDNSAnswer(t, "udp", addr, "api.anthropic.com.", dns.TypeA, "127.0.0.1")
	assertDNSAnswer(t, "tcp", addr, "a.b.claude.ai.", dns.TypeAAAA, "::1")
	assertDNSAnswer(t, "udp", addr, "anthropic.com.evil.test.", dns.TypeA, "203.0.113.7")
	assertDNSAnswer(t, "tcp", addr, "example.net.", dns.TypeA, "203.0.113.7")

	if err := service.Stop(); err != nil {
		t.Fatal(err)
	}
	c := &dns.Client{Net: "udp", Timeout: 200 * time.Millisecond}
	q := new(dns.Msg)
	q.SetQuestion("api.anthropic.com.", dns.TypeA)
	if _, _, err := c.Exchange(q, addr); err == nil {
		t.Fatal("CoreDNS still answered after Stop")
	}
}

func TestDomainListPublishesInterceptAndUpstreamsAtomically(t *testing.T) {
	old := domainListCache
	domainListCache = &sniDomainList{}
	defer func() { domainListCache = old }()

	body := []byte(`{
        "pinned_hosts":["API.Anthropic.com","api.anthropic.com"],
        "intercept":["Anthropic.com","claude.ai"],
        "upstream":["223.5.5.5","[2001:db8::53]:5353"]
    }`)
	if !updateDomainListCache(body, "etag-one") {
		t.Fatal("first valid OSS policy should publish as changed")
	}
	cfg := &SNIConfig{Intercept: []string{"fallback.test"}, Upstream: []string{"1.1.1.1"}}
	if got, want := effectiveIntercept(cfg), []string{"anthropic.com", "claude.ai"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("intercept=%v want=%v", got, want)
	}
	if got, want := effectiveUpstreams(cfg), []string{"223.5.5.5:53", "[2001:db8::53]:5353"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstreams=%v want=%v", got, want)
	}
	if got := domainListVersion(); got != "etag-one" {
		t.Fatalf("etag=%q", got)
	}

	// A new ETag with identical runtime policy updates observability without a
	// disruptive re-arm. A malformed document must preserve that good state.
	if updateDomainListCache(body, "etag-two") {
		t.Fatal("identical policy should not request a re-arm")
	}
	if got := domainListVersion(); got != "etag-two" {
		t.Fatalf("new ETag not published: %q", got)
	}
	if updateDomainListCache([]byte(`{"pinned_hosts":[],"intercept":[],"upstream":[]}`), "bad") {
		t.Fatal("invalid policy should not publish")
	}
	if got := domainListVersion(); got != "etag-two" {
		t.Fatalf("invalid policy replaced last good state: %q", got)
	}
}

func startTestDNSUpstream(t *testing.T) (int, func()) {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := tcp.Addr().(*net.TCPAddr).Port
	udp, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		tcp.Close()
		t.Fatal(err)
	}
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(req)
		if len(req.Question) > 0 && req.Question[0].Qtype == dns.TypeA {
			resp.Answer = append(resp.Answer, &dns.A{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}, A: net.ParseIP("203.0.113.7")})
		}
		_ = w.WriteMsg(resp)
	})
	udpServer := &dns.Server{PacketConn: udp, Handler: mux}
	tcpServer := &dns.Server{Listener: tcp, Handler: mux}
	go func() { _ = udpServer.ActivateAndServe() }()
	go func() { _ = tcpServer.ActivateAndServe() }()
	return port, func() {
		_ = udpServer.Shutdown()
		_ = tcpServer.Shutdown()
	}
}

func freeCoreDNSTestPort(t *testing.T) int {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := tcp.Addr().(*net.TCPAddr).Port
	udp, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		tcp.Close()
		t.Fatal(err)
	}
	tcp.Close()
	udp.Close()
	return port
}

func assertDNSAnswer(t *testing.T, network, server, name string, qtype uint16, want string) {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(name, qtype)
	c := &dns.Client{Net: network, Timeout: 2 * time.Second}
	resp, _, err := c.Exchange(q, server)
	if err != nil {
		t.Fatalf("%s %s/%d: %v", network, name, qtype, err)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("%s %s/%d: answers=%v", network, name, qtype, resp.Answer)
	}
	var got string
	switch rr := resp.Answer[0].(type) {
	case *dns.A:
		got = rr.A.String()
	case *dns.AAAA:
		got = rr.AAAA.String()
	}
	if got != want {
		t.Fatalf("%s %s/%d: got %q want %q", network, name, qtype, got, want)
	}
}

func TestSameSNI(t *testing.T) {
	a := &SNIConfig{Enabled: true, Account: "x@y.com", Exit: SNIExit{"100.64.0.16", 443}, Intercept: []string{"anthropic.com"}, Upstream: []string{"223.5.5.5"}}
	b := &SNIConfig{Enabled: true, Account: "x@y.com", Exit: SNIExit{"100.64.0.16", 443}, Intercept: []string{"anthropic.com"}, Upstream: []string{"223.5.5.5"}}
	if !sameSNI(a, b) {
		t.Fatal("相同配置应判等")
	}
	if sameSNI(a, nil) || sameSNI(nil, b) {
		t.Fatal("一侧 nil 应判不等")
	}
	if sameSNI(nil, nil) == false {
		t.Fatal("双 nil 应判等")
	}
	b.Exit.Host = "100.64.0.17"
	if sameSNI(a, b) {
		t.Fatal("exit 变了应判不等")
	}
}
