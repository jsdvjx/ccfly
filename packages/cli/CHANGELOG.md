# ccfly

## 0.6.0

### Minor Changes

- `ccfly claude login`:`--email` 改可选 —— 省略时由 ccfly-cloud 在你可访问的共享账号里按 claude 用量 + 分配次数自动选号;登录成功后该设备的 claude 会话经 sing-box center 从所分配账号的专属 /128 出网。新增 `ccfly claude logout` 清除按账号路由(不删凭证)。

## 0.5.9

### Patch Changes

- Decouple the client web UI into the `ccfly-webdist` npm package and fetch it at runtime: the binary embeds a fallback copy and pulls a newer `ccfly-webdist` from npm (SRI-verified, cached under `~/.ccfly/webcache`), so UI updates ship via npm without rebuilding the platform binaries. Also sync local Claude sessions (summary docs + archived jsonl) to the cloud so they're listable/searchable cross-device.

## 0.5.8

### Patch Changes

- 8b8de7f: New session now works when the ccfly service runs as root, and skips the folder-trust prompt for directories you explicitly pick.

  - **IS_SANDBOX for root + skip**: when `--dangerously-skip-permissions` is used and ccfly runs as root, inject `IS_SANDBOX=1` into the session — Claude Code otherwise refuses skip-permissions under root for safety ("cannot be used with root/sudo privileges"). Non-root is unaffected. Applied to `ccfly new` / the picker, the web `POST /new`, and offline `--resume` via `ccfly a`.
  - **Pre-trust the chosen directory**: sessions created via `ccfly new` / the picker / the web "＋ 新建" set `hasTrustDialogAccepted` for the chosen directory in `~/.claude.json`, so Claude Code doesn't block startup on the "Is this a project you trust?" dialog — you selected the directory explicitly. Done defensively (atomic write; no-op on any read/parse/write error).

## 0.5.7

### Patch Changes

- 3b2d920: New session can now choose its directory via a filesystem browser, in all three surfaces.

  - `ccfly a` picker: "＋ 新建会话…" (and the `n` key) opens a directory browser — navigate with ↑↓ / Enter / ← (parent), `n` creates a session in the current directory; `p`/`y` still toggle permission mode / skip.
  - `ccfly new`: with no `[dir]` arg it opens the same browser (a given arg still creates directly).
  - Web UI: a "＋ 新建" button in the workspace opens a directory-browser dialog, then starts a fresh claude there and switches to it.

  Backed by two new local control endpoints: `GET /dirs?path=` (list subdirectories) and `POST /new {cwd, permission_mode?, skip_permissions?}` (start a fresh claude detached, poll the panemap, return the real session id).

## 0.5.6

### Patch Changes

- 3528580: The embedded web UI now renders `AskUserQuestion` with a dedicated, unfolded card — question header, all options (with the chosen one highlighted), and the selected answer shown inline — instead of a collapsed generic JSON dump that hid the answer.

## 0.5.5

### Patch Changes

- 7d34c37: `ccfly new` and `ccfly a` can now set claude permission options, and the
  interactive picker can create new sessions.

  - New flags on both `ccfly new` and `ccfly a`: `--permission-mode <default|acceptEdits|plan|bypassPermissions>` and `--dangerously-skip-permissions` (alias `--yolo`), passed through to claude. They apply when a session is launched (new, or offline resume); attaching to an already-running session leaves its mode untouched.
  - The `ccfly a` picker gained a "＋ new session" entry at both levels (current dir / project dir; shortcut `n`) and a footer to toggle the permission options live (`p` cycles permission-mode, `y` toggles skip) — applied to whatever you launch.

  Also: the embedded web UI now renders `!command` bash echoes (`<bash-input>`/`<bash-stdout>`/`<bash-stderr>`) as IN/OUT/ERR cards instead of dropping them.

