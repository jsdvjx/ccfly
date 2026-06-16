---
"ccfly": patch
---

`ccfly new` and `ccfly a` can now set claude permission options, and the
interactive picker can create new sessions.

- New flags on both `ccfly new` and `ccfly a`: `--permission-mode <default|acceptEdits|plan|bypassPermissions>` and `--dangerously-skip-permissions` (alias `--yolo`), passed through to claude. They apply when a session is launched (new, or offline resume); attaching to an already-running session leaves its mode untouched.
- The `ccfly a` picker gained a "＋ new session" entry at both levels (current dir / project dir; shortcut `n`) and a footer to toggle the permission options live (`p` cycles permission-mode, `y` toggles skip) — applied to whatever you launch.

Also: the embedded web UI now renders `!command` bash echoes (`<bash-input>`/`<bash-stdout>`/`<bash-stderr>`) as IN/OUT/ERR cards instead of dropping them.
