# ccfly

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
