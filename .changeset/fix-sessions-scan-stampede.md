---
"ccfly": patch
---

fix(control): 消除 /sessions 扫描缓存踩踏导致的内存暴涨

设备端控制服务的 `/sessions` 等接口每个请求都全量扫描所有会话的 jsonl。高并发下(hub/web 频繁轮询 + 大量会话 + 磁盘/内存被 swap 拖慢),数百个请求会同时各自把全部会话 transcript 解析进内存,峰值内存 ×并发数 放大到数 GB，拖垮整机(实测单机 315 并发把 ccfly 顶到 8G、swap 抖动使全机卡死)。

- `scanClaudeSessions` 加 single-flight：一轮全量扫描进行中，后到的并发调用等待并共享结果，不再各扫一遍。
- 摘要扫描 `scanOneSession` 对单行设 1 MiB 上限(超大行截断跳过但**不丢失其后的行**)；全文渲染(transcript/subagents/图片)仍走无上限路径，不受影响。
