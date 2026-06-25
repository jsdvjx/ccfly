package main

import (
	"crypto/rand"
	"encoding/base64"
	"testing"

	"golang.org/x/crypto/nacl/box"

	"github.com/jsdvjx/ccfly/go/internal/mesh"
)

// openSealed 必须能用设备 WG 私钥打开 worker 用 NaCl sealed-box(box.SealAnonymous)封装到设备公钥的
// 凭证。WG 密钥即 Curve25519,直接当 nacl/box 密钥对用。
func TestOpenSealedRoundTrip(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-xyz","refreshToken":"r"}}`)
	ct, err := box.SealAnonymous(nil, msg, pub, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tgt := mesh.LoginTarget{
		PrivateKey: base64.StdEncoding.EncodeToString(priv[:]),
		PublicKey:  base64.StdEncoding.EncodeToString(pub[:]),
	}
	got, err := openSealed(tgt, base64.StdEncoding.EncodeToString(ct))
	if err != nil {
		t.Fatalf("openSealed: %v", err)
	}
	if string(got) != string(msg) {
		t.Fatalf("round-trip mismatch:\n got  %q\n want %q", got, msg)
	}
}

// 封装给别的公钥 → 本机私钥打不开(验证不会误把任意密文当成功)。
func TestOpenSealedWrongRecipient(t *testing.T) {
	otherPub, _, _ := box.GenerateKey(rand.Reader) // 封给它
	myPub, myPriv, _ := box.GenerateKey(rand.Reader)
	ct, _ := box.SealAnonymous(nil, []byte("secret"), otherPub, rand.Reader)
	tgt := mesh.LoginTarget{
		PrivateKey: base64.StdEncoding.EncodeToString(myPriv[:]),
		PublicKey:  base64.StdEncoding.EncodeToString(myPub[:]),
	}
	if _, err := openSealed(tgt, base64.StdEncoding.EncodeToString(ct)); err == nil {
		t.Fatal("expected open to fail for a box sealed to a different recipient")
	}
}

// 坏密钥 / 坏密文都应报错而非 panic。
func TestOpenSealedBadInputs(t *testing.T) {
	good := mesh.LoginTarget{
		PrivateKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
		PublicKey:  base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
	if _, err := openSealed(good, "!!!not-base64!!!"); err == nil {
		t.Fatal("expected error on bad ciphertext b64")
	}
	if _, err := openSealed(mesh.LoginTarget{PrivateKey: "short", PublicKey: good.PublicKey}, base64.StdEncoding.EncodeToString([]byte("x"))); err == nil {
		t.Fatal("expected error on bad private key")
	}
}
