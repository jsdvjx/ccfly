// engine/engine.ts — 读屏引擎核心(无 React,一文件看全)。
//
//   忠实帧 captureFrame → 静默去抖 → preFrame(一次性特征) → 按 weight 跑 resolve → 当前 Match。
//   命令侧两原子:send(走设备 /sendkeys HTTP 轨) + waitFor(订阅当前态)。
//   State 基类把 resolve / send / waitFor(this.kind) 捆给具体态继承,构造即自注册(单例)。
//
// 与旧 livestate.ts / ctrlstate.go 的根本差别:读屏【带属性】(反显 / 非默认底色),
// 当前项 cur 不再只认 ❯ 字形 —— 这是头号 bug(F1:无字形高亮 → 整菜单被丢)的修复点。
import { Terminal } from '@xterm/xterm'
import { liveTermHandle } from '../liveconn'
import { tmuxName, sendKeys, type SendResult } from '../api'

// ── 忠实帧:带属性的单元格网格(只留当前态需要的最小见证:inverse / 非默认底色)──
export interface Cell { char: string; inverse: boolean; dim: boolean; bold: boolean; bgDefault: boolean; width: number }
export interface Frame {
  rows: number
  cols: number
  cells: Cell[][]
  cursor: { row: number; col: number }
  text(row: number): string
}

const BLANK: Cell = { char: ' ', inverse: false, dim: false, bold: false, bgDefault: true, width: 1 }

// 读 xterm 的 buffer.active(可见屏,baseY 起),逐格保留 char + inverse/dim/bold + 底色是否默认。
export function captureFrame(term: Terminal): Frame {
  const buf = term.buffer.active
  const rows = term.rows
  const cols = term.cols
  const cells: Cell[][] = []
  for (let y = 0; y < rows; y++) {
    const line = buf.getLine(buf.baseY + y)
    const row: Cell[] = []
    for (let x = 0; x < cols; x++) {
      const c = line && line.getCell(x)
      if (!c) {
        row.push(BLANK)
        continue
      }
      row.push({
        char: c.getChars() || ' ',
        inverse: c.isInverse() !== 0,
        dim: c.isDim() !== 0,
        bold: c.isBold() !== 0,
        bgDefault: c.isBgDefault(), // ← 关键:底色是否默认(boolean)。非默认 = 被高亮(F1 的无字形高亮)
        width: c.getWidth(),
      })
    }
    cells.push(row)
  }
  const text = (y: number) => (cells[y] || []).map((c) => c.char).join('').replace(/\s+$/, '')
  return { rows, cols, cells, cursor: { row: buf.cursorY, col: buf.cursorX }, text }
}

// ── pre:对帧的一次性特征提取(只放特征,不放结论)。所有 resolve 共读,避免重复运算。──
export interface PreOption { num: number | null; label: string; rows: number[]; cur: boolean }
export interface FramePre { options: PreOption[]; footer: string | null; isBusy: boolean; inputBox: boolean; effort: string | null }
export interface Ctx { frame: Frame; pre: FramePre }

const RE_OPT = /^\s*(❯|›|>)?\s*(\d+)[.)]\s+(\S.*?)\s*$/
// 力度行:含 "effort" + 箭头提示 + "adjust"(如 "◉ medium effort  ←/→ to adjust")。捕获力度短语。
const RE_EFFORT = /(\S[^←<]*?effort[^←<]*?)\s*(?:←|<).*?adjust/i

export function preFrame(frame: Frame): FramePre {
  const options: PreOption[] = []
  for (let y = 0; y < frame.rows; y++) {
    const m = RE_OPT.exec(frame.text(y))
    if (!m) continue // 先判「像不像选项」,再谈高亮 —— /compact 进度条、力度条不会被误当选项
    const cur = !!m[1] || rowHighlighted(frame.cells[y] || []) // F1:cur = ❯ 字形 或 整行被属性高亮
    options.push({ num: Number(m[2]), label: m[3], rows: [y], cur })
  }
  const tail: string[] = []
  for (let y = Math.max(0, frame.rows - 8); y < frame.rows; y++) tail.push(frame.text(y))
  const footer = tail.slice(-6).find((t) => /(esc|enter)\b.*\bto\b|←\/→/i.test(t)) ?? null
  const isBusy = tail.some((t) => /esc to interrupt/i.test(t))
  let inputBox = false
  for (let y = 0; y < frame.rows; y++)
    if (/^─{6,}\s*$/.test(frame.text(y))) {
      inputBox = true
      break
    }
  let effort: string | null = null
  for (let y = 0; y < frame.rows; y++) {
    const e = RE_EFFORT.exec(frame.text(y))
    if (e) {
      effort = e[1].trim().replace(/\s+/g, ' ')
      break
    }
  }
  return { options, footer, isBusy, inputBox, effort }
}

