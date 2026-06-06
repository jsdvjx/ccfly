# @ccfly/react

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
