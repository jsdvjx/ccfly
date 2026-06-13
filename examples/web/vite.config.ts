import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath } from 'node:url'

// ccfly 控制服务(`ccfly serve`)默认监听 127.0.0.1:7699。
// 本地调试可用 CCFLY_DEV_TARGET 覆盖(如指向另一个 `ccfly serve` 端口)。
const CCFLY = process.env.CCFLY_DEV_TARGET ?? 'http://127.0.0.1:7699'

// CCFLY_REACT_SRC=1 时把 @ccfly/react 直接别名到源码(packages/react/src),
// 让 Vite 编译源码:HMR/断点精确落到组件源(如 livestate.ts),且省掉 tsup --watch。
// 不设此环境变量则照旧消费 dist(生产 `vite build` 行为不变)。
const useReactSrc = process.env.CCFLY_REACT_SRC === '1'
const srcPath = (p: string) => fileURLToPath(new URL(`../../packages/react/src/${p}`, import.meta.url))
const repoRoot = fileURLToPath(new URL('../../', import.meta.url))

// 控制服务的所有 REST/SSE 路径——dev 时全部反代到本地 ccfly serve,
// 让 `pnpm dev` 与同源生产部署(Go embed 托管)行为一致。
// /term 是终端 WebSocket,需 ws: true。
const API_PATHS = [
  '/sessions',
  '/state',
  '/transcript',
  '/subtranscript',
  '/subagents',
  '/workflow',
  '/workflowagent',
  '/capture',
  '/cmdresult',
  '/image',
  '/info',
  '/sendkeys',
  '/start',
]

const proxy: Record<string, { target: string; changeOrigin: boolean; ws?: boolean }> = {}
for (const p of API_PATHS) proxy[p] = { target: CCFLY, changeOrigin: true }
// 终端 WebSocket。
proxy['/term'] = { target: CCFLY, changeOrigin: true, ws: true }

export default defineConfig({
  plugins: [react()],
  // base 用相对路径:产物由 Go 通过 embed 在站点根托管,相对资源路径最稳妥。
  base: './',
  ...(useReactSrc
    ? {
        resolve: {
          // 精确匹配,避免把 '@ccfly/react/style.css' 也吞掉(它单独映射到源 css)。
          alias: [
            { find: /^@ccfly\/react\/style\.css$/, replacement: srcPath('styles.css') },
            { find: /^@ccfly\/react$/, replacement: srcPath('index.ts') },
          ],
          // 别名到源码后,react 作为 peer 可能出现两份实例 → hook 报错;去重保证单例。
          dedupe: ['react', 'react-dom'],
        },
      }
    : {}),
  server: {
    port: 5174,
    proxy,
    // 允许 Vite 读取 examples/web 之外的源码(packages/react/src)。
    ...(useReactSrc ? { fs: { allow: [repoRoot] } } : {}),
  },
})
