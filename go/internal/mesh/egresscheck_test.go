package mesh

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func serveEgressProbeTest(t *testing.T, mutate func(*egressProbeWire)) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		line, err := bufio.NewReader(c).ReadString('\n')
		if err != nil || !strings.HasPrefix(line, egressProbeMagic) {
			return
		}
		nonce := strings.TrimSpace(strings.TrimPrefix(line, egressProbeMagic))
		resp := egressProbeWire{
			Version: 1, Nonce: nonce, NodeID: "100.64.0.16", ExitID: "eg-a",
			Identity: "a@example.com", BoundEgress4: "10.0.0.84",
		}
		if mutate != nil {
			mutate(&resp)
		}
		_ = json.NewEncoder(c).Encode(resp)
	}()
	return ln.Addr().String()
}

func TestProbeEgressIdentity(t *testing.T) {
	addr := serveEgressProbeTest(t, nil)
	got, err := probeEgressIdentityAt(addr, "100.64.0.16", "A@example.com")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if got.NodeID != "100.64.0.16" || got.ExitID != "eg-a" || got.Identity != "a@example.com" || got.BoundEgress4 != "10.0.0.84" {
		t.Fatalf("identity = %+v", got)
	}
}

func TestProbeEgressIdentityRejectsWrongTarget(t *testing.T) {
	addr := serveEgressProbeTest(t, func(resp *egressProbeWire) { resp.NodeID = "100.64.0.17" })
	got, err := probeEgressIdentityAt(addr, "100.64.0.16", "a@example.com")
	if err == nil || !strings.Contains(err.Error(), "node mismatch") {
		t.Fatalf("wrong node must fail, got=%+v err=%v", got, err)
	}
}

func TestProbeEgressIdentityRejectsWrongAccountRoute(t *testing.T) {
	addr := serveEgressProbeTest(t, func(resp *egressProbeWire) { resp.Identity = "other@example.com" })
	got, err := probeEgressIdentityAt(addr, "100.64.0.16", "a@example.com")
	if err == nil || !strings.Contains(err.Error(), "account mismatch") {
		t.Fatalf("wrong source-selected account must fail, got=%+v err=%v", got, err)
	}
}

func TestProbeEgressIdentityRejectsStaleNonce(t *testing.T) {
	addr := serveEgressProbeTest(t, func(resp *egressProbeWire) { resp.Nonce = strings.Repeat("00", 16) })
	_, err := probeEgressIdentityAt(addr, "100.64.0.16", "a@example.com")
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale nonce must fail: %v", err)
	}
}

// 身份不符必须是类型化 egressTargetError(检测层据此区分「路由通但目标错」与「路由不通」);
// 传输级失败(拒连/nonce 过期)不得是该类型。
func TestProbeEgressIdentityErrorTyping(t *testing.T) {
	var targetErr *egressTargetError

	mismatch := serveEgressProbeTest(t, func(resp *egressProbeWire) { resp.Identity = "other@example.com" })
	if _, err := probeEgressIdentityAt(mismatch, "100.64.0.16", "a@example.com"); !errors.As(err, &targetErr) {
		t.Fatalf("account mismatch 应为 egressTargetError:%v", err)
	}

	stale := serveEgressProbeTest(t, func(resp *egressProbeWire) { resp.Nonce = strings.Repeat("00", 16) })
	if _, err := probeEgressIdentityAt(stale, "100.64.0.16", "a@example.com"); err == nil || errors.As(err, &targetErr) {
		t.Fatalf("stale nonce 应为路由级错误而非 target 错误:%v", err)
	}
}
