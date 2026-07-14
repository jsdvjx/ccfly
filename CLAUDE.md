# CLAUDE.md — ccfly

## 概述
ccfly 把一个正在跑的 **Claude Code 会话**镜像成一个可读、可控的 **Web 界面**:不抓屏,而是读 Claude 自己写在 `~/.claude` 下的 jsonl transcript + 驱动底层 `tmux` pane。一条 `npx ccfly serve` 即自成一个 **Node**(自带 web UI、自带终端 WS);多机用 hub 聚合即得 **cc.hn**(Claude Code Hub & Node)。

在 **ccfly 系列**里它是**设备端(Node)**:跑在 Mac 上,经 WireGuard overlay 接入云端 hub `cc.hn`(=`ccfly-cloud`)。本机 overlay IP `100.64.0.2`,hub `100.64.0.1`。

## 技术栈与目录
Go 1.25(控制服务 + overlay 客户端)+ pnpm workspace(TS/React + npm 分发)。`packageManager=pnpm@9.15.0`,Node>=18。

```
go/                    Go 控制服务(读 jsonl + 驱动 tmux + overlay 客户端)
  cmd/ccfly/           主二进制:serve/connect/install/new/attach/claude/...(npm 分发的就是它)
  cmd/ccfly-mesh/      纯组网客户端(无 tmux/控制面),给 sing-box center / byway exit 等服务器接 overlay
  cmd/ccfly-hostd/     host-agent,跑在多核 VM,受 cloud 经 overlay 指挥 `docker run` 实例容器
  internal/control/    HTTP/WS 控制面:transcript/SSE/subagents/workflow/term/upload/panemap/scanner
  internal/mesh/       WireGuard overlay(netstack)、配对、端口 expose/forward、relay 看门狗
  internal/svc/        把 connect 装成常驻服务(launchd / systemd)
  internal/profile/    能力档闸门 full/instance/host/restricted
  internal/hostagent/  hostd 的 docker spawn/stop 逻辑
packages/cli/          npm 包 "ccfly":bin shim(bin/ccfly.js)+ 按平台解析 optionalDeps
packages/react/        npm 包 "@ccfly/react":渲染/控制会话的 React 组件(tsup 构建,vitest 测)
npm/ccfly-<os>-<arch>/ 各平台预编译二进制子包(由 build-binaries 填充);ccfly-webdist=内嵌 web UI
examples/web/          消费 @ccfly/react 的 Vite 示例(默认 surface)
Dockerfile             受限档(restricted)查看器镜像;docker/=cc.hn 托管编排(instance/host/cloud)
```

> ⚠️ 在本仓库目录内跑 `npx ccfly` 会被 workspace 里的本地 `ccfly` 包遮蔽。要测发布版请 `cd ~ && npx ccfly serve`。

## 构建 / 运行 / 测试
根目录(已核实自 `package.json` scripts):
```sh
pnpm install
pnpm build            # 构建所有 TS 包(@ccfly/react)
pnpm typecheck        # 全量类型检查
pnpm build:go         # go -C go build -o ../bin/ccfly ./cmd/ccfly(当前平台 → 仓库根 bin/ccfly)
pnpm build:binaries   # bash scripts/build-binaries.sh,交叉编译进 npm/ccfly-<os>-<arch>/bin
TARGETS="darwin/arm64" pnpm build:binaries   # 只编一个目标
```
Go(在 `go/` 下):`go build ./...`、`go test ./...`(控制面/mesh/profile 均有单测)。
React:`pnpm -C packages/react test`(vitest)。
本地起服务:`go -C go run ./cmd/ccfly serve --port 7699 --bind 127.0.0.1`,浏览器开 `http://127.0.0.1:7699`。

CLI 子命令(`go/cmd/ccfly/main.go`):`serve` `connect <host>[/<code>]` `install`/`uninstall` `ls`(=list) `a`(=attach) `new` `claude` `version`。`connect` 默认**进程内同时起控制服务**(overlay listener 反代到它),`--no-serve` 才指向单独的 `serve`。

## 部署(本机 Mac,已核实)
设备端由 **system LaunchDaemon `com.ccfly.agent`**(`UserName=jinxing`)常驻,实际命令(`launchctl print system/com.ccfly.agent` 核实):
```
/usr/local/bin/ccfly connect cc.hn --claude-dir /Users/jinxing/.claude/projects
```
日志 `~/.ccfly/ccfly.log`。装/卸由 `ccfly install [--system]` / `ccfly uninstall` 生成(svc 包):system 档 bin→`/usr/local/bin/ccfly`、plist→`/Library/LaunchDaemons/com.ccfly.agent.plist`;user 档 bin→`~/.ccfly/bin/ccfly`、plist→`~/Library/LaunchAgents/`。`KeepAlive=true`、`RunAtLoad=true`。

