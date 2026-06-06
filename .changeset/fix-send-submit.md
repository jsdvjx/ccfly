---
"@ccfly/react": patch
---

Fix messages not sending over a cloud/proxied connection. ControlBar submits
(send message / slash command / suggestion) now go through the REST `/sendkeys`
path — which types the text and presses a **separate** real `Enter` — instead of
a single WS `"text\r"` frame. tmux's `assume-paste-time` heuristic could treat the
bulk WS frame as a bracketed paste, so claude inserted the text without submitting
(input box showed text but "发不出去"). The REST path submits reliably and, being a
plain HTTP POST, traverses reverse proxies / the ccfly-cloud gateway like the
transcript GETs that already work. Raw terminal typing (which bypasses this layer)
is unaffected.
