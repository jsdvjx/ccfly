---
"ccfly": patch
---

New session can now choose its directory via a filesystem browser, in all three surfaces.

- `ccfly a` picker: "＋ 新建会话…" (and the `n` key) opens a directory browser — navigate with ↑↓ / Enter / ← (parent), `n` creates a session in the current directory; `p`/`y` still toggle permission mode / skip.
- `ccfly new`: with no `[dir]` arg it opens the same browser (a given arg still creates directly).
- Web UI: a "＋ 新建" button in the workspace opens a directory-browser dialog, then starts a fresh claude there and switches to it.

Backed by two new local control endpoints: `GET /dirs?path=` (list subdirectories) and `POST /new {cwd, permission_mode?, skip_permissions?}` (start a fresh claude detached, poll the panemap, return the real session id).