- 86ddd3d: Log device-config refresh failures instead of swallowing them. `refreshConfig` now logs when the `GET /api/device/config` request errors, returns non-200, or yields unparseable JSON. The call still degrades gracefully (keeps existing State), but the failure is now observable — previously a silent return made it hard to tell why cloud-advertised config (proxy port/CA) wasn't being applied.

## 0.5.4

### Patch Changes

- Auto-trust the mesh exit's MITM CA. When the cloud advertises a proxy exit that bumps TLS (byway `-bump`), ccfly now receives the exit's CA bundle in its device config, writes it to `~/.ccfly/proxy-ca.pem`, and injects `NODE_EXTRA_CA_CERTS` into ccfly-created tmux sessions (`new`/`attach`/`/term`). Claude inside a session then reaches AI endpoints through the exit without any manual certificate setup. No-op when no proxy is advertised or when `CCFLY_TMUX_PROXY_CA` is already set.

## 0.4.10

### Patch Changes

- 4ae8025: fix(image): submit actually sends when an image is attached

  The device sent Enter with zero delay after the last bracketed image paste, so the
  Enter landed inside Claude Code's async paste-ingest window (path → `[Image #N]`) and
  was swallowed — text + `[Image #N]` sat in the input box unsent. Now we poll
  `capture-pane` until the `[Image #N]` placeholders render (baseline-delta count,
  bounded ~1.4s timeout fallback) before sending Enter. No-image submits are unchanged
  (zero added latency).

## 0.4.9

### Patch Changes

- fix(image): 改用「括号粘贴路径」原生附图,全平台统一(含 --system),替掉剪贴板/路径拼文本

  附图改走终端「拖拽文件」的底层机制:tmux `set-buffer` + `paste-buffer -p`(括号粘贴)把上传图的绝对路径粘进里世界输入框,Claude 原生嵌成 `[Image #N]`。纯 tmux 往 PTY 注字节、与 GUI/剪贴板无关 → `--system` / headless 一样能用,不再需要 0.4.8 的 `CCFLY_IMAGE_PATHS` 路径拼文本特判,也移除了 darwin 的 osascript 剪贴板通道(连同 `imgClip`/`pngfClassForExt`/`appleScriptQuote`)。

  实测(v2.1.168):文本 + 多图 → `[Image #1] [Image #2]` 原生嵌入、序号正确、buffer 不残留(`-d`),Claude 正确读出两张。优雅降级:万一某版 Claude 不再自动嵌图,路径就当文本落框 → 提交后 Claude 仍会 `Read` 取图。

## 0.4.8

### Patch Changes

- fix(image): `--system` 守护进程改走「图片路径拼文本」回退,绕开它没有的 GUI 会话剪贴板

  `--system`(LaunchDaemon)跑在系统上下文、拿不到 GUI 登录会话的剪贴板,osascript 写的剪贴板与里世界 Claude 读的不是同一块(asuser 也注入不进)→ 截图粘不进消息。改为:安装 `--system` 时在 plist 注入 `CCFLY_IMAGE_PATHS=1`,控制服务据此走与 Linux 相同的回退 —— 把图片绝对路径当文本拼进输入框,Claude 用 Read 工具读图(上传落在会话 cwd 内的 `.ccfly-uploads/`,Read 默认放行、不弹权限,实测可正确读出图片内容)。用户级安装无此 env、仍走剪贴板原生粘贴(干净的 `[Image #N]`)。同时移除上一版失败的 `launchctl asuser` 尝试。

## 0.4.7

### Patch Changes

- Fix native image paste under `ccfly install --system`: the launchd daemon has no GUI session, so its `osascript set the clipboard` didn't reach the tmux Claude (C-v pasted nothing, silently). Route the clipboard set through `launchctl asuser <console-uid>` to inject into the logged-in GUI session.

## 0.4.6

### Patch Changes

- Report a stable machine fingerprint (hardware UUID — Linux /etc/machine-id, macOS IOPlatformUUID — with a persisted ~/.ccfly/machine-id fallback) during no-code pairing, so re-installing/re-pairing the same machine reuses its existing device instead of spawning duplicates.

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
