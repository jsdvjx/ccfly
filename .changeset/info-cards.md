---
"@ccfly/react": minor
---

Port the `info/` InfoSheet feature from the upstream web app: native cards for
`/cost` `/status` `/mcp` `/doctor` `/hooks` `/skills` (plus the `/cost`-group tabs
`/stats` and `/config`). Commands flagged as "info" are driven in the background,
their tmux screen is parsed into structured data, and rendered as styled cards with
SWR caching, refresh, and a raw-text fallback. Default-wired through `SessionView`
and `ControlBar` (`isInfoCmd` defaults to the registry); pass `isInfoCmd={() => false}`
to disable. `/context` is unaffected — it still streams into the message flow.
New exports: `InfoSheet`, `CARDS`, `cardFor`, `groupOf`, `isInfoCmd`, `useCapture`,
`relTime`, and the `CmdCard` / `Capture` / `Phase` / `CardModule` types.
