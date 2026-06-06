---
"ccfly": patch
---

Fix `EACCES` when launching the prebuilt binary (notably under `npx ccfly`). The
platform subpackages declare no `bin` field (to avoid clashing with the `ccfly`
launcher name), so npm doesn't mark their binary executable, and `pnpm publish`
normalizes it to 0644 — the binary installed non-executable and the launcher
failed with `EACCES`. The launcher shim now restores the executable bit
(best-effort, POSIX) before spawning, so `npx ccfly …` / global installs work.
