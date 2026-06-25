package control

// uisync.go — 把内嵌 web UI 从「随二进制打包」改为「运行时从 npm 同步」。
//
// 二进制内嵌当前版本做兜底(首启 / 离线可用);后台周期查 npm 上 ccfly-webdist 的 latest,
// 比内嵌(或已缓存的当前版本)新就拉 tarball、按 npm 的 SRI 校验、解压缓存到
// ~/.ccfly/webcache/<ver>/,并切到该版本提供。失败 / 离线 / 不更新 → 静默留在当前(从不降级)。
// 改 UI = 发 npm,不必重打节点二进制。static.go 每请求读 servedUIDir() 决定服务源。
//
// 信任边界:只信 registry.npmjs.org over HTTPS + 发布时的 integrity 校验(= 二进制分发同一个
// npm 账号)。不依赖 mesh / Hub / token —— 直接打公共 registry。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jsdvjx/ccfly/go/internal/profile"
)

const (
	uiPackage       = "ccfly-webdist"
	uiRegistry      = "https://registry.npmjs.org/"
	uiCheckInterval = time.Hour
	uiMaxTarball    = 64 << 20 // 64 MiB 防御性上限
)

var (
	uiMu      sync.RWMutex
	uiCurrent string // 当前提供的缓存 dist 目录;"" = 用内嵌兜底
	uiOnce    sync.Once
)

// servedUIDir 返回当前应提供的缓存目录(空 = 内嵌)。static.go 每请求读它。
func servedUIDir() string { uiMu.RLock(); defer uiMu.RUnlock(); return uiCurrent }

func setServedUIDir(d string) { uiMu.Lock(); uiCurrent = d; uiMu.Unlock() }

// embeddedUIVersion 读内嵌 webdist/VERSION(build-web.sh 写入 = ccfly-webdist 包版本);缺失 → 0.0.0。
func embeddedUIVersion() string {
	data, err := fs.ReadFile(webdistFS, "webdist/VERSION")
	if err != nil {
		return "0.0.0"
	}
	return strings.TrimSpace(string(data))
}

func uiCacheRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "ccfly-webcache")
	}
	return filepath.Join(home, ".ccfly", "webcache")
}

// StartUISync 一次性启动后台同步:先采纳已缓存的更新版本(离线友好),再起周期巡检。
// 由 staticHandler 调用(serve / mesh 两条路都经它),用 sync.Once 保证只起一次。
func StartUISync(ctx context.Context) {
	if !profile.Current().UISync {
		return // 受限模式:保持内嵌 UI,不外联 npm registry 拉新
	}
	uiOnce.Do(func() {
		if dir, ver := bestCachedUI(); dir != "" && semverGt(ver, embeddedUIVersion()) {
			setServedUIDir(dir)
		}
		go uiSyncLoop(ctx)
	})
}

func uiSyncLoop(ctx context.Context) {
	syncUIOnce(ctx)
	t := time.NewTicker(uiCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			syncUIOnce(ctx)
		}
	}
}

// currentServedVersion 返回此刻实际提供的版本(缓存目录的 VERSION,否则内嵌)。
func currentServedVersion() string {
	if dir := servedUIDir(); dir != "" {
		if v := readDirVersion(dir); v != "" {
			return v
		}
	}
	return embeddedUIVersion()
}

func syncUIOnce(ctx context.Context) {
	latest, tarURL, integrity, ok := fetchLatestUIMeta(ctx)
	if !ok {
		return
	}
	if !semverGt(latest, currentServedVersion()) {
		return // npm 不比当前新 → 保持,从不降级
	}
	dir := filepath.Join(uiCacheRoot(), latest)
	if isUsableUIDir(dir) { // 之前已拉过
		setServedUIDir(dir)
		return
	}
	prev := currentServedVersion()
	if err := downloadUI(ctx, tarURL, integrity, latest, dir); err != nil {
		log.Printf("ccfly: ui sync %s failed: %v", latest, err)
		return
	}
	setServedUIDir(dir)
	log.Printf("ccfly: ui synced %s -> %s", prev, latest)
}

