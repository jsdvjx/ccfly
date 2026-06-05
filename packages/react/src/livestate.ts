// livestate.ts —— 客户端 detectState:把 agent/ctrlstate.go 的判断器移植到浏览器,直接吃 xterm 解析后的 buffer。
//
// 与后端的本质差异:
//   后端吃「tmux capture -e」的带 ANSI 原文,先 stripANSI 再正则;suggest 靠逐字符扫 SGR(dim/reverse)。
//   浏览器里 xterm 已把 ANSI 解析进 buffer(每格带属性),故:
//     - 文本 = line.translateToString(true)(已是无色干净文本,无需 stripANSI);
//     - suggest 的 dim/reverse 直接读 cell.isDim()/cell.isInverse(),不再解析转义序列。
//
// 产出与 api.ts 的 CtrlState 同构(kind/title/options/effort/actions/verb/tokens/tip/elapsed/hint/suggest),
// 供 P3 切换 ControlBar/AgentDock 的消费源。本阶段只产出与暴露,不改消费方。
import { create } from 'zustand'
import type { Terminal, IBufferLine } from '@xterm/xterm'
import type { CtrlState } from './api'

// ── 正则:与 ctrlstate.go 逐条对齐 ──
const reOpt = /^\s*(❯|›|>)?\s*(\d+)[.)]\s+(\S.*?)\s*$/
const reEffort = /^\s*\S\s+(.+?effort.*?)\s*←\/→\s*to adjust\s*$/i
const reFooter = /(\b(esc|enter)\b.*\bto\b|←\/→\s*to adjust)/i
const reBusy = /esc to interrupt/i
// spinner 行:任意字形 + 大写开头单词 + …(如 "✢ Zesting…")。
const reVerb = /^\s*\S\s+([A-Z][a-zA-Z]+)…/
const reTokens = /([\d.]+[kKmM]?)\s*tokens/
const reTip = /\bTip:\s*(.+?)\s*$/i
const reElapsed = /\b(\d+)s\b/
const reTitle = /^(select|choose|pick|do you|would you|permission|claude needs)/i
const reEnterTo = /Enter\s+to\s/i
const reSessOnly = /\bs\s+to\s+use\s+this\s+session/i
const reWS = /\s+/g
// 「确认看到输入框」的行定位:claude 空闲时底部有个框,框内首行以 "❯ " 起头(NBSP 或普通空格)。
// 与下面 reInputLine 同源,但语义不同:reInputLine 用于 suggest 定位(dim 鬼影),这里用于
// 「确认空闲」—— 只有真在可见屏看到这条输入行,detectState 才敢把状态判成 input(idle)。
// 放宽:行首可有空白;❯ 后跟空格/NBSP 再带任意内容,或就是裸 ❯(空框尾随 NBSP 被 trimRight 掉)。
// 为何放宽 —— 这是 /context 冻结 bug 的根因:跑完 /context、/model 等本地命令后,claude 回到底部
// 输入框,框上方塞满静态输出(⛀⛁⛶ 区块、"Estimated usage by category" 表、力度块),这些上方内容
// 既非 busy 也非 select,不被识别。原 reInputPrompt 死认「❯ 在第 0 列、其后必跟一个空格/NBSP」,
// 一旦因 box 边框/主题缩进,或 translateToString(true) 把空输入框尾随的 NBSP 当空白裁掉(只剩裸
// "❯")而漏认,hasIdlePrompt 就返 false → detectState 标 certain:false → store 保留上一个 busy →
// 看着「卡死」。放宽后只要看到输入框就确凿判 input(与后端 ctrlstate.go「看到框即空闲」对齐),
// 同时仍要求确凿看到框(非空缓冲),保留抗 reflow/重连瞬态的能力。
const reInputPrompt = /^\s*❯(?:[\u0020\u00a0].*)?\s*$/
// 输入框底栏提示行(空闲框下沿,如 "? for shortcuts · ← for agents" / "… to send" / "shift+tab")。
// 第二条独立的「确认空闲」信号:输入行因渲染瞬态没匹配上 reInputPrompt 时,尾部见此提示也算确凿空闲。
// busy 行是 "esc to interrupt"(已被 reBusy 先吃),select 行是 "Enter to …/esc to cancel"(已先判 select),
// 故此 hint 只在「非 busy、非 select」时生效,不抢 busy/select。
const reInputHint = /(\?\s*for\s+shortcuts|for\s+agents|\bto\s+send\b|shift\s*\+\s*tab)/i
// 输入行定位:行首 "❯" 后跟「NBSP(U+00A0)或普通空格(U+0020)」。
// 为何放宽:后端吃 tmux 原文,NBSP 与普通空格泾渭分明,故 reInputLine 死认 NBSP 即可区分
// 真实输入行 vs 建议/历史气泡(后两者用普通空格)。但浏览器侧文本来自 xterm 的
// translateToString/getChars,可能把 U+00A0 归一成普通空格 → 死认 NBSP 会永不命中,
// WS 在线态的 ✨ suggest 静默失效。故这里对 NBSP 与普通空格都放宽匹配,仅用来「定位输入行」。
// 真正区分 suggest(dim 鬼影)vs 真实输入,仍靠 cell 的 dim/inverse 属性(见 suggestFromLine)——
// 那才是判定信号;放宽不会误判:非建议行在 suggestFromLine 里无 dim 段会直接返回 ''。
const reInputLine = /^❯[\u0020\u00a0]/

