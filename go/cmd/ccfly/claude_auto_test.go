package main

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jsdvjx/ccfly/go/internal/mesh"
)

func TestClaudeAutoHandoffRoundTripAndOneTimeConsumption(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Unix(1_700_000_000, 0)
	tgt := mesh.LoginTarget{Host: "cc.hn", Scheme: "https", DeviceID: "device-123"}
	link, err := createClaudeAutoHandoff(tgt, "Owner@Example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(u.Query().Get("claude_auto"))
	if err != nil {
		t.Fatal(err)
	}
	var payload claudeAutoHandoff
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.DeviceID != tgt.DeviceID || payload.Email != "owner@example.com" || payload.ExpiresAt != now.Add(30*time.Minute).Unix() {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	path, err := validateClaudeAutoHandoff(tgt, payload.Email, payload.Handoff, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := validateClaudeAutoHandoff(tgt, payload.Email, payload.Handoff, now.Add(time.Minute)); err == nil {
		t.Fatal("consumed handoff should not validate again")
	}
}

func TestClaudeAutoHandoffRejectsWrongEmailAndExpiry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Unix(1_700_000_000, 0)
	tgt := mesh.LoginTarget{Host: "cc.hn", DeviceID: "device-123"}
	link, err := createClaudeAutoHandoff(tgt, "owner@example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	raw, _ := base64.RawURLEncoding.DecodeString(u.Query().Get("claude_auto"))
	var payload claudeAutoHandoff
	_ = json.Unmarshal(raw, &payload)
	if _, err := validateClaudeAutoHandoff(tgt, "other@example.com", payload.Handoff, now); err == nil {
		t.Fatal("wrong email should fail")
	}
	if _, err := validateClaudeAutoHandoff(tgt, payload.Email, payload.Handoff, now.Add(31*time.Minute)); err == nil {
		t.Fatal("expired handoff should fail")
	}
}
