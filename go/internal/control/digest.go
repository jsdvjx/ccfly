package control

// digest.go — local-read accessor for the cloud syncer.
//
// control 自身不含任何 cloud/mesh/上报逻辑(见 control.go 头部约定):这里只把本地扫描出的
// 会话摘要 + 其 jsonl 文件路径/字节数暴露出去;真正的上传在 mesh 包(它持有 mesh_token / cloud 地址)。

import (
	"os"
	"path/filepath"
	"strings"
)

// SessionDigest 是单个会话的摘要快照,外加其本地 jsonl 文件位置与当前字节数——同步的数据源。
// 内嵌 claudeSnapshot 的导出字段(SessionID/Cwd/GitBranch/Title/Model/State/Turns/Tokens/
// LastTs/Preview…)对外可见;Path/Size 仅本机用(json:"-",不外泄进摘要文档)。
type SessionDigest struct {
	claudeSnapshot
	Path string `json:"-"`
	Size int64  `json:"-"`
}

// SessionDigests 扫描本地 Claude 会话,返回每个会话的摘要快照 + 其 jsonl 路径与当前大小。
// 复用 claudescan 的 (mtime+size) 缓存(cachedScanOne):idle 会话命中缓存,只有变动文件付全扫。
func SessionDigests() ([]SessionDigest, error) {
	root := claudeProjectsDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := []SessionDigest{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pdir := filepath.Join(root, e.Name())
		files, _ := os.ReadDir(pdir)
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			path := filepath.Join(pdir, f.Name())
			fi, err := f.Info()
			if err != nil {
				continue
			}
			snap, ok := cachedScanOne(path, fi.ModTime().UnixNano(), fi.Size())
			if !ok {
				continue
			}
			out = append(out, SessionDigest{claudeSnapshot: snap, Path: path, Size: fi.Size()})
		}
	}
	return out, nil
}
