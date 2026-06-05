# examples/web

默认的 ccfly **表世界**:一个 Vite + React + TS 应用,消费 [`@ccfly/react`](../../packages/react),
由 `ccfly serve` 同源(same-origin)托管。

- 落地页 = 会话选择器:取 `GET /sessions`,按最近活动倒序,`live`(有同名 tmux 在跑)置顶分组。
- 点某行 → 渲染 `<SessionView/>`(完整会话视图:消息流 + 控件层 + 镜像终端)+ `<CCFlyHosts/>`(四个单例弹层 host)。
- 同源:`CCFlyProvider` 的 `baseUrl` 传空串,所有 REST/SSE/WS(含 `/term`)走相对路径,指向托管本 SPA 的同一个 Go 服务。

## 开发

```sh
# 1) 起本地控制服务(默认 127.0.0.1:7699)
ccfly serve            # 或:pnpm -w build:go && ./bin/ccfly serve

# 2) 跑本示例(Vite dev,API 反代到 127.0.0.1:7699)
pnpm install           # 在 monorepo 根执行(本包是 pnpm workspace 成员)
pnpm --filter web dev  # 或在本目录:pnpm dev
```

## 构建

```sh
pnpm --filter web build   # 产物在 examples/web/dist/
```

产物用相对资源路径(`base: './'`),供 Go 端 `//go:embed dist/*` 在站点根托管。
