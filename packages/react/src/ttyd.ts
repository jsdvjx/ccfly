// ttyd.ts —— 浏览器侧终端 WebSocket 客户端(自连,不走 iframe)。
//
// 连接目标:ccfly 自带的终端端点 —— config.wsBaseUrl + '/term?session=<tmux 会话名>'
// (替代旧的「外部 ttyd」:/d/<host>/<port>/ws?arg=…)。ccfly 的 Go 服务在 PTY 里跑
// `tmux new-session -A -s <session>`,**自己提供与 ttyd 协议兼容的帧**,zero 外部 ttyd。
// wsBaseUrl 由 CCFlyProvider 注入,形如 '/x/mac'(同源相对)或 'wss://hub/x/mac'(绝对);
// 不传则默认同源(见 config.ts)。
//
// attach 目标(关键):把 tmux 会话名作为 ?session= 传给 /term;ccfly 据此
// `tmux new-session -A -s <session>` —— 同名已在跑则 attach = 与本地/其它端实时镜像,不新开实例。
// 可选 ?cwd= / ?cmd=(首次创建该会话时透传给 tmux)。
//
// 帧协议(与 ttyd 1.7.x 兼容,故本文件编解码逻辑保持不变;子协议接受 'tty',binaryType=arraybuffer):
//   首帧(client→server):一条「无命令字节前缀」的 JSON 文本 {AuthToken, columns, rows}
//     (ccfly /term 见首字符 '{' 即认作握手帧,取 columns/rows 作初始 PTY 尺寸;AuthToken 忽略)。
//   client→server:'0'+data = INPUT(输入)、'1'+JSON{columns,rows} = RESIZE_TERMINAL。
//   server→client:首字节 '0' = OUTPUT(剩余 = 终端字节)、'1' = SET_WINDOW_TITLE、'2' = SET_PREFERENCES。
// 命令字节是 ASCII 字符 '0'/'1'/'2'(即 0x30/0x31/0x32),不是数值 0/1/2。

// ── 协议命令字节(ASCII) ──
const CMD = {
  // server → client
  OUTPUT: '0',
  SET_WINDOW_TITLE: '1',
  SET_PREFERENCES: '2',
  // client → server
  INPUT: '0',
  RESIZE_TERMINAL: '1',
} as const

export interface TtydHandlers {
  onOutput?: (data: string) => void
  onTitle?: (title: string) => void
  onOpen?: () => void
  onClose?: () => void
}

export interface TtydConn {
  sendInput: (text: string) => void
  resize: (cols: number, rows: number) => void
  close: () => void
  // 是否已完成握手(open + 已发首帧)。livestate 据此判断「降级」。
  ready: () => boolean
}

import { getConfig } from './config'

const enc = new TextEncoder()
const dec = new TextDecoder()

// wsBaseUrl(config 注入)→ 连到 ccfly 自带终端的绝对 ws(s)://…/term?session=… 地址。
// 支持三种 base 形态:
//   1) 已是 ws://… / wss://…   → 原样用。
//   2) http://… / https://…     → 协议换成 ws/wss。
//   3) 相对路径(/x/…)/ 空      → 同源(协议跟随页面 http/https)+ location.host 前缀。
// query:session(tmux 会话名,必带)+ 可选 cwd / cmd(首次创建该会话时透传给 tmux)。
function wsURL(session: string, opts?: { cwd?: string; cmd?: string }): string {
  const base = (getConfig().wsBaseUrl || '').replace(/\/$/, '')
  const params = new URLSearchParams({ session })
  if (opts?.cwd) params.set('cwd', opts.cwd)
  if (opts?.cmd) params.set('cmd', opts.cmd)
  const path = '/term?' + params.toString()
  if (/^wss?:\/\//i.test(base)) {
    return base + path
  } else if (/^https?:\/\//i.test(base)) {
    return base.replace(/^http/i, 'ws') + path
  } else {
    // 相对 / 空:同源拼协议 + host。
    const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const origin = scheme + '//' + location.host
    const rel = base ? (base.startsWith('/') ? base : '/' + base) : ''
    return origin + rel + path
  }
}