// 可见区行范围 [start, end):对齐后端 `tmux capture-pane -p -e`(无 -S,仅当前可见屏 ~rows 行)。
// 锚点用 baseY(滚到底时视口顶行的 buffer 索引)而非 viewportY:LiveTerm 是恒在底部的无头镜像,
// 永不向上滚,故 baseY 就是「当前可见屏」的顶。扫 baseY..baseY+rows 这一屏,杜绝吸到滚上去的
// 陈旧 spinner/Tip/tokens/elapsed/effort/suggest(它们都只在当前屏有效)。
function visibleRange(term: Terminal): { start: number; end: number } {
  const buf = term.buffer.active
  const start = Math.max(0, buf.baseY)
  const end = Math.min(buf.length, start + term.rows)
  return { start, end }
}

// 从 xterm buffer 抽出可见区文本行(剥色后)。
function readLines(term: Terminal): { texts: string[]; lineCount: number } {
  const buf = term.buffer.active
  const texts: string[] = []
  // 仅取当前可见屏(与后端一致):全扫循环的「行集合」就是这一屏,不含 scrollback。
  const { start, end } = visibleRange(term)
  for (let y = start; y < end; y++) {
    const line = buf.getLine(y)
    texts.push(line ? line.translateToString(true) : '')
  }
  return { texts, lineCount: end - start }
}

// 尾窗 k 行(对齐 Go 的 tail)。
function tail(lines: string[], k: number): string[] {
  const n = lines.length
  return n - k < 0 ? lines : lines.slice(n - k)
}

// rstrip 全文尾部空白后按行切(对齐 Go 的 strings.TrimRight + Split)。
function toLines(texts: string[]): string[] {
  // texts 已是逐行;去掉末尾连续空行(translateToString 已逐行 trimRight)。
  let end = texts.length
  while (end > 0 && texts[end - 1].trim() === '') end--
  return texts.slice(0, end)
}

// ── suggest:在「真实输入行(❯+U+00A0)」上读 dim 段为建议主体,紧贴其前的 inverse 单字并回建议首字 ──
// 完全对应 ctrlstate.go 的 suggestFromInputLine,但属性来自 xterm cell(isDim/isInverse)而非 SGR 解析。
function parseSuggest(term: Terminal): string {
  const buf = term.buffer.active
  // 仅在当前可见屏找输入行(对齐后端「仅可见屏」),不吸滚上去的陈旧鬼影。
  const { start, end } = visibleRange(term)
  for (let y = start; y < end; y++) {
    const line = buf.getLine(y)
    if (!line) continue
    const text = line.translateToString(true)
    if (!reInputLine.test(text)) continue
    const s = suggestFromLine(line)
    if (s) return s
  }
  return ''
}

// 逐格扫一条输入行:收集 dim 段(建议主体);记最近 inverse 单字,dim 段开启时并入建议首字。
function suggestFromLine(line: IBufferLine): string {
  let dimBody = ''
  let head = '' // 紧贴 dim 之前的 inverse 单字(光标块压住的建议首字)
  let dimStarted = false
  let lastInverse = ''
  const len = line.length
  const cell = line.getCell(0)
  if (!cell) return ''
  for (let x = 0; x < len; x++) {
    const c = line.getCell(x, cell)
    if (!c) continue
    const chars = c.getChars()
    if (chars === '') continue // 空格/空格(宽字符尾格)等
    if (c.isDim()) {
      if (!dimStarted) {
        dimStarted = true
        head = lastInverse // dim 段开始 → 并入刚压在光标下的首字
      }
      dimBody += chars
    } else if (c.isInverse()) {
      lastInverse = chars // 记最近反显单字符,dim 段开启时可能并入
    }
  }
  const body = dimBody.trim()
  if (body === '') return '' // 无 dim 段 → 空框或真实输入,均非建议
  let sug = (head.trim() + body).trim()
  if (sug.startsWith('❯')) sug = sug.slice(1).trim()
  return sug
}

