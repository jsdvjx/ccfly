package mesh

// sni_test.go — 客户端 SNI arm 纯逻辑回归:DNS 问题解析、intercept 匹配、loopback 应答合成、sameSNI 判等。
// (DNS/:443 监听 + resolv.conf 需 root,不在单测内起;此处只验协议层逻辑正确。)

import (
	"encoding/binary"
	"testing"
)

// buildQuery 构造一个最小 DNS 查询(单问题,给定 qname + qtype)。
func buildQuery(name string, qtype uint16) []byte {
	msg := make([]byte, 12)
	binary.BigEndian.PutUint16(msg[0:2], 0x1234) // id
	binary.BigEndian.PutUint16(msg[4:6], 1)      // QDCOUNT=1
	for _, label := range splitLabels(name) {
		msg = append(msg, byte(len(label)))
		msg = append(msg, []byte(label)...)
	}
	msg = append(msg, 0)                                    // root
	msg = append(msg, byte(qtype>>8), byte(qtype), 0, 0x01) // QTYPE + QCLASS IN
	return msg
}

func splitLabels(name string) []string {
	out := []string{}
	cur := ""
	for _, r := range name {
		if r == '.' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func TestParseDNSQuestion(t *testing.T) {
	q := buildQuery("api.anthropic.com", 1)
	name, qtype, ok := parseDNSQuestion(q)
	if !ok || name != "api.anthropic.com" || qtype != 1 {
		t.Fatalf("解析应得 api.anthropic.com/A,得 %q/%d ok=%v", name, qtype, ok)
	}
	// AAAA + 大写归一化。
	q2 := buildQuery("Console.Anthropic.COM", 28)
	name2, qtype2, _ := parseDNSQuestion(q2)
	if name2 != "console.anthropic.com" || qtype2 != 28 {
		t.Fatalf("应小写化并识别 AAAA,得 %q/%d", name2, qtype2)
	}
	// 截断/畸形 → ok=false。
	if _, _, ok := parseDNSQuestion([]byte{0, 0, 0}); ok {
		t.Fatal("畸形查询应 ok=false")
	}
}

func TestMatchesIntercept(t *testing.T) {
	ic := []string{"anthropic.com", "claude.ai", "statsig.com"}
	for _, name := range []string{"anthropic.com", "api.anthropic.com", "a.b.c.anthropic.com", "claude.ai", "x.statsig.com"} {
		if !matchesIntercept(name, ic) {
			t.Fatalf("%q 应命中 intercept", name)
		}
	}
	for _, name := range []string{"anthropic.com.evil.com", "notanthropic.com", "google.com", "claude.ai.co"} {
		if matchesIntercept(name, ic) {
			t.Fatalf("%q 不应命中 intercept", name)
		}
	}
}

func TestBuildLoopbackAnswer(t *testing.T) {
	// A → 127.0.0.1,响应可解析、ANCOUNT=1、QR 置位、rdata 正确。
	q := buildQuery("api.anthropic.com", 1)
	resp := buildLoopbackAnswer(q, 1)
	if resp == nil {
		t.Fatal("A 应合成应答")
	}
	if resp[2]&0x80 == 0 {
		t.Fatal("QR 位应置 1")
	}
	if binary.BigEndian.Uint16(resp[6:8]) != 1 {
		t.Fatal("ANCOUNT 应为 1")
	}
	// 应答尾部 rdata 应为 127.0.0.1。
	if n := len(resp); n < 4 || resp[n-4] != 127 || resp[n-1] != 1 {
		t.Fatalf("A rdata 应为 127.0.0.1,得尾 %v", resp[len(resp)-4:])
	}
	// 问题段仍能被解析回来(应答复用了原 header+question)。
	if name, qtype, ok := parseDNSQuestion(resp); !ok || name != "api.anthropic.com" || qtype != 1 {
		t.Fatalf("应答应保留原问题,得 %q/%d ok=%v", name, qtype, ok)
	}
	// AAAA → ::1(16 字节,末位 1)。
	respv6 := buildLoopbackAnswer(buildQuery("api.anthropic.com", 28), 28)
	if n := len(respv6); n < 16 || respv6[n-1] != 1 || respv6[n-16] != 0 {
		t.Fatalf("AAAA rdata 应为 ::1")
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