// connect —— 连到 ccfly /term 上的 tmux 会话 tmuxSession。
// tmuxSession 即 attach 目标(?session=);可选 cwd/resumeCmd 仅在该会话「首次创建」时透传给 tmux
// (?cwd= / ?cmd=);同名会话已在跑则 attach 镜像,这两个值被忽略(tmux new -A 的语义)。
// host 形参保留(签名兼容),不再参与 URL 构造(端点改由 config.wsBaseUrl + /term 决定)。
export function connect(
  host: string,
  tmuxSession: string,
  h: TtydHandlers,
  opts?: { cwd?: string; resumeCmd?: string; cols?: number; rows?: number },
): TtydConn {
  void host // 兼容旧签名,不再使用
  let ws: WebSocket | null = null
  let closed = false
  let handshook = false
  let cols = opts?.cols || 80
  let rows = opts?.rows || 24
  let backoff = 800 // 指数退避起点(ms)
  let reTimer = 0

  // 首帧:无前缀 JSON 文本 {AuthToken, columns, rows}。ttyd 以首字符 '{' 识别为鉴权帧。
  const sendFirstFrame = () => {
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    ws.send(JSON.stringify({ AuthToken: '', columns: cols, rows }))
    handshook = true
  }

  const open = () => {
    if (closed) return
    let sock: WebSocket
    try {
      sock = new WebSocket(wsURL(tmuxSession, { cwd: opts?.cwd, cmd: opts?.resumeCmd }), 'tty')
    } catch {
      schedule()
      return
    }
    sock.binaryType = 'arraybuffer'
    ws = sock
    handshook = false

    sock.onopen = () => {
      if (closed) {
        try {
          sock.close()
        } catch {
          /* ignore */
        }
        return
      }
      backoff = 800 // 连上 → 退避复位
      sendFirstFrame() // 重连后必须重发首帧(带最新 cols/rows)
      h.onOpen?.()
    }

    sock.onmessage = (ev) => {
      const d = ev.data
      // 文本帧极少见(ttyd 输出统一走二进制);为稳妥两种都处理。
      if (typeof d === 'string') {
        dispatch(d.charCodeAt(0), d.slice(1))
        return
      }
      const buf = new Uint8Array(d as ArrayBuffer)
      if (buf.length === 0) return
      const cmd = buf[0] // ASCII '0'/'1'/'2' 即 0x30/0x31/0x32
      const rest = buf.subarray(1)
      dispatchBytes(cmd, rest)
    }

    sock.onclose = () => {
      if (ws === sock) ws = null
      handshook = false
      h.onClose?.()
      schedule()
    }
    sock.onerror = () => {
      // onerror 后必跟 onclose;重连交给 onclose。
      try {
        sock.close()
      } catch {
        /* ignore */
      }
    }
  }

  // 二进制帧分派:cmd 是字节(0x30/0x31/0x32)。
  function dispatchBytes(cmd: number, rest: Uint8Array) {
    switch (String.fromCharCode(cmd)) {
      case CMD.OUTPUT:
        h.onOutput?.(dec.decode(rest))
        break
      case CMD.SET_WINDOW_TITLE:
        h.onTitle?.(dec.decode(rest))
        break
      case CMD.SET_PREFERENCES:
        // preferences:忽略(我们自管 xterm 参数,不吃 ttyd 下发的偏好)。
        break
      default:
        break
    }
  }
  // 文本帧分派(兜底)。
  function dispatch(cmdCode: number, rest: string) {
    switch (String.fromCharCode(cmdCode)) {
      case CMD.OUTPUT:
        h.onOutput?.(rest)
        break
      case CMD.SET_WINDOW_TITLE:
        h.onTitle?.(rest)
        break
      default:
        break
    }
  }

  // 指数退避重连(上限 15s)。
  function schedule() {
    if (closed) return
    if (reTimer) return
    reTimer = window.setTimeout(() => {
      reTimer = 0
      open()
    }, backoff)
    backoff = Math.min(backoff * 1.7, 15000)
  }

  open()

  return {
    sendInput(text: string) {
      if (!ws || ws.readyState !== WebSocket.OPEN) return
      // '0' + data。用字节拼接,避免命令字节与多字节 UTF-8 混排出错。
      const body = enc.encode(text)
      const out = new Uint8Array(1 + body.length)
      out[0] = CMD.INPUT.charCodeAt(0)
      out.set(body, 1)
      ws.send(out)
    },
    resize(c: number, r: number) {
      cols = c
      rows = r
      if (!ws || ws.readyState !== WebSocket.OPEN) return
      // '1' + JSON{columns,rows}
      const json = JSON.stringify({ columns: c, rows: r })
      const body = enc.encode(json)
      const out = new Uint8Array(1 + body.length)
      out[0] = CMD.RESIZE_TERMINAL.charCodeAt(0)
      out.set(body, 1)
      ws.send(out)
    },
    close() {
      closed = true
      if (reTimer) {
        clearTimeout(reTimer)
        reTimer = 0
      }
      if (ws) {
        try {
          ws.close()
        } catch {
          /* ignore */
        }
        ws = null
      }
    },
    ready() {
      return !!ws && ws.readyState === WebSocket.OPEN && handshook
    },
  }
}