// fetchLatestUIMeta 查 npm registry 拿 latest 版本 + 其 tarball 地址 + integrity。
func fetchLatestUIMeta(ctx context.Context) (version, tarball, integrity string, ok bool) {
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, "GET", uiRegistry+uiPackage, nil)
	if err != nil {
		return "", "", "", false
	}
	// 精简元数据格式(只含 install 所需字段),省带宽。
	req.Header.Set("Accept", "application/vnd.npm.install-v1+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", false
	}
	var doc struct {
		DistTags map[string]string `json:"dist-tags"`
		Versions map[string]struct {
			Dist struct {
				Tarball   string `json:"tarball"`
				Integrity string `json:"integrity"`
			} `json:"dist"`
		} `json:"versions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&doc); err != nil {
		return "", "", "", false
	}
	latest := doc.DistTags["latest"]
	if latest == "" {
		return "", "", "", false
	}
	v, has := doc.Versions[latest]
	if !has || v.Dist.Tarball == "" {
		return "", "", "", false
	}
	return latest, v.Dist.Tarball, v.Dist.Integrity, true
}

// downloadUI 下载 tarball、验 integrity(sha512)、解压 package/dist/* 到 dir(原子:temp + rename)。
func downloadUI(ctx context.Context, tarURL, integrity, version, dir string) error {
	rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, "GET", tarURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tarball HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, uiMaxTarball))
	if err != nil {
		return err
	}
	if err := verifyIntegrity(raw, integrity); err != nil {
		return err
	}
	if err := os.MkdirAll(uiCacheRoot(), 0o755); err != nil {
		return err
	}
	tmp := dir + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := extractDistTar(raw, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if !isUsableUIDir(tmp) {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("no index.html in package dist")
	}
	if err := os.WriteFile(filepath.Join(tmp, "VERSION"), []byte(version+"\n"), 0o644); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	_ = os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	return nil
}

// verifyIntegrity 验 npm 的 SRI(sha512-<base64>);无 / 不支持 integrity → 拒(不接受未校验内容)。
func verifyIntegrity(data []byte, integrity string) error {
	integrity = strings.TrimSpace(integrity)
	if !strings.HasPrefix(integrity, "sha512-") {
		return fmt.Errorf("missing/unsupported integrity")
	}
	want, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(integrity, "sha512-"))
	if err != nil {
		return fmt.Errorf("bad integrity: %w", err)
	}
	sum := sha512.Sum512(data)
	if subtle.ConstantTimeCompare(sum[:], want) != 1 {
		return fmt.Errorf("integrity mismatch")
	}
	return nil
}

// extractDistTar 把 npm tarball(.tgz)里 package/dist/ 下的文件解到 destRoot(扁平到 dist 根)。
// 仅取常规文件;路径做穿越防护(拒绝 ..、确保落点在 destRoot 内)。
func extractDistTar(tgz []byte, destRoot string) error {
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	const prefix = "package/dist/"
	cleanRoot := filepath.Clean(destRoot)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.ToSlash(hdr.Name)
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" || strings.Contains(rel, "..") {
			continue
		}
		out := filepath.Join(cleanRoot, filepath.FromSlash(rel))
		if out != cleanRoot && !strings.HasPrefix(out, cleanRoot+string(os.PathSeparator)) {
			continue // 双保险:落点必须在 destRoot 内
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, io.LimitReader(tr, uiMaxTarball)); err != nil {
			f.Close()
			return err
		}
		f.Close()
	}
	return nil
}

// bestCachedUI 扫缓存根,返回 index.html 存在的最高版本目录(离线时也能用上次拉到的)。
func bestCachedUI() (dir, version string) {
	entries, err := os.ReadDir(uiCacheRoot())
	if err != nil {
		return "", ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d := filepath.Join(uiCacheRoot(), e.Name())
		if !isUsableUIDir(d) {
			continue
		}
		v := readDirVersion(d)
		if v == "" {
			v = e.Name()
		}
		if version == "" || semverGt(v, version) {
			dir, version = d, v
		}
	}
	return dir, version
}

func isUsableUIDir(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "index.html"))
	return err == nil && !fi.IsDir()
}

func readDirVersion(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "VERSION"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// semverGt 判 a > b(只比 X.Y.Z 数字三元组,忽略 pre-release/build;解析失败 → false,保守不升级)。
func semverGt(a, b string) bool {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	if !oka || !okb {
		return false
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parseSemver(s string) ([3]int, bool) {
	var out [3]int
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
