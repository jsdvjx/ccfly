import { defineConfig } from 'vitest/config'

// 引擎核心(pre.ts / 各 resolve)是纯逻辑,跑在 node 环境即可;真正的读屏夹具用 frameBuilder 构造。
export default defineConfig({
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'node',
  },
})
