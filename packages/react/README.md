# @ccfly/react

> React components that render a running **Claude Code** session as a live, controllable web view.

Part of [**ccfly**](https://github.com/jsdvjx/ccfly) — *code anywhere*: mirror your Claude Code sessions to a browser / phone and steer them. `@ccfly/react` renders the **surface world** of a session, driven by the [`ccfly`](https://www.npmjs.com/package/ccfly) Go control service (`npx ccfly serve`) — from structured jsonl + control state, **not** screen-scraping.

## Install

```sh
npm i @ccfly/react
```

Peer deps: `react` / `react-dom` >= 18.

## Usage

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

- **`<CCFlyProvider>`** — injects the control-service endpoint (+ storage / tmux naming) into the subtree.
- **`<SessionView sid>`** — renders one session: transcript, tool cards, diffs, permission prompts, rich select menus (model / effort / …), image & file upload, input, and the live terminal mirror (via ccfly's own `/term`).
- **`<CCFlyHosts>`** — mounts the root overlays once (code reader, subagent stack, workflow detail, image lightbox).

## What it renders

Transcript · tool & diff cards · permission prompts · rich select pickers (model · thinking effort · permission · confirm · multi / list) · `/compact` progress · image / file upload (paste · drag · picker) · subagents · workflows · live terminal mirror.

## Docs & source

Full architecture, the Go control service, and the zero-config `npx ccfly serve` web UI: **<https://github.com/jsdvjx/ccfly>**.

## License

MIT © ccfly authors
