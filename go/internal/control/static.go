package control

// static.go — 内嵌(embed)已构建的 web 表世界 SPA,并由控制服务自身托管(自包含)。
//
// 目标:`ccfly serve` 单进程既出 API 端点、又出落地页 + 会话视图前端,无需另起静态服务器。
//
// 内嵌:`//go:embed all:webdist` 把整棵 webdist/ 打进二进制(all: 含点开头文件,如 .vite 产物)。
// 占位:仓库内 webdist/index.html 是「未构建」占位页,保证 embed 目录非空、go build 在真正
// 跑 Vite 之前也能编过;真实构建产物(Vite outDir 指到 webdist/)会覆盖它。
//
// SPA 回退:命中静态文件就发文件;其余「非 API、无文件」路径一律发 index.html
// (前端路由 history 模式所需)。API 路由因 Go 1.22 ServeMux「最具体优先」自动赢过本兜底
// "GET /",故 handleStatic 只接管剩下的请求,不会截到 /transcript、/sessions 等显式端点。
//
// 安全模型同本服务其它端点:自身不鉴权,默认绑回环,远端暴露交反代。

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:webdist
var webdistFS embed.FS

// staticHandler 构造 SPA 静态托管 + history 回退的处理器。
// fs.Sub 把根从 "webdist" 降到产物根,使 URL 路径与产物布局一一对应(/assets/x.js 等)。
func staticHandler() http.HandlerFunc {
	sub, err := fs.Sub(webdistFS, "webdist")
	if err != nil {
		// 仅当 embed 路径写错才会到这(编译期 embed 已保证目录存在);兜底返 500 文本。
		return func(w http.ResponseWriter, _ *http.Request) { ctrlErr(w, 500, "spa embed: "+err.Error()) }
	}
	fileServer := http.FileServer(http.FS(sub))

	return func(w http.ResponseWriter, r *http.Request) {
		// URL 路径去掉前导 "/" 即 fs 路径;空(根)→ 让 FileServer 出 index.html。
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			fileServer.ServeHTTP(w, r)
			return
		}
		// 文件存在 → 直接发(静态资源);不存在 → SPA 回退发 index.html。
		if _, err := fs.Stat(sub, p); err != nil {
			serveIndex(w, r, sub)
			return
		}
		fileServer.ServeHTTP(w, r)
	}
}

// serveIndex 发产物根的 index.html(SPA history 回退)。
func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		ctrlErr(w, 404, "spa index.html missing")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}