**升级流程(核实可编译;`launchctl kickstart -k` 需 sudo 密码,请让用户自己跑最后一步):**
```sh
cd ~/ccfly-workspace/ccfly/go && CGO_ENABLED=0 go build -o /tmp/x ./cmd/ccfly
sudo install -m755 /tmp/x /usr/local/bin/ccfly && sudo launchctl kickstart -k system/com.ccfly.agent
```
> 当前 `/usr/local/bin/ccfly version` = 0.5.6,落后于仓库的 `packages/cli` 0.6.1 —— 改了代码记得用上面流程同步设备端。

受限镜像:先 `bash scripts/build-web.sh`(把已构建 web UI staged 进 `go/internal/control/webdist`,真实产物被 .gitignore),再 `docker build -t ccfly:restricted .`。云端托管(instance/host/cloud)编排见 `docker/README.md`。

发布:pnpm workspace + changesets(`.changeset/` 存在)。`pnpm changeset` 记变更 → `pnpm version` → `pnpm release`(build + 交叉编译 + 发平台子包 + `changeset publish`)。细节见 `PUBLISH.md`。

## 约定与坑(均已核实)
- **overlay 出网代理是 hub 下发、不是 baked 进 plist。** 旧记忆说 LaunchDaemon 带 `--overlay-forward 2080:100.64.0.1:2080` —— 当前 plist 已无此 flag。`127.0.0.1:2080 → overlay 100.64.0.1:2080` 这条转发由 `internal/mesh/mesh.go:applyMeshProxy` 据云端 device-config 下发的 `ProxyPort`(`GET /api/device/config`)**自动建立**,并把 `CCFLY_TMUX_PROXY` 设进环境;会话经 `internal/control/proxyenv.go` 注入 `http_proxy`/`HTTPS_PROXY`/`NODE_EXTRA_CA_CERTS`(指向云端下发的出口 CA `~/.ccfly/proxy-ca.pem`)。改 overlay 转发行为去看 mesh 包,别去改 plist。
- **5min 无流量看门狗(连接泄漏修复)。** `internal/mesh/forward.go` 的 `relay()` 有 `relayIdleTimeout = 5*time.Minute` 看门狗:云端重建 WS 隧道后,netstack TCP 侧不报错的半死连接会被强关,否则连接表占满(曾致 Mac 32767 连接耗尽、大量网页打不开)。健康连接靠活跃 SSE 流每 ~15s ping 永不触发。
- **tmux 会话名规范 `cc-<sid8>`。** 最新提交(`597f1da`)统一新建会话名为 `cc-<sid前8位>` 并撤掉裸 `claude` 兜底,根治接错会话/目录错/孤儿雪崩。`newTmuxName`(control.go)、panemap 真值表都按此口径。
- **panemap-hook 是确定性找 pane 的关键。** `ccfly panemap-hook` 作为 Claude Code 的 **SessionStart hook**,把「tmux pane → 当前 sid」写进 `~/.ccfly/panemap.json`;控制端据此精确命中会话所在 pane,杜绝消息错发。hook 由 `InstallSessionHook()`(随 `RunScanner` 在 serve/connect 启动时幂等写入 `~/.claude/settings.json`)。`CCFLY_NO_HOOK=1` 可关。
- **能力档闸门。** `internal/profile`:`full`(npm 默认)/`instance`/`host`/`restricted`,经 `-ldflags -X ...profile.defaultMode=` + root 的 `/etc/ccfly/profile.json` + `CCFLY_PROFILE`(只能加严)三层决定。受限档关掉 connect/install/claude/向会话注代理。
- **二进制分发模型(esbuild/swc 式)。** 主包 `ccfly` 不带二进制,只声明 4 个平台 optionalDependencies;`bin/ccfly.js` 运行期按 `process.platform-arch` 解析对应 `ccfly-<os>-<arch>` 子包并 exec。`CGO_ENABLED=0` 全静态;musl(Alpine)被 shim 显式拒绝。
- **mac 内置 tmux(darwin 构建自带,用户免装)。** `internal/tmuxbin` 把可移植 tmux 3.5a(libevent/ncursesw 静态链,仅依赖 `/usr/lib` 系统库;blob 由 `scripts/build-tmux-macos.sh` 生成并**提交进仓库**,升级 tmux 才重跑)`go:embed` 进 darwin 二进制。运行时 `ensureToolPath` 尾部(cmd/ccfly/toolpath_unix.go `ensureBundledTmux`)找不到系统 tmux 才释放到 `~/.ccfly/bin/tmux` 并前置 PATH;`ccfly install` 走 `servicePATH` 的 bundled 分支同样兜底。**系统 tmux 永远优先**——tmux client/server 跨版本直接 protocol mismatch,抢用户已跑的 server 会搞挂人家会话。内置 tmux 的 `default-terminal` 编译期定为 `screen-256color`(全版本 macOS 的 terminfo 都有;`tmux-256color` 老系统缺失)。Windows 是另一条路(npm 平台包捆 psmux,toolpath_windows.go);Linux 不内嵌(blob 空,`Bundled()=false`,行为不变)。
- **安全姿态:控制服务自身不鉴权**,默认只绑 loopback。任何远程暴露必须前置鉴权反代 / hub(cc.hn 经鉴权 WireGuard overlay 网关每个 Node)。勿在不可信网络 bind `0.0.0.0`。
- Go module path 是 `github.com/jsdvjx/ccfly/go`(与 GitHub remote `github.com/jsdvjx/ccfly` 一致;原占位 `github.com/ccfly/ccfly/go` 已统一改掉)。

