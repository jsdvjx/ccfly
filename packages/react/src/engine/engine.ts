// engine/engine.ts — 读屏引擎核心(无 React,一文件看全)。
//
//   忠实帧 captureFrame → 静默去抖 → preFrame(一次性特征) → 按 weight 跑 resolve → 当前 Match。
//   命令侧两原子:send(走设备 /sendkeys HTTP 轨) + waitFor(订阅当前态)。
//   State 基类把 resolve / send / waitFor(this.kind) 捆给具体态继承,构造即自注册(单例)。
//
// 与旧 livestate.ts / ctrlstate.go 的根本差别:读屏【带属性】(反显 / 非默认底色),
// 当前项 cur 不再只认 ❯ 字形 —— 这是头号 bug(F1:无字形高亮 → 整菜单被丢)的修复点。
import type { Terminal } from '@xterm/xterm'
import { liveTermHandle } from '../liveconn'
import { tmuxName, sendKeys, type SendResult } from '../api'
// 忠实帧 / 特征解析器抽到纯模块 ./pre(零 xterm/React,可单测;state-engine.md §3「one parser」)。
import { type Cell, type Frame, type Ctx, BLANK, preFrame } from './pre'

// 帧 / 特征类型与 preFrame 从纯模块 ./pre re-export,保持既有 `from '../engine'` 的 import 路径不变。
export { preFrame } from './pre'
export type { Cell, Frame, FramePre, PreOption, Ctx } from './pre'

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

// (PreOption / FramePre / Ctx / preFrame / rowHighlighted 已移至 ./pre,纯解析器单测在那边。)

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

// classify — 对一个忠实帧跑注册表(按 weight 升序,首个非空 resolve 胜出)→ Match | null。
// 纯函数:只读 registry + 帧,不触 curMatch / 不通知订阅。tick 与单测共用它,保证「线上判定」与
// 「测试判定」是同一口径(F6:测的就是跑的)。
export function classify(frame: Frame): Match | null {
  const ctx: Ctx = { frame, pre: preFrame(frame) }
  const ordered = [...registry].sort((a, b) => a.weight - b.weight) // 态数极少,排序可忽略
  for (const s of ordered) {
    const info = s.resolve(ctx)
    if (info) return { kind: s.kind, info }
  }
  return null
}

function tick(frame: Frame) {
  curMatch = classify(frame)
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
