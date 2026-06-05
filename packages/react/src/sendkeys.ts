// sendkeys.ts —— 统一发键层(双轨):把「往里世界送东西」收口到一处,按内容选通道。
//
// 两条轨:
//   1) 文本 / 回车(text、enter)—— 走 ttyd WS 的 INPUT 帧(conn.sendInput,'0'+data)。
//      这是「打字」语义:直接把字符灌进 pane 的 stdin,毫秒级、无需后端中转。
//      回车作为「提交」时,等价于在文本尾部追加 "\r"(CR);claude TUI 与 tmux 都按 Enter 处理。
//   2) 语义键(keys: ['Escape'|'Up'|'Down'|'Left'|'Right'|'Enter'|'C-c'...])—— 走 /sendkeys(后端 tmux send-keys)。
//      方向键/Esc/^C 这类「控制键」用 tmux 的具名键最稳(转义序列各终端不一,后端具名映射是权威),不走 WS 裸字节。
//      注意:keys 里若出现 'Enter',这是「把 Enter 当一次按键」(如菜单确认),仍走 /sendkeys —— 与「文本+enter 提交」区分。
//
// 降级(liveDegraded:WS 没连上 / 未握手 / 还没收到输出):一切都走 /sendkeys(后端把 text/enter/keys 都能发)。
//   —— 这正是 P1+P2 之前的老路径,保留为兜底,确保 WS 不可用时控件仍能驱动里世界。
//
// 调用方:ControlBar 的 act()、可见终端的键盘处理,统一经此。返回 Promise<boolean>(是否成功送达)。
import { sendKeys } from './api'
import { liveTermHandle } from './liveconn'
import { useLiveStore } from './livestate'

export interface SendBody {
  text?: string
  keys?: string[]
  enter?: boolean
}

// WS 是否此刻可用作发键通道:有 conn、已握手 ready、且 store 未判降级。
function wsUsable(): boolean {
  const conn = liveTermHandle.conn
  return !!conn && conn.ready() && !useLiveStore.getState().degraded
}

// 统一发送。
//  - 降级 或 含 keys(语义键)→ 整条走 /sendkeys(后端能同时处理 text/enter/keys,顺序与老路径一致)。
//  - 否则(仅 text/enter,WS 可用)→ 走 WS INPUT 帧:text 原样灌入,enter 追加 "\r"。
export async function sendAct(host: string, tsess: string, body: SendBody): Promise<boolean> {
  const hasKeys = !!(body.keys && body.keys.length)
  // 语义键 或 WS 不可用 → 后端轨(老路径,完全兼容)。
  if (hasKeys || !wsUsable()) {
    return sendKeys(host, tsess, body)
  }
  // WS 轨:纯文本/回车直灌 stdin。
  const conn = liveTermHandle.conn!
  const payload = (body.text || '') + (body.enter ? '\r' : '')
  if (payload === '') return true
  try {
    conn.sendInput(payload)
    return true
  } catch {
    // WS 写失败 → 退回后端轨,别丢这次操作。
    return sendKeys(host, tsess, body)
  }
}
