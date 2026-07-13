---
"ccfly": minor
---

feat(sni): 客户端 SNI 出口 arm——据云端下发的 sni 段装本地 DNS 拦截器 + :443 透传，把 AI 域流量经 overlay 送到账号出口 byway-sni（真证书，无 HTTP 代理/无 MITM）。有段则装、无段则幂等卸载；Linux 改 resolv.conf 指向本地（备份+次级上游 fail-open），非 Linux 暂 no-op。未收到 sni 段则完全 dormant，零回归。
