---
"ccfly": minor
---

Unify SNI interception on all three platforms around one mechanism: an embedded CoreDNS on 127.0.0.1:53 whose policy service pulls the OSS domain list itself and hot-reloads on change. Windows moves from hosts-file pinning to interface DNS pointing at the local resolver (with backup/restore and fail-open secondary); macOS's root helper now owns the policy service permanently and rewrites scoped /etc/resolver entries on policy updates without agent involvement. The agent no longer fetches or distributes domain lists; cloud-delivered intercept/upstream fields are ignored (account/exit unchanged).
