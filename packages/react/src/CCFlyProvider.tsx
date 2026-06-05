// CCFlyProvider —— 顶层配置注入。把控制服务端点 / 存储前缀 / tmux 命名等参数注入整棵 SessionView 子树,
// 取代上游里硬编码的 /x/<host>、/d/<host>/7681、jv:、库名(IndexedDB)。
//
// 用法:
//   <CCFlyProvider config={{ baseUrl: '/x/mac' }}>
//     <SessionView sid={sid} />
//   </CCFlyProvider>
// 终端默认走 ccfly 自带的 /term(PTY+tmux,ttyd 帧兼容),不依赖外部 ttyd;wsBaseUrl 缺省 = baseUrl。
//
// 它做两件事:
//   1) 把解析后的完整配置写进模块级单例(config.ts setConfig),供 api.ts/ttyd.ts/idb.ts/sendkeys.ts
//      等「React 树外也会被调用」的纯函数层读取。
//   2) 把同一份配置放进 React context(useCCFly),供组件内取用(如取 storagePrefix、terminalUrl)。
//
// P0:模块级单例 → 单实例消费 OK。多实例化见 config.ts 顶部的 P0.5 TODO。
import { createContext, useContext, useMemo, type ReactNode } from 'react'
import { setConfig, type CCFlyConfig } from './config'

// 消费方传入的配置(大多可选,Provider 补默认)。唯一基本必填:baseUrl(控制服务前缀)。
export interface CCFlyProviderConfig {
  // 控制服务前缀(替代 /x/<host>)。形如 '/x/mac' 或 'https://hub/x/mac'。不含结尾斜杠。
  baseUrl: string
  // ccfly 自带终端 WebSocket 的 base(不含 '/term')。缺省 = baseUrl(终端与 REST 同源同前缀)。
  // 仅当终端 WS 与 REST 走不同前缀/主机时才需显式传(如 REST 同源、WS 走另一反代)。
  wsBaseUrl?: string
  // 会话列表接口(替代 /api/claude-sessions)。不传 → fetchSessions 返回 []。
  sessionsUrl?: string
  fetch?: typeof fetch
  tmuxName?: (sid: string) => string
  terminalUrl?: (sid: string, cwd?: string) => string
  resumeCmd?: (sid: string) => string
  // localStorage/sessionStorage/IndexedDB 键前缀(替代 'jv:')。缺省 'ccfly:'。
  storagePrefix?: string
}

function resolve(c: CCFlyProviderConfig): CCFlyConfig {
  const baseUrl = c.baseUrl.replace(/\/$/, '')
  // 终端 WS 默认与 REST 同前缀(ccfly 自带 /term 与其它端点同一个 Go 服务)。
  const wsBaseUrl = (c.wsBaseUrl ?? baseUrl).replace(/\/$/, '')
  const tmuxName = c.tmuxName ?? ((sid: string) => 'cc-' + sid.slice(0, 8))
  const resumeCmd = c.resumeCmd ?? ((sid: string) => 'claude --resume ' + sid)
  // 默认无外部终端直链:ccfly /term 是 WS,不可直接开新标签。降级时 UI 据空串隐藏「打开终端」。
  // 消费方若另起了可开标签的网页终端可覆盖此项。
  const terminalUrl = c.terminalUrl ?? (() => '')
  return {
    baseUrl,
    wsBaseUrl,
    sessionsUrl: c.sessionsUrl,
    fetch: c.fetch ?? ((...a: Parameters<typeof fetch>) => globalThis.fetch(...a)),
    tmuxName,
    terminalUrl,
    resumeCmd,
    storagePrefix: c.storagePrefix ?? 'ccfly:',
  }
}

const Ctx = createContext<CCFlyConfig | null>(null)

export function CCFlyProvider({ config, children }: { config: CCFlyProviderConfig; children: ReactNode }) {
  // 解析 + 写模块级单例。useMemo 让同一份输入只解析一次;输入变化即重解析并刷新单例。
  const resolved = useMemo(() => {
    const r = resolve(config)
    setConfig(r) // 同步写模块级单例(纯函数层从这读)
    return r
    // 依赖到具体字段:对象字面量每次新引用,逐字段比对避免每渲染都 setConfig。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    config.baseUrl,
    config.wsBaseUrl,
    config.sessionsUrl,
    config.fetch,
    config.tmuxName,
    config.terminalUrl,
    config.resumeCmd,
    config.storagePrefix,
  ])
  return <Ctx.Provider value={resolved}>{children}</Ctx.Provider>
}

// 组件内取配置。未包 Provider 时返回 null(api 层仍有模块级默认兜底,但组件应在 Provider 下使用)。
export function useCCFly(): CCFlyConfig | null {
  return useContext(Ctx)
}
