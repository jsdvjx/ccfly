---
"ccfly": patch
---

fix(image): submit actually sends when an image is attached

The device sent Enter with zero delay after the last bracketed image paste, so the
Enter landed inside Claude Code's async paste-ingest window (path → `[Image #N]`) and
was swallowed — text + `[Image #N]` sat in the input box unsent. Now we poll
`capture-pane` until the `[Image #N]` placeholders render (baseline-delta count,
bounded ~1.4s timeout fallback) before sending Enter. No-image submits are unchanged
(zero added latency).
