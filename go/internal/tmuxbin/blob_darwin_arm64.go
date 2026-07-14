//go:build darwin && arm64

package tmuxbin

import _ "embed"

// 由 scripts/build-tmux-macos.sh 生成并提交进仓库(tmux 升级才重新生成)。
//
//go:embed blob/tmux-darwin-arm64.gz
var blobGz []byte

const selfDesc = "darwin/arm64"
