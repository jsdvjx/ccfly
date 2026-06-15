---
"ccfly": patch
---

Log device-config refresh failures instead of swallowing them. `refreshConfig` now logs when the `GET /api/device/config` request errors, returns non-200, or yields unparseable JSON. The call still degrades gracefully (keeps existing State), but the failure is now observable — previously a silent return made it hard to tell why cloud-advertised config (proxy port/CA) wasn't being applied.
