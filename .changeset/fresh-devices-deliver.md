---
"ccfly": patch
---

Add owner-gated web delivery of a ready Claude credential to a selected online device, plus `ccfly claude login --auto` for creating a 30-minute, one-time browser handoff that returns the finished login directly to its originating device. Both paths reuse the existing device-bound sealed-login and ack-destroy flow; the original CLI and web login workflows remain fully supported.
