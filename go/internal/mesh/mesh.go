// Package mesh implements the device side of ccfly's overlay: `ccfly connect
// <host>/<code>` enrolls the device with a ccfly-cloud control plane and holds a
// WebSocket tunnel (`/mesh`) open.
//
// Increment 1 (this file): X25519 key generation, the `/connect` enrollment
// handshake, local state persistence, and a self-healing /mesh tunnel that keeps
// the device marked online. Increment 2 will frame actual WireGuard packets over
// this tunnel (custom conn.Bind + netstack) so the cloud can reach the device's
// local ccfly control API over the overlay.
package mesh

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// State is the persisted per-host connection state (~/.ccfly/conn-<host>.json).
type State struct {
	Host           string `json:"host"`
	Scheme         string `json:"scheme"` // http | https (control plane)
	DeviceID       string `json:"device_id"`
	Name           string `json:"name"`
	Owner          string `json:"owner"`
	PrivateKey     string `json:"private_key"` // device WG private key (base64)
	PublicKey      string `json:"public_key"`
	OverlayIP      string `json:"overlay_ip"`
	OverlayCIDR    string `json:"overlay_cidr"`
	CloudPublicKey string `json:"cloud_public_key"`
	CloudOverlayIP string `json:"cloud_overlay_ip"`
	MeshURL        string `json:"mesh_url"`
	MeshToken      string `json:"mesh_token"`
	KeepaliveSec   int    `json:"keepalive_sec"`
}

// connectResp mirrors ccfly-cloud's POST /connect response.
type connectResp struct {
	DeviceID       string `json:"device_id"`
	Name           string `json:"name"`
	Owner          string `json:"owner"`
	OverlayIP      string `json:"overlay_ip"`
	OverlayCIDR    string `json:"overlay_cidr"`
	CloudPublicKey string `json:"cloud_public_key"`
	CloudOverlayIP string `json:"cloud_overlay_ip"`
	MeshURL        string `json:"mesh_url"`
	MeshToken      string `json:"mesh_token"`
	KeepaliveSec   int    `json:"keepalive_sec"`
}

// Connect enrolls the device against <host>/<code> and holds the mesh tunnel
// open until ctx is cancelled. target forms: "host/code", "https://host/code",
// "http://host/code" (loopback hosts default to http).
func Connect(ctx context.Context, target string) error {
	scheme, host, code, err := parseTarget(target)
	if err != nil {
		return err
	}

	// Reuse this host's key identity if we've connected before; else generate.
	st, _ := loadState(host)
	if st == nil || st.PrivateKey == "" {
		priv, pub, err := newKeypair()
		if err != nil {
			return fmt.Errorf("generate key: %w", err)
		}
		st = &State{PrivateKey: priv, PublicKey: pub}
	}
	st.Host, st.Scheme = host, scheme

	resp, err := enroll(ctx, scheme, host, code, st.PublicKey)
	if err != nil {
		return err
	}
	st.DeviceID = resp.DeviceID
	st.Name = resp.Name
	st.Owner = resp.Owner
	st.OverlayIP = resp.OverlayIP
	st.OverlayCIDR = resp.OverlayCIDR
	st.CloudPublicKey = resp.CloudPublicKey
	st.CloudOverlayIP = resp.CloudOverlayIP
	st.MeshURL = resp.MeshURL
	st.MeshToken = resp.MeshToken
	st.KeepaliveSec = resp.KeepaliveSec
	if err := saveState(st); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	log.Printf("ccfly: enrolled as %q (device %s) overlay %s on %s", st.Name, st.DeviceID, st.OverlayIP, host)
	fmt.Printf("✓ connected to %s — device %q, overlay IP %s\n  holding mesh tunnel (Ctrl-C to stop)\n", host, st.Name, st.OverlayIP)

	return runTunnel(ctx, st)
}

// ── enrollment ──

