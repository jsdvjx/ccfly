---
"ccfly": minor
---

Add an in-band SNI egress environment check. The device now sends a nonce through the same local `:443` production relay, verifies the configured overlay node and source-selected account exit reported by byway, and only reports `path_ok` when local interception, that identity check, and a real upstream TLS handshake all succeed.
