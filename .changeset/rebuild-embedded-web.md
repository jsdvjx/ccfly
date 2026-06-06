---
"ccfly": patch
---

Rebuild the embedded web UI so the bundled SPA (served by `ccfly serve` /
`ccfly connect`) picks up `@ccfly/react`'s reliable message-submit fix and the
native info cards. No Go code change — this republishes the binary with updated
web assets so deployed devices get the fixes without installing the npm package.
