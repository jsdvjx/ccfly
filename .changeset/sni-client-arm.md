---
"ccfly": minor
---

feat(sni): 客户端 SNI 出口 arm——据云端下发的 sni 段装本地 DNS 拦截器 + :443 透传，把 AI 域流量经 overlay 送到账号出口 byway-sni（真证书，无 HTTP 代理/无 MITM）。内嵌极小 Go DNS（intercept 域→loopback，其余转上游），系统解析指向三平台全实现：macOS `/etc/resolver` scoped、Windows NRPT scoped、Linux resolv.conf。有段则装、无段则幂等卸载；未收到 sni 段则完全 dormant，零回归。
