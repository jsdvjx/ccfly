# @ccfly/react

## 0.4.4

### Patch Changes

- 4ae8025: fix(compose): reliable send-button feedback + image send

  - Instant press feedback (pointer-driven `.is-pressed`) — `.cbtn.primary` overrode
    `.cbtn:active` and the disabled state had no styling at all, so a tap showed nothing.
  - Not-sendable state is now visible (`.is-off`) and still tappable, flashing the reason
    (uploading / busy / not ready) instead of silently doing nothing.
  - Long-press with an image but no text now sends the image instead of dropping it.

## 0.4.3

### Patch Changes

- Rebuilt screen-state engine: attribute-aware rich-select detection (current row read from reverse-video/bg, not just the ❯ glyph) + closed-loop send/waitFor drive. All rich selects ported (model/permission/effort/confirm/multi/sessionScope/list). cc.hn — Claude Code Hub & Node.

## 0.3.2

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

## 0.3.1

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

## 0.3.0

### Minor Changes

- 95ca8ca: Port the `info/` InfoSheet feature from the upstream web app: native cards for
  `/cost` `/status` `/mcp` `/doctor` `/hooks` `/skills` (plus the `/cost`-group tabs
  `/stats` and `/config`). Commands flagged as "info" are driven in the background,
  their tmux screen is parsed into structured data, and rendered as styled cards with
  SWR caching, refresh, and a raw-text fallback. Default-wired through `SessionView`
  and `ControlBar` (`isInfoCmd` defaults to the registry); pass `isInfoCmd={() => false}`
  to disable. `/context` is unaffected — it still streams into the message flow.
  New exports: `InfoSheet`, `CARDS`, `cardFor`, `groupOf`, `isInfoCmd`, `useCapture`,
  `relTime`, and the `CmdCard` / `Capture` / `Phase` / `CardModule` types.

### Patch Changes

- 558d070: Fix messages not sending over a cloud/proxied connection. ControlBar submits
  (send message / slash command / suggestion) now go through the REST `/sendkeys`
  path — which types the text and presses a **separate** real `Enter` — instead of
  a single WS `"text\r"` frame. tmux's `assume-paste-time` heuristic could treat the
  bulk WS frame as a bracketed paste, so claude inserted the text without submitting
  (input box showed text but "发不出去"). The REST path submits reliably and, being a
  plain HTTP POST, traverses reverse proxies / the ccfly-cloud gateway like the
  transcript GETs that already work. Raw terminal typing (which bypasses this layer)
  is unaffected.
