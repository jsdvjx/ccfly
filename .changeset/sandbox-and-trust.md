---
"ccfly": patch
---

New session now works when the ccfly service runs as root, and skips the folder-trust prompt for directories you explicitly pick.

- **IS_SANDBOX for root + skip**: when `--dangerously-skip-permissions` is used and ccfly runs as root, inject `IS_SANDBOX=1` into the session — Claude Code otherwise refuses skip-permissions under root for safety ("cannot be used with root/sudo privileges"). Non-root is unaffected. Applied to `ccfly new` / the picker, the web `POST /new`, and offline `--resume` via `ccfly a`.
- **Pre-trust the chosen directory**: sessions created via `ccfly new` / the picker / the web "＋ 新建" set `hasTrustDialogAccepted` for the chosen directory in `~/.claude.json`, so Claude Code doesn't block startup on the "Is this a project you trust?" dialog — you selected the directory explicitly. Done defensively (atomic write; no-op on any read/parse/write error).
