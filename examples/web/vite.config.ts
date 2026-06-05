import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// ccfly 控制服务(`ccfly serve`)默认监听 127.0.0.1:7699。
const CCFLY = 'http://127.0.0.1:7699'

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
  server: {
    port: 5174,
    proxy,
  },
})