## 相关项目
本仓是 ccfly 系列的**设备端(Node)**,也是所有设备侧二进制(`ccfly`/`ccfly-mesh`/`ccfly-hostd`)的源头仓库。系列全景见 `~/ccfly-workspace/ccfly-series.md`。

- **`ccfly-cloud`(云端 Hub,`~/ccfly-workspace/ccfly-cloud`,跑在 `cc.hn`,overlay 网关 `100.64.0.1`)** — 闭源控制面、本机的对端。本机经 `ccfly connect cc.hn` 以 WG-over-WSS 拨入(`GET /mesh`)、用连接码入网(`POST /connect`);浏览器经 hub 网关 `https://cc.hn/x/<device>/…` 反代到本机 `overlay:7699` 的 `ccfly serve`(`overlayServicePort=7699` 须两仓一致)。本机那条 `127.0.0.1:2080 → overlay 100.64.0.1:2080` 出网转发、以及注入会话的出口 CA,都来自 hub 的 `GET /api/device/config`(`ProxyPort`/CA)。**改入网/网关/鉴权/device-config 下发策略 → 去 `ccfly-cloud`;改本机如何消费这些(`internal/mesh` 的 `applyMeshProxy`、`internal/control` 的 proxyenv 注入)→ 在本仓。这是跨仓协议契约,改一边要同步另一边。**
- **`ccfly-app`(跨平台前端,`~/ccfly-workspace/ccfly-app`)** — web + 原生 React 客户端,消费本机控制服务的会话契约:`/term`(WS)、`/sse/jsonl`(SSE)、`/sessions`、`/takeover`、`/new`、`/dirs`、`/upload`、`/sendkeys`、`/image`。`base=''` 同源(本仓 `go:embed` 的 web UI)或 `base=/x/<device>` 经 cc.hn 路由。`tmux` 会话名 `cc-<sid8>`、takeover「先杀后建」是两仓共享约定。**改这些端点的形状/语义 → 本仓 `internal/control`;改 UI 渲染/交互 → 去 `ccfly-app`(本仓自带的 `examples/web`+`@ccfly/react` 是较旧的 surface)。**
- **`byway`(MITM 出口,`~/ccfly-workspace/byway`,与 hub 同机 `cc.hn`,overlay `100.64.0.3:8080`)** — 本机 2080 出网转发的最终去向:overlay `100.64.0.1:2080` 进 cloud 上的 sing-box center,AI 域名按 `out-a` 路由到 byway 做 TLS bump 并记日志。会话里注入的 `NODE_EXTRA_CA_CERTS`(`~/.ccfly/proxy-ca.pem`)即 byway 的根 CA(由 hub 经 device-config 下发)。把 sing-box center/byway 接进同一 overlay 的是本仓 `cmd/ccfly-mesh`。**改出口/MITM/路由 → 去 `byway`;需要不被 MITM 的干净出口 → 绕开 byway(走 SG anytls)。**
