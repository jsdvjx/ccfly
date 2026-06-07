# ccfly

## 0.4.5

### Patch Changes

- ccfly install: accept `--system` / flags in any position (a leading flag is no longer mis-parsed as the host), and prompt for the host (default cc.hn) when omitted instead of erroring with `lookup --system: no such host`.

## 0.4.4

### Patch Changes

- Rebuilt screen-state engine: attribute-aware rich-select detection (current row read from reverse-video/bg, not just the ❯ glyph) + closed-loop send/waitFor drive. All rich selects ported (model/permission/effort/confirm/multi/sessionScope/list). cc.hn — Claude Code Hub & Node.

## 0.3.7

### Patch Changes

- Fix info cards (`/cost` `/status` `/mcp` `/doctor` `/hooks` `/skills`) showing
  "未能打开 … 里世界未响应".

  The device's `/capture?ansi=1` ran `tmux capture-pane -t <s> -e -S -N` — but `-e`
  **without `-p`** makes tmux copy the screen into a paste buffer instead of printing it
  to stdout, so the HTTP body came back empty. The InfoSheet polls an ansi capture for
  every info command, so it always parsed an empty screen and gave up.

  - **ccfly** (device): always pass `-p`, append `-e` only for `ansi=1` →
    `tmux capture-pane -p -e`. (`/state` was already correct.)
  - **@ccfly/react**: defensive fallback in `fetchCapture` — if an ansi capture returns
    empty, refetch without ansi. Parsing always strips ANSI, so this is lossless (only the
    raw "原始" view loses color) and makes info cards work against an older device too.

## 0.3.6

### Patch Changes

- Two session-control fixes for cloud / remote use:

  - **@ccfly/react** — the hidden live-terminal mirror now ATTACHES ONLY: it no longer
    auto-spawns `claude --resume` when you merely open a session. Auto-spawn made a
    non-live session blink live/dead (the spawned process exits, the WS reconnects and
    respawns…), so the input bar flapped between the send box and "会话未运行" and the
    slash button flickered out of reach. Starting a session is now exclusively the
    explicit "启动会话" button's job (`/start`); the mirror gates its `/term` connection
    on liveness. Live sessions are unaffected (clean attach).

  - **ccfly + @ccfly/react** — the control-state detector (device `ctrlstate.go` and
    client `livestate.ts`, kept in lock-step) now distinguishes claude's input box from
    a shell `❯` prompt. A tmux pane sitting at a zsh shell (claude exited) was detected
    as `input`, so the web showed a send box and slash commands were typed into the shell
    ("zsh: command not found: context"). Both detectors now require positive claude
    evidence — a pure `─{6,}` border, a hint line (`? for shortcuts` / `← for agents` /
    `to send` / `shift+tab`), or (device-only, reliable) the `❯`+NBSP input line — before
    reporting `input`; otherwise the pane is treated as offline ("会话未在运行 / 启动会话").

## 0.3.5

### Patch Changes

- 54e33da: Fix `EACCES` when launching the prebuilt binary (notably under `npx ccfly`). The
  platform subpackages declare no `bin` field (to avoid clashing with the `ccfly`
  launcher name), so npm doesn't mark their binary executable, and `pnpm publish`
  normalizes it to 0644 — the binary installed non-executable and the launcher
  failed with `EACCES`. The launcher shim now restores the executable bit
  (best-effort, POSIX) before spawning, so `npx ccfly …` / global installs work.

## 0.3.4

### Patch Changes

- 558d070: Rebuild the embedded web UI so the bundled SPA (served by `ccfly serve` /
  `ccfly connect`) picks up `@ccfly/react`'s reliable message-submit fix and the
  native info cards. No Go code change — this republishes the binary with updated
  web assets so deployed devices get the fixes without installing the npm package.
