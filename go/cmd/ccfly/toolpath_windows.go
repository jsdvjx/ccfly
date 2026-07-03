//go:build windows

package main

import (
	"os"
	"path/filepath"
)

// ensureToolPath prepends the executable's own directory to PATH so that
// bundled tools (e.g. tmux.exe / psmux shipped alongside ccfly.exe in the
// npm platform package) are found by exec.Command without requiring the
// user to install them separately.
func ensureToolPath() {
	// Windows 默认把「当前目录」排在可执行搜索最前;Go 的 exec.LookPath 对 cwd 命中直接报
	// "cannot run executable found relative to current directory"(安全拒绝)。若启动 cwd 恰好
	// 散落着 tmux.exe/ccfly.exe(用户手测残留),所有 exec.Command("tmux") 全灭。设此环境变量
	// 让 LookPath 跳过 cwd,只走 PATH(下面已把自带 bin 目录放在 PATH 最前)。
	os.Setenv("NoDefaultCurrentDirectoryInExePath", "1")
	self, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(self)
	current := os.Getenv("PATH")
	if current == "" {
		os.Setenv("PATH", dir)
	} else {
		os.Setenv("PATH", dir+string(os.PathListSeparator)+current)
	}
}
