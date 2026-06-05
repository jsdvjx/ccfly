// liveconn.ts —— 跨组件单例句柄:常驻 LiveTerm 的 xterm 实例、其 ttyd 连接、显隐/refit 控制。
//
// 抽到独立模块(不含 React/xterm 运行时依赖,只一个可变对象 + 类型)以便:
//   - sendkeys.ts(统一发键层)拿到当前 conn 走 WS INPUT 轨,而不必 import LiveTerm.tsx(避免循环 + 拖入 React)。
//   - App.tsx 顶栏「⌨ 终端」按钮调 show/hide/refit 做秒切。
// 单例即可:同一时刻只有一个会话在看,LiveTerm 挂载时填充、卸载时清空。
import type { Terminal } from '@xterm/xterm'
import type { TtydConn } from './ttyd'

export interface LiveTermHandle {
  term: Terminal | null
  conn: TtydConn | null // 当前 ttyd 连接(WS INPUT 轨发键用);未连上为 null
  refit: () => void
  show: () => void
  hide: () => void
  isShown: () => boolean
}

export const liveTermHandle: LiveTermHandle = {
  term: null,
  conn: null,
  refit: () => {},
  show: () => {},
  hide: () => {},
  isShown: () => false,
}
