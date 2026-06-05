# ccfly

> Transport-agnostic **surface-world** toolkit for Claude Code sessions.

ccfly turns a running Claude Code session into a controllable web view. It reads
the session data Claude already writes to `~/.claude` (the jsonl transcripts) and
drives the underlying `tmux` pane — so the rendered UI mirrors the real session
(the "inner world") instead of scraping the screen.

- **`ccfly` (CLI)** — a small Go control service shipped over npm. It tails the
  `~/.claude` jsonl transcripts and drives `tmux`, exposing a local HTTP/WS API.
  It also serves its **own terminal WebSocket** (`GET /term?session=<tmux>`): a
  PTY running `tmux new-session -A` with a ttyd-compatible frame protocol, so the
  live terminal mirror needs **no external ttyd**.
- **`@ccfly/react`** — React components that consume that API and render a Claude
  Code session as a live, interactive view (transcript, tool calls, diffs,
  permission prompts, input). The live terminal mirror connects to ccfly's own
  `/term`.

Transport is intentionally out of scope: run it on localhost, tunnel it, or put
your own proxy in front. ccfly only cares about *rendering and controlling* a
session.

MIT licensed. Published to npm under the **`@ccfly`** org (CLI is the bare
package name **`ccfly`**).

> **Repository placeholder:** the `repository` / `homepage` fields in every
> `package.json` and the badges/links here use `jsdvjx` as a stand-in for the
> GitHub account or org that will host this repo. Replace `jsdvjx` with the real
> owner before (or right after) the first publish. See [PUBLISH.md](./PUBLISH.md).

## Usage

### 1. Run the local control service

```sh
npx ccfly serve
```

This launches the Go control service on loopback (default `127.0.0.1:7699`). The
right prebuilt binary for your platform is pulled in automatically via an
optional platform package (`ccfly-<os>-<arch>`, e.g. `ccfly-darwin-arm64`).

```sh
ccfly serve [--port 7699] [--bind 127.0.0.1] [--claude-dir ~/.claude/projects]
```

The service exposes the HTTP/WS control API **and its own terminal WebSocket**
(`GET /term?session=<tmux>`) — a PTY running `tmux new-session -A` with a
ttyd-compatible frame protocol. The live terminal mirror needs **no external
ttyd**.

> Security: the service performs no auth of its own and binds loopback by
> default. Front it with a reverse proxy / hub for any remote exposure.

### 2. Render a session in your app

```sh
npm i @ccfly/react
```

```tsx
import { CCFlyProvider, SessionView, CCFlyHosts } from "@ccfly/react";
import "@ccfly/react/style.css";

export function App() {
  return (
    // baseUrl points at the local control service from `npx ccfly serve`
    <CCFlyProvider config={{ baseUrl: "http://localhost:7699" }}>
      <SessionView sid={sessionId} />
      <CCFlyHosts />
    </CCFlyProvider>
  );
}
```

`<CCFlyProvider>` injects the control-service endpoint (and storage / tmux
naming) into the subtree; `<SessionView>` renders one Claude Code session
(transcript, tool cards, diffs, permission prompts, input, and the live terminal
mirror via ccfly's own `/term`); `<CCFlyHosts>` mounts the overlay hosts
(code reader, subagent stack, workflow detail, image lightbox) once at the root.

## Repository layout

```
ccfly/
├─ packages/
│  ├─ cli/          # npm package "ccfly" — bin wrapper + platform optionalDeps
│  └─ react/        # npm package "@ccfly/react" — UI components
├─ go/              # Go control service (reads ~/.claude jsonl + drives tmux)
├─ npm/             # per-platform binary subpackages (filled by CI cross-compile)
└─ examples/
   └─ web/          # minimal Vite app consuming @ccfly/react
```

## Development

```sh
pnpm install
pnpm build         # build all TS packages (@ccfly/react)
pnpm typecheck     # typecheck all packages
pnpm build:go      # build the Go control service into ./bin/ccfly (current platform)
pnpm build:binaries  # cross-compile into npm/ccfly-<os>-<arch>/bin (all 4 targets)
```

To build a single target locally:

```sh
TARGETS="darwin/arm64" pnpm build:binaries
```

This repo is a pnpm workspace and uses [changesets](https://github.com/changesets/changesets)
for versioning and publishing.

## Publishing

See [PUBLISH.md](./PUBLISH.md) for the release runbook (npm login, `@ccfly` org,
publish order for the platform binaries + CLI + React package, and GitHub setup).
Note the `jsdvjx` placeholder in `repository` / `homepage` fields must be replaced
with the real GitHub owner before publishing.

## Status

`v0.1.0`. The CLI, React, and Go control layers are in place. Contributions and
issues welcome.

## License

[MIT](./LICENSE)