func enroll(ctx context.Context, scheme, host, code, pubkey string) (*connectResp, error) {
	hostname, _ := os.Hostname()
	body, _ := json.Marshal(map[string]string{
		"code":       code,
		"public_key": pubkey,
		"hostname":   hostname,
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
	})
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodPost, scheme+"://"+host+"/connect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", host, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		return nil, fmt.Errorf("enrollment rejected (%d): %s", resp.StatusCode, e.Error)
	}
	var cr connectResp
	if err := json.Unmarshal(data, &cr); err != nil {
		return nil, fmt.Errorf("bad enrollment response: %w", err)
	}
	if cr.MeshURL == "" || cr.MeshToken == "" {
		return nil, errors.New("enrollment response missing mesh url/token")
	}
	return &cr, nil
}

// ── tunnel: dial /mesh, keepalive, self-heal ──

func runTunnel(ctx context.Context, st *State) error {
	backoff := time.Second
	for ctx.Err() == nil {
		err := dialOnce(ctx, st)
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("ccfly: mesh disconnected: %v — retrying in %s", err, backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	return nil
}

func dialOnce(ctx context.Context, st *State) error {
	u := st.MeshURL + "?token=" + url.QueryEscape(st.MeshToken)
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	c, _, err := websocket.Dial(dctx, u, nil)
	cancel()
	if err != nil {
		return err
	}
	defer c.CloseNow()
	log.Printf("ccfly: mesh up (overlay %s via %s)", st.OverlayIP, st.Host)

	keepalive := time.Duration(st.KeepaliveSec) * time.Second
	if keepalive <= 0 {
		keepalive = 25 * time.Second
	}
	go func() {
		t := time.NewTicker(keepalive)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := c.Ping(pctx)
				cancel()
				if err != nil {
					return
				}
			}
		}
	}()

	// Increment 2: bring up the userspace WireGuard device whose transport IS
	// this WebSocket. If it fails to come up we fall back to merely draining
	// inbound frames so the tunnel (and online status) is unaffected, then let
	// the outer loop reconnect.
	sess, err := bringUpWG(ctx, st, c)
	if err != nil {
		log.Printf("ccfly: wireguard datapath unavailable (tunnel still up): %v", err)
		for {
			if _, _, rerr := c.Read(ctx); rerr != nil {
				return rerr
			}
		}
	}
	// pump owns the WS read side; it blocks until the conn drops (or ctx ends).
	// Tear the device down before returning so the outer loop can rebuild a
	// fresh device on the next dial.
	defer func() {
		sess.bind.detach(c)
		sess.close()
	}()
	return sess.bind.pump(ctx)
}

// ── target parsing ──

func parseTarget(t string) (scheme, host, code string, err error) {
	explicit := false
	scheme = "https"
	if i := strings.Index(t, "://"); i >= 0 {
		scheme = t[:i]
		t = t[i+3:]
		explicit = true
	}
	slash := strings.Index(t, "/")
	if slash < 0 {
		return "", "", "", errors.New(`expected "<host>/<code>" (e.g. ccfly connect example.com/Ab12Cd34Ef)`)
	}
	host = t[:slash]
	code = strings.Trim(t[slash+1:], "/")
	if host == "" || code == "" {
		return "", "", "", errors.New(`expected "<host>/<code>"`)
	}
	if !explicit && isLoopback(host) {
		scheme = "http"
	}
	return scheme, host, code, nil
}

func isLoopback(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h, "::") {
		h = h[:i] // strip :port
	}
	return h == "localhost" || strings.HasPrefix(h, "127.") || h == "[::1]" || h == "::1"
}

// ── X25519 keys (WireGuard-compatible, base64-std) ──

func newKeypair() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()),
		base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// ── state persistence (~/.ccfly/conn-<host>.json) ──

func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".ccfly")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func statePath(host string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	safe := strings.NewReplacer(":", "_", "/", "_").Replace(host)
	return filepath.Join(dir, "conn-"+safe+".json"), nil
}

func loadState(host string) (*State, error) {
	p, err := statePath(host)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func saveState(st *State) error {
	p, err := statePath(st.Host)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}
