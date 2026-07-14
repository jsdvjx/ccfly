// Package tmuxbin 让 mac 用户不用自装 tmux:darwin 构建把一个「可移植 tmux」
// (libevent/ncursesw 静态链接,只依赖 libSystem;见 scripts/build-tmux-macos.sh)
// 用 go:embed 嵌进 ccfly 主二进制,运行时在系统找不到 tmux 时释放到 ~/.ccfly/bin/tmux。
//
// 为什么 embed 而不是像 Windows 那样在 npm 平台包里放散文件(psmux/tmux.exe):
// mac 的分发路径不止 npm —— `ccfly install` 会把**单个**二进制拷到 /usr/local/bin
// 或 ~/.ccfly/bin,用户也常手动 `go build` + install;散文件在这些路径上全会丢。
// embed 让「有 ccfly 就有 tmux」对所有路径成立。
//
// 策略:**系统 tmux 永远优先**,内置只做兜底(调用方先 LookPath 再来找这里)。
// 用户已装的 tmux 可能带自己的配置、正跑着 server;tmux 的 client-server 协议
// 跨版本直接报 mismatch,抢默认 socket 会把用户现有会话搞挂。
//
// 非 darwin 构建 blobGz 为空,Bundled()=false,一切调用方零行为变化。
package tmuxbin

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// unpacked 缓存解压后的 tmux 二进制及其摘要(进程内只解压一次;只有「系统没
// tmux」的机器才会走到解压,常规部署零开销)。
var unpacked struct {
	once sync.Once
	bin  []byte
	sum  [sha256.Size]byte
	err  error
}

func unpack() ([]byte, [sha256.Size]byte, error) {
	unpacked.once.Do(func() {
		zr, err := gzip.NewReader(bytes.NewReader(blobGz))
		if err != nil {
			unpacked.err = fmt.Errorf("tmuxbin: bad embedded blob: %w", err)
			return
		}
		defer zr.Close()
		bin, err := io.ReadAll(zr)
		if err != nil {
			unpacked.err = fmt.Errorf("tmuxbin: decompress embedded tmux: %w", err)
			return
		}
		unpacked.bin = bin
		unpacked.sum = sha256.Sum256(bin)
	})
	return unpacked.bin, unpacked.sum, unpacked.err
}

// Bundled 报告本构建是否内嵌了 tmux(目前仅 darwin/arm64、darwin/amd64)。
func Bundled() bool { return len(blobGz) > 0 }

// DefaultDir 是内置 tmux 的默认释放目录 ~/.ccfly/bin(ccfly 自有工具目录,
// user 档 `ccfly install` 的二进制也装在这里)。
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("tmuxbin: no home dir: %w", err)
	}
	return filepath.Join(home, ".ccfly", "bin"), nil
}

// Ensure 把内嵌 tmux 释放到默认目录(已是最新则零写入),返回其路径。
func Ensure() (string, error) {
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return EnsureAt(dir)
}

// EnsureAt 把内嵌 tmux 释放到 dir/tmux 并返回该路径。幂等:文件已存在且内容
// 与内嵌版一致(sha256)→ 不动;缺失/内容不同(ccfly 升级带来新版 tmux、文件
// 损坏)→ 原子重写(临时文件 + rename,正在跑的旧 tmux server 持有旧 inode
// 不受影响)。二进制自带 ad-hoc 签名(构建脚本 codesign),释放后可直接执行。
func EnsureAt(dir string) (string, error) {
	if !Bundled() {
		return "", fmt.Errorf("tmuxbin: no tmux bundled in this build (%s)", selfDesc)
	}
	bin, sum, err := unpack()
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "tmux")
	if cur, err := os.ReadFile(dst); err == nil && sha256.Sum256(cur) == sum {
		return dst, nil // 已是当前版本
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("tmuxbin: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmux-*")
	if err != nil {
		return "", fmt.Errorf("tmuxbin: %w", err)
	}
	defer os.Remove(tmp.Name()) // rename 成功后已不存在,失败时清残留
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return "", fmt.Errorf("tmuxbin: write: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return "", fmt.Errorf("tmuxbin: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("tmuxbin: close: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", fmt.Errorf("tmuxbin: install: %w", err)
	}
	return dst, nil
}
