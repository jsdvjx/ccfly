---
"ccfly": minor
---

Make cloud-CDP OAuth the default `ccfly claude login`: the local Claude CLI owns the complete OAuth flow and writes its credential locally, while ccfly sends the short-lived authorize request to the cloud workbench, lets the user choose one of their Claude accounts, starts that account's fixed-identity browser for hCaptcha/Authorize, automatically pastes the one-shot result back into Claude, and destroys it after local persistence. The previous sealed inventory delivery remains available explicitly as `--credential`, and `--auto` remains available.
