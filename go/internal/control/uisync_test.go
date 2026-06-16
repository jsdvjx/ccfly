package control

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestSemverGt(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.1", "0.1.0", true},
		{"0.1.0", "0.1.0", false},
		{"0.1.0", "0.1.1", false}, // 从不降级
		{"1.0.0", "0.9.9", true},
		{"0.2.0", "0.1.9", true},
		{"v0.1.1", "0.1.0", true},     // 容忍 v 前缀
		{"0.1.1-beta", "0.1.0", true}, // 忽略 pre-release
		{"garbage", "0.1.0", false},   // 解析失败 → 保守 false
		{"0.1.0", "garbage", false},
	}
	for _, c := range cases {
		if got := semverGt(c.a, c.b); got != c.want {
			t.Errorf("semverGt(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func sri(data []byte) string {
	sum := sha512.Sum512(data)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

func TestVerifyIntegrity(t *testing.T) {
	data := []byte("hello ccfly ui")
	if err := verifyIntegrity(data, sri(data)); err != nil {
		t.Errorf("valid integrity rejected: %v", err)
	}
	if err := verifyIntegrity(append([]byte{}, append(data, 'x')...), sri(data)); err == nil {
		t.Error("tampered data accepted")
	}
	if err := verifyIntegrity(data, ""); err == nil {
		t.Error("missing integrity accepted")
	}
	if err := verifyIntegrity(data, "sha1-abc"); err == nil {
		t.Error("non-sha512 integrity accepted")
	}
}

func makeTgz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractDistTar(t *testing.T) {
	dest := t.TempDir()
	root := filepath.Join(dest, "out")
	tgz := makeTgz(t, map[string]string{
		"package/package.json":       `{"name":"ccfly-webdist"}`, // 非 dist → 忽略
		"package/dist/index.html":    "<html>",
		"package/dist/assets/a.js":   "console.log(1)",
		"package/dist/../escape.txt": "evil", // 穿越 → 必须不落地
		"other/x":                    "nope", // 非 package/dist → 忽略
	})
	if err := extractDistTar(tgz, root); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "index.html")); err != nil || string(b) != "<html>" {
		t.Errorf("index.html missing/wrong: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "assets", "a.js")); err != nil || string(b) != "console.log(1)" {
		t.Errorf("assets/a.js missing/wrong: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "escape.txt")); err == nil {
		t.Error("path traversal escaped destRoot")
	}
	if _, err := os.Stat(filepath.Join(root, "package.json")); err == nil {
		t.Error("non-dist file extracted")
	}
}
