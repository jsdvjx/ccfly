<div align="center">

# ✈️ ccfly

### Code anywhere — your Claude Code sessions, mirrored to a controllable web surface.

**English** · [简体中文](./README.zh-CN.md)

[![ccfly on npm](https://img.shields.io/npm/v/ccfly?logo=npm&label=ccfly&color=cb3837)](https://www.npmjs.com/package/ccfly)
[![@ccfly/react on npm](https://img.shields.io/npm/v/%40ccfly%2Freact?logo=npm&label=%40ccfly%2Freact&color=cb3837)](https://www.npmjs.com/package/@ccfly/react)
[![license](https://img.shields.io/npm/l/ccfly?color=4f9cf9)](./LICENSE)
[![node](https://img.shields.io/node/v/ccfly?logo=node.js&logoColor=white&color=339933)](https://nodejs.org)
[![PRs welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](#contributing)

</div>

---

**ccfly** turns a running [Claude Code](https://www.anthropic.com/claude-code) session into a live, controllable **web view** — so you can read it, steer it, and resume it from a browser, your phone, anywhere.

It does **not** scrape the screen. It mirrors the *inner world* — the jsonl transcript Claude already writes under `~/.claude`, plus the underlying `tmux` pane — into a *surface world* you can render and control. Detach, lock your phone, walk away, reconnect: the session keeps running in `tmux` and **nothing is lost**.

> One `ccfly serve` is a self-contained **Node** — read and steer the machine it runs on. Run it on many machines and front them with a hub to get **cc.hn** (*Claude Code Hub & Node*): one authenticated Hub gatewaying a mesh of device Nodes. ccfly stays fully usable standalone; the Hub is optional.

```
━━━ inner world ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
    Claude Code  ──►  ~/.claude/**.jsonl     transcript · tool calls
         ⇅
    tmux pane   ◄──►  PTY
              │
              ▼   reads jsonl · drives tmux · /term WS
━━━ ccfly · Go control service ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
    HTTP + WebSocket API            npx ccfly serve · :7699
              │
              ▼   baseUrl (HTTP / WS)
━━━ surface world · @ccfly/react ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
    transcript · tool cards · diffs · permission prompts · input
    rich selects (model · effort · permission) · image upload · /compact progress
    live terminal mirror · subagents · workflows
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

## ✨ Why ccfly

- **Zero-config web UI.** `npx ccfly serve`, open the browser, pick a session — a full, self-contained surface world ships *inside the binary*. Nothing to build.
- **Mirror, don't scrape.** The UI is built from the structured jsonl Claude already writes — real messages, tool calls, diffs, and permission prompts — not from terminal screen-grabs.
- **Detach-safe by design.** Sessions live in `tmux`. Drop your connection, lock your phone, reconnect later — the run continues and the scrollback is intact.
- **Batteries-included terminal.** ccfly serves its **own** terminal WebSocket (a real PTY running `tmux new-session -A`, ttyd-compatible frames). The live terminal mirror needs **no external ttyd**.
- **Rich, native controls.** Slash menus become real pickers — switch model & thinking effort, answer permission prompts, watch `/compact` progress live — and paste, drag, or upload images straight into the session.
- **Two layers, cleanly split.** A tiny Go control service (`ccfly`) + a React component library (`@ccfly/react`). Use one, or both.
- **Transport-agnostic.** Runs on loopback by default. Tunnel it, mesh it, or put your own authenticating proxy in front — ccfly only cares about *rendering and controlling* a session.
- **Single binary, zero runtime deps.** The CLI ships prebuilt Go binaries over npm (`npx ccfly`), resolved per-platform with no postinstall download.

## 🚀 Quick start

### Option A — zero-config web UI

```sh
npx ccfly serve
```

Then open the URL it prints (default <http://127.0.0.1:7699>). ccfly serves a **complete, self-contained surface world** straight from the binary — pick a session and you get the full transcript, tool cards, diffs, permission prompts, input, live terminal mirror, subagents and workflows. Nothing to build.

The right prebuilt binary for your platform is pulled in automatically (`ccfly-<os>-<arch>`, e.g. `ccfly-darwin-arm64`).

```sh
ccfly serve [--port 7699] [--bind 127.0.0.1] [--claude-dir ~/.claude/projects]
```

> 💡 Run it from **outside** the ccfly monorepo (e.g. `cd ~ && npx ccfly serve`). Inside the repo, a local workspace package named `ccfly` shadows the published one.

### Option B — embed it in your own app

```sh
npm i @ccfly/react
```

```tsx
import { CCFlyProvider, SessionView, CCFlyHosts } from "@ccfly/react";
import "@ccfly/react/style.css";

export function App() {
  return (
    // baseUrl points at the control service started by `npx ccfly serve`
    <CCFlyProvider config={{ baseUrl: "http://localhost:7699" }}>
      <SessionView sid={sessionId} />
      <CCFlyHosts />
    </CCFlyProvider>
  );
}
```

- `<CCFlyProvider>` injects the control-service endpoint (plus storage / tmux naming) into the subtree.
- `<SessionView>` renders one Claude Code session — transcript, tool cards, diffs, permission prompts, rich select menus (model / effort / …), image & file upload, input, and the live terminal mirror (via ccfly's own `/term`).
- `<CCFlyHosts>` mounts the overlay hosts once at the root (code reader, subagent stack, workflow detail, image lightbox).

## 🧠 How it works

| Concept | What it means |
| --- | --- |
| **Inner world** | The real session: the `tmux` pane Claude runs in + the append-only jsonl transcript under `~/.claude`. |
| **Surface world** | The web rendering of that session. Every control on the surface maps to a real action on the inner world. |
| **Data over scraping** | Message content comes from jsonl; control state comes from structured backend detectors — the frontend never reads pixels off the screen. |
| **Own terminal WS** | `GET /term?session=<tmux>` runs a PTY (`tmux new-session -A`) with a ttyd-compatible frame protocol, so the live terminal needs no external ttyd. |

## 📦 Packages

| Package | Install | What it is |
| --- | --- | --- |
| [`ccfly`](https://www.npmjs.com/package/ccfly) | `npx ccfly serve` | Go control service over npm — tails `~/.claude` jsonl, drives `tmux`, exposes the HTTP/WS API + `/term`. |
| [`@ccfly/react`](https://www.npmjs.com/package/@ccfly/react) | `npm i @ccfly/react` | React components that render and control a session: transcript, tool cards, diffs, permissions, input, terminal, subagents, workflows. |

## 🔌 Control API (surface)

The control service exposes a small, stable HTTP/WS surface. Highlights:

| Group | Endpoints |
| --- | --- |
| **Session data** | `GET /transcript[/stream]` · `GET /subtranscript[/stream]` · `GET /subagents` · `GET /workflow` · `GET /workflowagent[/stream]` · `GET /cmdresult` · `GET /image` · `GET /info` · `GET /state` |
| **Control** | `POST /sendkeys` · `POST /start` · `POST /upload` (image/file → session cwd) |
| **Terminal** | `GET /term` (WebSocket, ttyd-compatible) |
| **Fallback / health** | `GET /capture` (screen scrape, non-jsonl sessions) · `GET /healthz` |

> 🔐 **Security.** The service performs **no auth of its own** and binds loopback by default — equivalent to the full terminal control a local `tmux` already grants. For any remote exposure, front it with an authenticating reverse proxy / hub (such as a **cc.hn** Hub, which gateways each Node over an authenticated WireGuard overlay), or bind it to a private mesh interface. Never bind `0.0.0.0` on an untrusted network.

## 🗂 Repository layout

```
ccfly/
├─ packages/
│  ├─ cli/          # npm package "ccfly" — bin shim + per-platform optionalDeps
│  └─ react/        # npm package "@ccfly/react" — UI components
├─ go/              # Go control service (reads ~/.claude jsonl + drives tmux)
├─ npm/             # per-platform binary subpackages (filled by cross-compile)
└─ examples/
   └─ web/          # Vite app consuming @ccfly/react (the default surface)
```

## 🛠 Development

```sh
pnpm install
pnpm build           # build all TS packages (@ccfly/react)
pnpm typecheck       # typecheck all packages
pnpm build:go        # build the Go service into ./bin/ccfly (current platform)
pnpm build:binaries  # cross-compile into npm/ccfly-<os>-<arch>/bin (all targets)
```

Build a single target:

```sh
TARGETS="darwin/arm64" pnpm build:binaries
```

This repo is a pnpm workspace and uses [changesets](https://github.com/changesets/changesets) for versioning and publishing.

## 🗺 Roadmap

- ✅ Go control service · `@ccfly/react` component library · self-hosted terminal WS (`v0.1.x`)
- ✅ **Zero-config web UI** — `npx ccfly serve`, open the browser, pick a session, full surface world with nothing to build (`v0.2.0`)
- ⬜ Multi-instance / multi-device contexts · themeable, prefixed CSS · full CI cross-compile matrix

## 🤝 Contributing

Issues and PRs welcome. For releases, see [PUBLISH.md](./PUBLISH.md) (npm login, the `@ccfly` org, publish order for the platform binaries + CLI + React package, and GitHub setup).

## License

[MIT](./LICENSE) © ccfly authors
