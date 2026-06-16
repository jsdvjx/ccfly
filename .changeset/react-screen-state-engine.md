---
"@ccfly/react": patch
---

Screen-state engine: harden + test the read-screen → classify pipeline.

- Split the pure frame parser out of `engine.ts` into `pre.ts` (zero xterm/React imports) and add a pure `classify(frame)` seam, so detection is unit-testable and "what's tested is what runs."
- Add a vitest harness with a JSON-constructible frame builder and golden tests, including a real `tmux capture-pane` fixture of the `/model` menu. 14 passing; remaining failure-oracle gaps (F4 wrapped/un-numbered options) pinned as documented `it.fails`.
- Fix the menu title scrape: it now takes the topmost line of the title block (bounded by a blank line or a horizontal rule), instead of the nearest line — which on description-bearing menus like `/model` returned the description tail (`--model.`) instead of the real title (`Select model`).

No public API or behavior change beyond the title fix; the parser refactor is internal and re-exported.
