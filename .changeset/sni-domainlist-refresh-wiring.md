---
"ccfly": patch
---

Refresh the SNI intercept domain list on device-config refresh and on the 15s policy ticker, re-arming local interception as soon as the OSS policy changes instead of waiting for a full reconnect.
