//go:build !darwin || (!arm64 && !amd64)

package tmuxbin

// 非 darwin(及未提供 blob 的架构)不内嵌:Bundled()=false,调用方保持原行为
// (Linux 发行版装 tmux 是常态;Windows 走 npm 平台包捆 psmux 的既有路径)。
var blobGz []byte

const selfDesc = "no bundled tmux for this platform"
