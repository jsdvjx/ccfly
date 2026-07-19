package mesh

// egresscheck.go is the active environment check for the SNI egress path.
// It deliberately enters through the same local 127.0.0.1:443 listener used by
// application traffic. The target byway listener applies its live source-IP
// route, echoes a fresh nonce, and reports the selected node/account identity.
// A successful check therefore proves more than reachability: this device's
// production path arrived at the configured node and selected account exit.

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const (
	egressProbeMagic = "CCFLY-EGRESS-PROBE/1 "
	egressProbeAddr  = "127.0.0.1:443"
)

type egressIdentity struct {
	NodeID       string
	ExitID       string
	Identity     string
	BoundEgress4 string
}

// egressTargetError 标记「连接路由通,但对端身份不符」:nonce 应答合法(确实到了一个 byway
// 探测处理器),只是节点/账号出口/绑定出口不是期望的那个 → target_mismatch。其余错误(拨号、
// 读、解析、nonce 过期)都是路由级失败(loopback_route 不通)。检测分类靠 errors.As 区分两者。
type egressTargetError struct{ msg string }

func (e *egressTargetError) Error() string { return e.msg }

type egressProbeWire struct {
	Version      int    `json:"version"`
	Nonce        string `json:"nonce"`
	NodeID       string `json:"node_id"`
	ExitID       string `json:"exit_id"`
	Identity     string `json:"identity"`
	BoundEgress4 string `json:"bound_egress_ipv4"`
}

func probeEgressIdentity(expectedNode, expectedIdentity string) (egressIdentity, error) {
	return probeEgressIdentityAt(egressProbeAddr, expectedNode, expectedIdentity)
}

func probeEgressIdentityAt(addr, expectedNode, expectedIdentity string) (egressIdentity, error) {
	var out egressIdentity
	expectedNode = strings.TrimSpace(expectedNode)
	expectedIdentity = strings.TrimSpace(expectedIdentity)
	if expectedNode == "" || expectedIdentity == "" {
		return out, fmt.Errorf("egress identity check has no expected node/account")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return out, fmt.Errorf("egress identity nonce: %w", err)
	}
	nonce := hex.EncodeToString(random)

	c, err := net.DialTimeout("tcp", addr, sniProbeTimeout)
	if err != nil {
		return out, fmt.Errorf("egress identity dial: %w", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(sniProbeTimeout))
	if _, err := fmt.Fprintf(c, "%s%s\n", egressProbeMagic, nonce); err != nil {
		return out, fmt.Errorf("egress identity write: %w", err)
	}

	line, err := bufio.NewReaderSize(io.LimitReader(c, 4097), 4096).ReadBytes('\n')
	if err != nil {
		return out, fmt.Errorf("egress identity read: %w", err)
	}
	if len(line) > 4096 {
		return out, fmt.Errorf("egress identity response too large")
	}
	var wire egressProbeWire
	if err := json.Unmarshal(line, &wire); err != nil {
		return out, fmt.Errorf("egress identity response: %w", err)
	}
	out = egressIdentity{
		NodeID: strings.TrimSpace(wire.NodeID), ExitID: strings.TrimSpace(wire.ExitID),
		Identity: strings.TrimSpace(wire.Identity), BoundEgress4: strings.TrimSpace(wire.BoundEgress4),
	}
	if wire.Version != 1 || wire.Nonce != nonce {
		return out, fmt.Errorf("egress identity response is stale or incompatible")
	}
	if !sameNodeIdentity(out.NodeID, expectedNode) {
		return out, &egressTargetError{fmt.Sprintf("egress target node mismatch: got %q, expected %q", out.NodeID, expectedNode)}
	}
	if !strings.EqualFold(out.Identity, expectedIdentity) {
		return out, &egressTargetError{fmt.Sprintf("egress target account mismatch: got %q, expected %q", out.Identity, expectedIdentity)}
	}
	if out.ExitID == "" {
		return out, &egressTargetError{"egress target returned no exit id"}
	}
	if ip := net.ParseIP(out.BoundEgress4); ip == nil || ip.To4() == nil {
		return out, &egressTargetError{fmt.Sprintf("egress target returned invalid bound IPv4 %q", out.BoundEgress4)}
	}
	return out, nil
}

func sameNodeIdentity(got, expected string) bool {
	if gotIP, expectedIP := net.ParseIP(got), net.ParseIP(expected); gotIP != nil && expectedIP != nil {
		return gotIP.Equal(expectedIP)
	}
	return got != "" && strings.EqualFold(got, expected)
}