// detectState 的产物:除了 CtrlState 本体,带一个「这次判定是否确凿」的信号。
//   certain:true  —— 确凿看到了 busy / select / 清晰输入框(空闲)。store 可放心覆盖。
//   certain:false —— 既无 busy/select,又没看到清晰输入框(缓冲空 / 半重绘 / reflow 瞬态)。
//     这种「不确定」帧不能把 busy 降级成 input,store 应保留 last-known(见 applyDetect)。
// 之所以不直接在 CtrlState.kind 上加 'unknown':CtrlState 是与后端 /state 同构的契约(api.ts),
// 消费方(ControlBar/AgentDock)只认 offline/busy/select/input。把「确凿与否」收在 store 边界,
// 对外仍只暴露四态的 CtrlState,消费方零改动。
export interface DetectResult {
  state: CtrlState
  certain: boolean
}

// 「确认看到空闲输入框」:可见屏内存在以 "❯" 起头的输入行(reInputPrompt),或尾部存在输入框底栏
// 提示行(reInputHint,如 "? for shortcuts")。任一命中即认作确凿空闲。
// 这是把状态判成 input(idle)的硬门槛 —— 仅「这一帧没读到 busy」不算空闲(可能是 reflow/空缓冲)。
//
// 两路信号互为兜底:静态满屏(/context 等本地命令跑完)时,输入框 "❯" 行确实在屏,prompt 路命中;
// 万一某帧 "❯" 行因重绘瞬态没匹配上,底栏 "? for shortcuts" 提示一般还在,hint 路兜住,不再误判
// 不确凿而保留 busy。两路都只在「非 busy(reBusy 先吃)、非 select(reFooter+opts/effort 先判)」
// 后才被 detectState 调用,故不会抢 busy/select。
function hasIdlePrompt(term: Terminal): boolean {
  const buf = term.buffer.active
  const { start, end } = visibleRange(term)
  for (let y = start; y < end; y++) {
    const line = buf.getLine(y)
    if (!line) continue
    if (reInputPrompt.test(line.translateToString(true))) return true
  }
  // 兜底:尾部若见输入框底栏提示行,同样算确凿空闲。
  const last = end - 1
  for (let y = last; y >= start && y >= last - 6; y--) {
    const line = buf.getLine(y)
    if (!line) continue
    if (reInputHint.test(line.translateToString(true))) return true
  }
  return false
}

// ── detectState:对齐 ctrlstate.go 的 detectState,吃 xterm buffer ──
// 返回 DetectResult(state + certain),由 store.applyDetect 决定是否覆盖 last-known。
export function detectState(term: Terminal): DetectResult {
  const suggest = parseSuggest(term)
  const { texts } = readLines(term)
  const lines = toLines(texts)
  const n = lines.length

  // busy:尾 8 行含 "esc to interrupt"。
  let busy = false
  for (const ln of tail(lines, 8)) {
    if (reBusy.test(ln)) {
      busy = true
      break
    }
  }
  if (busy) {
    const st: CtrlState = { kind: 'busy' }
    for (const ln of lines) {
      if (!st.verb) {
        const m = reVerb.exec(ln)
        if (m) st.verb = m[1]
      }
      if (!st.tip) {
        const m = reTip.exec(ln)
        if (m) st.tip = m[1].trim()
      }
      // token 仅从 spinner / interrupt 行取。
      if (!st.tokens && (reBusy.test(ln) || reVerb.test(ln))) {
        const m = reTokens.exec(ln)
        if (m) st.tokens = m[1]
      }
      // 运行时间同样仅限 spinner / interrupt 行。
      if (!st.elapsed && (reBusy.test(ln) || reVerb.test(ln))) {
        const m = reElapsed.exec(ln)
        if (m) st.elapsed = m[1] + 's'
      }
    }
    return { state: st, certain: true }
  }

  // 模态门槛:最后 6 行须有底栏。
  let modal = false
  for (const ln of tail(lines, 6)) {
    if (reFooter.test(ln)) {
      modal = true
      break
    }
  }

  if (modal) {
    // 横向力度
    let effort = ''
    for (const ln of lines) {
      const m = reEffort.exec(ln)
      if (m) {
        effort = m[1].replace(reWS, ' ').trim()
        break
      }
    }
    // 编号选项:自底向上,≥2、从 1 起、含 ❯ 当前项。
    let opts: { num: string; label: string; cur: boolean }[] = []
    let started = false
    let firstIdx = -1
    for (let i = n - 1; i >= 0; i--) {
      const m = reOpt.exec(lines[i])
      if (m) {
        opts.unshift({ num: m[2], label: m[3].replace(reWS, ' '), cur: !!m[1] })
        started = true
        firstIdx = i
      } else if (started) {
        if (lines[i].trim() === '') continue
        break
      }
    }
    const hasCur = opts.some((o) => o.cur)
    if (!(opts.length >= 2 && opts[0].num === '1' && hasCur)) opts = []

    if (opts.length > 0 || effort !== '') {
      const joinTail = tail(lines, 6).join(' ')
      const actions: { label: string; keys?: string[]; text?: string }[] = []
      if (reEnterTo.test(joinTail)) actions.push({ label: '确认', keys: ['Enter'] })
      if (reSessOnly.test(joinTail)) actions.push({ label: '本次', text: 's' })
      actions.push({ label: '取消', keys: ['Escape'] })

      let title = ''
      if (firstIdx >= 0) {
        for (let y = firstIdx - 1; y >= 0 && y >= firstIdx - 6; y--) {
          const t = lines[y].trim()
          if (t === '') continue
          if (
            t.endsWith('?') ||
            t.endsWith(':') ||
            t.endsWith('：') ||
            reTitle.test(t)
          ) {
            title = t
            break
          }
        }
      }
      if (title === '' && opts.length === 0 && effort !== '') title = '调整力度'
      return { state: { kind: 'select', title, options: opts, effort, actions }, certain: true }
    }
  }

  // 走到这里:既无 busy,也无 select。是不是真「空闲可输入」?
  // 只有确认看到清晰输入框(❯ 输入行存在)才算确凿空闲;否则这帧很可能是 reflow / 半重绘 /
  // 空缓冲的瞬态(尺寸跳动、刚重连),不能据此把状态降级成 input —— 标 certain:false,
  // 让 store 保留 last-known(尤其别把 busy 误降成 input)。
  const idle = hasIdlePrompt(term)
  return { state: { kind: 'input', suggest }, certain: idle }
}