// 行是否被属性高亮:已绘制(非空)单元格里,反显 / 非默认底色占多数 → 高亮。
function rowHighlighted(row: Cell[]): boolean {
  let painted = 0
  let hi = 0
  for (const c of row) {
    if (c.char === ' ' || c.char === '') continue
    painted++
    if (c.inverse || !c.bgDefault) hi++
  }
  return painted > 0 && hi >= Math.ceil(painted / 2)
}

// ── 注册表 + 当前态 + 订阅 ──
export interface StateInfo { kind: string }
export interface Match { kind: string; info: StateInfo }
export interface StateDef { kind: string; weight: number; resolve(ctx: Ctx): StateInfo | null }

const registry: StateDef[] = []
export function register(s: StateDef) {
  registry.push(s)
} // 不在此排序:weight 在子类构造后才有,tick 时再排

let curMatch: Match | null = null
const subs = new Set<(m: Match | null) => void>()
export const current = () => curMatch
export function subscribe(cb: (m: Match | null) => void) {
  subs.add(cb)
  return () => {
    subs.delete(cb)
  }
}

function tick(frame: Frame) {
  const ctx: Ctx = { frame, pre: preFrame(frame) }
  const ordered = [...registry].sort((a, b) => a.weight - b.weight) // 态数极少,排序可忽略
  for (const s of ordered) {
    const info = s.resolve(ctx)
    if (info) {
      curMatch = { kind: s.kind, info }
      for (const cb of subs) cb(curMatch)
      return
    }
  }
  curMatch = null
  for (const cb of subs) cb(curMatch)
}

// ── waitFor:订阅当前态,期望出现即 resolve,超时 null。复用同一注册表的 resolve,不另起匹配。
//    StateInfo 来自引擎已跑好的 Match(注册方与 waitFor 互不相识,在引擎会合;kind 既是会合键也是判别符)。
export function waitFor<I extends StateInfo>(kind: string, pred?: (i: I) => boolean, ms = 1500): Promise<I | null> {
  return new Promise((res) => {
    const hit = (m: Match | null) => !!m && m.kind === kind && (!pred || pred(m.info as I))
    if (hit(curMatch)) return res((curMatch as Match).info as I)
    let t = 0
    const off = subscribe((m) => {
      if (hit(m)) {
        off()
        if (t) clearTimeout(t)
        res((m as Match).info as I)
      }
    })
    t = window.setTimeout(() => {
      off()
      res(null)
    }, ms)
  })
}

// ── send:全部走设备 /sendkeys HTTP 轨(设备能看到每个键),绝不走 WS 打字轨。──
let sessionName = ''
export function send(keys: string[]): Promise<SendResult> {
  return sendKeys('', sessionName, { keys })
}

// ── 启动:把常驻 xterm 的写入接到「静默去抖 → tick」。返回 dispose。──
const QUIET_MS = 100 // 唯一的时钟常量(去抖),不是分类阈值
export function startEngine(sid: string): () => void {
  sessionName = tmuxName(sid)
  const term = liveTermHandle.term
  if (!term) return () => {}
  let quiet = 0
  const recalc = () => {
    if (quiet) clearTimeout(quiet)
    quiet = window.setTimeout(() => {
      quiet = 0
      try {
        tick(captureFrame(term))
      } catch {
        /* 解析失败不致命,保留上次态 */
      }
    }, QUIET_MS)
  }
  const sub = term.onWriteParsed(recalc)
  recalc()
  return () => {
    sub.dispose()
    if (quiet) clearTimeout(quiet)
  }
}

// ── State 基类:resolve(检测)+ send + waitFor(this.kind)捆给具体态;构造即自注册(单例)。──
// 注:waitFor/send 的逻辑对所有态相同,基类放它只为把 waitFor 绑到 this.kind,子类里省掉 kind 参数。
// 跨 kind 的等待(如提交后等回到 input)用自由函数 waitFor('input', …)。基类保持薄:命令逻辑在子类。
export abstract class State<I extends StateInfo> implements StateDef {
  abstract readonly kind: string
  abstract readonly weight: number
  abstract resolve(ctx: Ctx): I | null
  protected send = send
  protected waitFor(pred?: (i: I) => boolean, ms?: number) {
    return waitFor<I>(this.kind, pred, ms)
  }
  constructor() {
    register(this) // this.kind/weight 此刻尚未初始化,但 register 只存引用,tick 时才读
  }
}
