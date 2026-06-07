// sendkeys.ts —— 统一发键层(双轨):把「往里世界送东西」收口到一处,按内容选通道。
//
// 两条轨:
//   1) 纯文本(text、无 enter)—— 走 ttyd WS 的 INPUT 帧(conn.sendInput,'0'+data)。
//      「打字」语义:直接把字符灌进 pane 的 stdin,毫秒级、无需后端中转。
//   2) 语义键(keys) 或「提交」(enter:true)—— 走 /sendkeys(后端 tmux send-keys)。
//      方向键/Esc/^C 这类「控制键」用 tmux 的具名键最稳(转义序列各终端不一,后端具名映射是权威)。
//      提交(text+enter,如发消息/斜杠命令)也走这里:后端先 `send-keys -l -- text` 再发**独立的** `Enter`。
//
// 为什么「提交」不走 WS 而走 /sendkeys(关键修复):
//   WS 轨把 "text\r" 作为「一帧」灌进 attach 端 PTY,字节几乎同刻到达。tmux 的 assume-paste-time
//   (默认 1ms)会把这种「瞬时一大串」判作**粘贴**,而 claude TUI 开了 bracketed-paste(DECSET 2004),
//   于是被包进 \e[200~…\e[201~ → claude 把文本(连同那个 \r)当**粘贴内容塞进输入框、并不提交**。
//   表现:输入框里有字、却「发不出去」。改走 /sendkeys 后,文本与 Enter 是**两次独立的 send-keys**,
//   Enter 是真正的按键事件(不触发粘贴启发),claude 正常提交。
//   附带收益:/sendkeys 是普通 HTTP POST,经云网关/反代比 WS 双向桥接更稳(与能正常拉取的 transcript GET 同路)。
//
// 降级(liveDegraded:WS 没连上 / 未握手 / 还没收到输出):一切都走 /sendkeys(后端把 text/enter/keys 都能发)。
//   —— 这正是 P1+P2 之前的老路径,保留为兜底,确保 WS 不可用时控件仍能驱动里世界。
//
// 调用方:ControlBar 的 act()、可见终端的键盘处理,统一经此。返回 Promise<boolean>(是否成功送达)。
import { sendKeys, type SendResult } from './api'
import { liveTermHandle } from './liveconn'
import { useLiveStore } from './livestate'

// 透出 SendResult({ok, kind?}):sendAct/sendKeys 的返回类型,消费方据此区分成功 / 409 拒发 / 失败。
export type { SendResult } from './api'

export interface SendBody {
  text?: string
  keys?: string[]
  enter?: boolean
  // clear:仅「原子提交」(配 enter:true)时置真 —— 后端打字前先清空里世界输入行(根因 A)。
  // 只在 enter:true 时有意义,而 enter:true 已强制走 /sendkeys(下方),故 clear 随 body 自动转发,
  // 无 WS 专属代码。纯 WS 打字轨(无 enter)从不带 clear。
  clear?: boolean
}

// WS 是否此刻可用作发键通道:有 conn、已握手 ready、且 store 未判降级。
function wsUsable(): boolean {
  const conn = liveTermHandle.conn
  return !!conn && conn.ready() && !useLiveStore.getState().degraded
}

// 统一发送。返回 SendResult({ok, kind?}):提交可能被 server floor 以 409 拒发(带真实 kind),
// ControlBar 据此冒真实原因并保留草稿;非提交轨(纯打字成功)恒返回 {ok:true}。
//  - 含 keys(语义键)、含 enter(提交)、或 WS 不可用 → 走 /sendkeys(后端 tmux send-keys;提交时文本与 Enter 分两次发,可靠提交)。
//  - 否则(纯文本、无 enter、WS 可用)→ 走 WS INPUT 帧:字符原样灌入 stdin(低延迟「打字」)。
export async function sendAct(host: string, tsess: string, body: SendBody): Promise<SendResult> {
  const hasKeys = !!(body.keys && body.keys.length)
  // 语义键 / 提交(enter)/ WS 不可用 → 后端轨。
  // enter 必走后端:WS 的 "text\r" 一帧会被 tmux 当粘贴(见顶部注释)→ claude 不提交;
  // 后端 send-keys 把 Enter 作为独立按键发,才真正提交。clear 仅配 enter 出现,故只在这条轨上传到后端。
  if (hasKeys || body.enter || !wsUsable()) {
    return sendKeys(host, tsess, body)
  }
  // WS 轨:纯文本(无 enter)直灌 stdin —— 单字符/增量打字,不涉及提交,故 clear 在此恒不出现。
  const conn = liveTermHandle.conn!
  const payload = body.text || ''
  if (payload === '') return { ok: true }
  try {
    conn.sendInput(payload)
    return { ok: true }
  } catch {
    // WS 写失败 → 退回后端轨,别丢这次操作。
    return sendKeys(host, tsess, body)
  }
}