// ── zustand store ──
interface LiveState {
  state: CtrlState
  // 降级:WS 未连上 / 未握手 / 还没收到任何输出 → true(此时 P3 应回退到后端 /state 轮询)。
  degraded: boolean
  // settle 窗口截止时刻(epoch ms)。> now 时,所有解析视为 provisional:即便 certain 也只「升级」
  // 不「降级」(保留 last-known),用于扛尺寸跳动 / reflow 的瞬态。0 = 无 settle 窗口。
  settleUntil: number
  // 直接覆盖(老接口,保留;但已不在热路径用)。
  setState: (s: CtrlState) => void
  // 解析入口:按「确凿与否 + settle 窗口」决定覆盖还是保留 last-known。
  applyDetect: (r: DetectResult) => void
  // 开一个 ms 毫秒的 settle 窗口(尺寸跳动 / 重连时调用)。多次调用取较晚截止。
  markSettle: (ms: number) => void
  setDegraded: (d: boolean) => void
  resetLive: () => void
}

// 同 kind 的两个 input 是否在「实质内容」上变了(suggest)。busy/select 的字段(verb/tokens/options…)
// 也会变,但那属于「同态刷新」,直接放行即可;这里只用于避免无谓的引用变更触发重渲染。
function sameState(a: CtrlState, b: CtrlState): boolean {
  if (a.kind !== b.kind) return false
  return JSON.stringify(a) === JSON.stringify(b)
}

export const useLiveStore = create<LiveState>((set, get) => ({
  state: { kind: 'input' },
  degraded: true,
  settleUntil: 0,
  setState: (s) => set({ state: s }),
  applyDetect: (r) => {
    const { state: prev, settleUntil } = get()
    const provisional = Date.now() < settleUntil
    // 决策:何时用新解析覆盖 last-known?
    //   1) 不确凿(certain:false)—— 永不覆盖(reflow/空缓冲/重连瞬态),保留 last-known。
    //   2) settle 窗口内的「确认空闲(input)」—— 视为 provisional,不降级,保留 last-known。
    //      (settle 期间布局正在重排,即便此刻读到了输入框也可能是半成品;确认 busy/select 仍放行,
    //       因为那是「升级到更具体态」,不会丢思考中。)
    //   3) 其余(确凿 busy/select,或非 settle 期的确认 input)—— 覆盖。
    if (!r.certain) return
    if (provisional && r.state.kind === 'input' && prev.kind !== 'input') return
    if (sameState(prev, r.state)) return
    set({ state: r.state })
  },
  markSettle: (ms) => {
    const until = Date.now() + ms
    if (until > get().settleUntil) set({ settleUntil: until })
  },
  setDegraded: (d) => set({ degraded: d }),
  // resetLive:仅「真正换会话(host/sid 变)」时调用 —— 把镜像状态归零、重新进入降级待握手。
  // 瞬时断连 / 重连不得调用它(那会把 busy 抹成 input,正是要修的丢状态)。
  resetLive: () => set({ state: { kind: 'input' }, degraded: true, settleUntil: 0 }),
}))

// 消费侧便捷 hook。
export const useLiveState = (): CtrlState => useLiveStore((s) => s.state)
export const useLiveDegraded = (): boolean => useLiveStore((s) => s.degraded)
