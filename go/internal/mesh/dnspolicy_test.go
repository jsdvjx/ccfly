package mesh

// dnspolicy_test.go — DNS 策略服务:OSS 文档校验发布、兜底启动、热重载。

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestDNSPolicyPublishValidation(t *testing.T) {
	svc := newDNSPolicyService("127.0.0.1", 15353)

	body := []byte(`{"intercept":["Anthropic.com","claude.ai"],"upstream":["223.5.5.5","[2001:db8::53]:5353"]}`)
	if !svc.publish(body, "etag-one") {
		t.Fatal("first valid OSS policy should publish as changed")
	}
	if got, want := svc.Domains(), []string{"anthropic.com", "claude.ai"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("domains=%v want=%v", got, want)
	}
	if got := svc.Version(); got != "etag-one" {
		t.Fatalf("etag=%q", got)
	}

	// 同策略新 ETag:运行策略不变(不触发 reload),但版本观测要更新。
	if svc.publish(body, "etag-two") {
		t.Fatal("identical policy should not report change")
	}
	if got := svc.Version(); got != "etag-two" {
		t.Fatalf("new ETag not published: %q", got)
	}
	// 坏文档/空清单:保留上一份好策略。
	for _, bad := range [][]byte{
		[]byte(`{"intercept":[],"upstream":[]}`),
		[]byte(`not json`),
		[]byte(`{"intercept":["ok.test"],"upstream":["not-an-ip"]}`),
	} {
		if svc.publish(bad, "bad") {
			t.Fatalf("invalid policy should not publish: %s", bad)
		}
	}
	if got := svc.Version(); got != "etag-two" {
		t.Fatalf("invalid policy replaced last good state: %q", got)
	}
}

func TestDNSPolicyStartsOnFallbackWhenOSSDown(t *testing.T) {
	upstreamPort, stopUpstream := startTestDNSUpstream(t)
	defer stopUpstream()
	listenPort := freeCoreDNSTestPort(t)

	svc := newDNSPolicyService("127.0.0.1", listenPort)
	svc.fetchURL = "http://127.0.0.1:1/unreachable" // 首拉必失败
	svc.upstreams = []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(upstreamPort))}
	if err := svc.start(); err != nil {
		t.Fatal(err)
	}
	defer svc.Stop()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(listenPort))
	assertDNSAnswer(t, "udp", addr, "api.anthropic.com.", dns.TypeA, "127.0.0.1")
	if got := svc.Version(); got != "" {
		t.Fatalf("fallback policy should carry no OSS version, got %q", got)
	}
}

func TestDNSPolicyHotReloadsOnPolicyChange(t *testing.T) {
	upstreamPort, stopUpstream := startTestDNSUpstream(t)
	defer stopUpstream()
	listenPort := freeCoreDNSTestPort(t)

	// mock OSS:policy 可热换;ETag 随内容变化。
	var doc atomic.Value
	doc.Store(`{"intercept":["anthropic.com"],"upstream":["127.0.0.1:` + strconv.Itoa(upstreamPort) + `"]}`)
	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := doc.Load().(string)
		w.Header().Set("ETag", fmt.Sprintf("\"%x\"", len(body)))
		_, _ = w.Write([]byte(body))
	}))
	defer oss.Close()

	svc := newDNSPolicyService("127.0.0.1", listenPort)
	svc.fetchURL = oss.URL
	svc.poll = 30 * time.Millisecond
	reloaded := make(chan []string, 4)
	svc.onChange = func(domains []string) { reloaded <- domains }
	if err := svc.start(); err != nil {
		t.Fatal(err)
	}
	defer svc.Stop()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(listenPort))
	assertDNSAnswer(t, "udp", addr, "api.anthropic.com.", dns.TypeA, "127.0.0.1")
	// 未列入清单的域 → 转上游(不拦截)。
	assertDNSAnswer(t, "udp", addr, "example.net.", dns.TypeA, "203.0.113.7")

	// OSS 策略加入 example.net → 一个轮询周期内热重载,该域开始被拦截。
	doc.Store(`{"intercept":["anthropic.com","example.net"],"upstream":["127.0.0.1:` + strconv.Itoa(upstreamPort) + `"]}`)
	select {
	case domains := <-reloaded:
		if !reflect.DeepEqual(domains, []string{"anthropic.com", "example.net"}) {
			t.Fatalf("onChange domains=%v", domains)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no reload within a poll cycle")
	}
	assertDNSAnswer(t, "udp", addr, "example.net.", dns.TypeA, "127.0.0.1")
}
