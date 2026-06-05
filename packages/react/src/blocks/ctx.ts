// 会话上下文:把 host/sid 透传给深层块(如 AgentCard 懒加载子 transcript 时需要),
// 免得逐层 props 钻孔。App 的 Session 在消息流外层用 Provider 注入。
import { createContext, useContext } from 'react'

export interface SessionCtx {
  host: string
  sid: string
}

export const SessionContext = createContext<SessionCtx>({ host: '', sid: '' })

export function useSession(): SessionCtx {
  return useContext(SessionContext)
}
