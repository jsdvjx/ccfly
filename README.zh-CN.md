<div align="center">

# ✈️ ccfly

### 随处写码 —— 把你的 Claude Code 会话,镜像成一个可操控的网页「表世界」。

[English](./README.md) · **简体中文**

[![ccfly on npm](https://img.shields.io/npm/v/ccfly?logo=npm&label=ccfly&color=cb3837)](https://www.npmjs.com/package/ccfly)
[![@ccfly/react on npm](https://img.shields.io/npm/v/%40ccfly%2Freact?logo=npm&label=%40ccfly%2Freact&color=cb3837)](https://www.npmjs.com/package/@ccfly/react)
[![license](https://img.shields.io/npm/l/ccfly?color=4f9cf9)](./LICENSE)
[![node](https://img.shields.io/node/v/ccfly?logo=node.js&logoColor=white&color=339933)](https://nodejs.org)
[![PRs welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](#-参与贡献)

</div>

---

**ccfly** 把一个正在运行的 [Claude Code](https://www.anthropic.com/claude-code) 会话,变成一个实时、可操控的**网页视图** —— 让你在浏览器、手机、任何地方都能查看、操控、续跑你的会话。

它**不靠抓屏**。它把「**里世界**」—— Claude 本就写在 `~/.claude` 下的 jsonl 流水,加上底层的 `tmux` 面板 —— 镜像成一个你能渲染、能操控的「**表世界**」。断开连接、锁屏、走开、再连回来:会话仍在 `tmux` 里跑着,**什么都不会丢**。

> 一个 `ccfly serve` 就是一个自包含的 **Node** —— 查看并操控它所在的那台机器。把它跑在多台机器上、前面再套一个 hub,就得到 **cc.hn**(*Claude Code Hub & Node*):一个带鉴权的 Hub,统辖一张由设备 Node 组成的 mesh。ccfly 单机即完整可用;Hub 是可选项。

```
━━━ 里世界 ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
    Claude Code  ──►  ~/.claude/**.jsonl     对话 · 工具调用
         ⇅
    tmux 面板   ◄──►  PTY
              │
              ▼   读 jsonl · 驱动 tmux · /term WS
━━━ ccfly · Go 控制服务 ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
    HTTP + WebSocket API            npx ccfly serve · :7699
              │
              ▼   baseUrl (HTTP / WS)
━━━ 表世界 · @ccfly/react ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
    消息流 · 工具卡 · diff · 权限确认 · 输入
    富选择卡(模型 · 力度 · 权限) · 图片上传 · /compact 进度
    实时终端镜像 · 子代理 · workflow
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

## ✨ 为什么用 ccfly

- **零配置网页 UI。** `npx ccfly serve`,打开浏览器、选一个会话 —— 一个完整、自包含的表世界*就装在二进制里*,无需自建前端。
- **镜像,而非抓屏。** 界面由 Claude 本就写下的结构化 jsonl 构建 —— 真实的消息、工具调用、diff、权限确认 —— 不是从终端截下来的像素。
- **天生抗断连。** 会话活在 `tmux` 里。掉线、锁屏、稍后再连 —— 任务继续跑,滚动历史完整无缺。
- **自带终端,开箱即用。** ccfly 提供**自己的**终端 WebSocket(一个真实 PTY 跑 `tmux new-session -A`,帧协议兼容 ttyd)。实时终端镜像**无需任何外部 ttyd**。
- **原生富控件。** 斜杠菜单渲染成真正的选择器 —— 切换模型与思考力度、回应权限确认、实时看 `/compact` 进度 —— 还能把图片直接粘贴 / 拖拽 / 上传进会话。
- **两层,干净拆分。** 一个极小的 Go 控制服务(`ccfly`) + 一个 React 组件库(`@ccfly/react`)。可单用,可合用。
- **传输层无关。** 默认只绑 loopback。你可以打隧道、走 mesh,或在前面套一层带鉴权的反代 —— ccfly 只负责「渲染与操控」一个会话。
- **单二进制,零运行时依赖。** CLI 通过 npm 分发预编译的 Go 二进制(`npx ccfly`),按平台解析,无 postinstall 下载。

## 🚀 快速开始

### 方式 A —— 零配置网页 UI

```sh
npx ccfly serve
```

然后打开它打印出的地址(默认 <http://127.0.0.1:7699>)。ccfly **直接从二进制里**托管一个完整、自包含的表世界 —— 选一个会话,就能看到完整的消息流、工具卡、diff、权限确认、输入、实时终端镜像、子代理与 workflow。无需自建前端。

你平台对应的预编译二进制会自动拉取(`ccfly-<os>-<arch>`,如 `ccfly-darwin-arm64`)。

```sh
ccfly serve [--port 7699] [--bind 127.0.0.1] [--claude-dir ~/.claude/projects]
```

> 💡 请在 ccfly 仓库**之外**的目录运行(如 `cd ~ && npx ccfly serve`)。在仓库内,本地 workspace 里同名的 `ccfly` 包会把已发布的那个遮住。

### 方式 B —— 嵌入你自己的应用

```sh
npm i @ccfly/react
```

```tsx
import { CCFlyProvider, SessionView, CCFlyHosts } from "@ccfly/react";
import "@ccfly/react/style.css";

export function App() {
  return (
    // baseUrl 指向 `npx ccfly serve` 起的控制服务
    <CCFlyProvider config={{ baseUrl: "http://localhost:7699" }}>
      <SessionView sid={sessionId} />
      <CCFlyHosts />
    </CCFlyProvider>
  );
}
```

- `<CCFlyProvider>` 把控制服务端点(以及存储 / tmux 命名)注入子树。
- `<SessionView>` 渲染一个 Claude Code 会话 —— 消息流、工具卡、diff、权限确认、富选择菜单(模型 / 力度 / …)、图片与文件上传、输入,以及实时终端镜像(走 ccfly 自带的 `/term`)。
- `<CCFlyHosts>` 在根上挂一次浮层宿主(代码阅读器、子代理栈、workflow 详情、图片灯箱)。

## 🧠 工作原理

| 概念 | 含义 |
| --- | --- |
| **里世界** | 真实会话:Claude 运行所在的 `tmux` 面板 + `~/.claude` 下 append-only 的 jsonl 流水。 |
| **表世界** | 该会话的网页呈现。表世界上的每个控件,都映射到里世界的一个真实动作。 |
| **数据驱动,不抓屏** | 消息内容取自 jsonl,控件状态取自后端结构化判定器 —— 前端绝不读屏上的像素。 |
| **自带终端 WS** | `GET /term?session=<tmux>` 跑一个 PTY(`tmux new-session -A`),帧协议兼容 ttyd,实时终端无需外部 ttyd。 |

## 📦 两个包

| 包 | 安装 | 是什么 |
| --- | --- | --- |
| [`ccfly`](https://www.npmjs.com/package/ccfly) | `npx ccfly serve` | 通过 npm 分发的 Go 控制服务 —— 跟读 `~/.claude` jsonl、驱动 `tmux`、暴露 HTTP/WS API 与 `/term`。 |
| [`@ccfly/react`](https://www.npmjs.com/package/@ccfly/react) | `npm i @ccfly/react` | 渲染并操控会话的 React 组件:消息流、工具卡、diff、权限、输入、终端、子代理、workflow。 |

## 🔌 控制 API(表世界)

控制服务暴露一组小而稳定的 HTTP/WS 接口,主要包括:

| 分组 | 端点 |
| --- | --- |
| **会话数据** | `GET /transcript[/stream]` · `GET /subtranscript[/stream]` · `GET /subagents` · `GET /workflow` · `GET /workflowagent[/stream]` · `GET /cmdresult` · `GET /image` · `GET /info` · `GET /state` |
| **操控** | `POST /sendkeys` · `POST /start` · `POST /upload`(图片/文件 → 会话 cwd) |
| **终端** | `GET /term`(WebSocket,兼容 ttyd) |
| **兜底 / 健康** | `GET /capture`(抓屏,用于非 jsonl 会话) · `GET /healthz` |

> 🔐 **安全。** 该服务**自身不做鉴权**,默认只绑 loopback —— 其权限等价于本地 `tmux` 已经给出的「完整终端控制」。任何远程暴露,都请在前面套一层带鉴权的反向代理 / hub(例如一个 **cc.hn** Hub —— 它把每个 Node 经带鉴权的 WireGuard overlay 网关出来),或绑到私有 mesh 网卡。**永远不要在不可信网络上裸绑 `0.0.0.0`。**

## 🗂 仓库结构

```
ccfly/
├─ packages/
│  ├─ cli/          # npm 包 "ccfly" —— bin shim + 各平台 optionalDeps
│  └─ react/        # npm 包 "@ccfly/react" —— UI 组件
├─ go/              # Go 控制服务(读 ~/.claude jsonl + 驱动 tmux)
├─ npm/             # 各平台二进制子包(由交叉编译填充)
└─ examples/
   └─ web/          # 消费 @ccfly/react 的 Vite 应用(默认表世界)
```

## 🛠 本地开发

```sh
pnpm install
pnpm build           # 构建所有 TS 包(@ccfly/react)
pnpm typecheck       # 全量类型检查
pnpm build:go        # 把 Go 服务编译进 ./bin/ccfly(当前平台)
pnpm build:binaries  # 交叉编译进 npm/ccfly-<os>-<arch>/bin(全平台)
```

只构建单个目标:

```sh
TARGETS="darwin/arm64" pnpm build:binaries
```

本仓库是一个 pnpm workspace,使用 [changesets](https://github.com/changesets/changesets) 管理版本与发布。

## 🗺 路线图

- ✅ Go 控制服务 · `@ccfly/react` 组件库 · 自带终端 WS(`v0.1.x`)
- ✅ **零配置网页 UI** —— `npx ccfly serve`,打开浏览器、选一个会话,完整表世界、无需自建前端(`v0.2.0`)
- ⬜ 多实例 / 多设备上下文 · 可换肤、带前缀的 CSS · 完整 CI 交叉编译矩阵

## 🤝 参与贡献

欢迎提 issue 与 PR。发布流程见 [PUBLISH.md](./PUBLISH.md)(npm 登录、`@ccfly` 组织、平台二进制 + CLI + React 包的发布顺序,以及 GitHub 设置)。

## 许可

[MIT](./LICENSE) © ccfly authors
