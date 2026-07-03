package mesh

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// machineID 返回一台物理机的稳定唯一标识,用于云端去重 —— 同一台机器重装/重配对算「同一台」,
// 不再每次刷出一个新设备。优先用硬件 UUID(Linux /etc/machine-id、macOS IOPlatformUUID);
// 拿不到则回落到持久化的 ~/.ccfly/machine-id(首次生成、之后复用;重装不抹 ~/.ccfly,故同样稳定)。
// 返回值永不为空,且带来源前缀(hw:/file:)便于排查。
var reUUID = regexp.MustCompile(`[0-9A-Fa-f]{8}-?[0-9A-Fa-f]{4}-?[0-9A-Fa-f]{4}-?[0-9A-Fa-f]{4}-?[0-9A-Fa-f]{12}`)

func machineID() string {
	if id := hardwareID(); id != "" {
		return "hw:" + strings.ToLower(id)
	}
	return "file:" + persistedID()
}

func hardwareID() string {
	switch runtime.GOOS {
	case "linux":
		for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
			if b, err := os.ReadFile(p); err == nil {
				if s := strings.TrimSpace(string(b)); s != "" {
					return s
				}
			}
		}
	case "darwin":
		out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if err == nil {
			for _, ln := range strings.Split(string(out), "\n") {
				if strings.Contains(ln, "IOPlatformUUID") {
					if m := reUUID.FindString(ln); m != "" {
						return m
					}
				}
			}
		}
	case "windows":
		out, err := exec.Command("wmic", "csproduct", "get", "UUID", "/value").Output()
		if err == nil {
			for _, ln := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(strings.TrimSpace(ln), "UUID=") {
					if m := reUUID.FindString(ln); m != "" {
						return m
					}
				}
			}
		}
	}
	return ""
}

// persistedID 读/写 ~/.ccfly/machine-id(首次随机生成并落盘)。拿不到目录则退化为每次随机。
func persistedID() string {
	dir, err := stateDir() // 复用 mesh.go 的 ~/.ccfly
	if err != nil {
		return randHex(16)
	}
	p := filepath.Join(dir, "machine-id")
	if b, err := os.ReadFile(p); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	id := randHex(16)
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(p, []byte(id), 0o600)
	return id
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
