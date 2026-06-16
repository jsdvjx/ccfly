// engine/pre.ts — 纯数据 + 唯一解析器(零 xterm / 零 React / 零网络依赖)。
//
// 这是 state-engine.md §3 的 frame.ts + pre.ts:忠实帧(带属性的单元格网格)与「一次性特征提取」。
// 刻意不含任何运行时副作用,故可 JSON 构造、可单测(F6 的「one parser」就落在这里)。
// engine.ts 从本模块 import 这些类型与 preFrame,并补上 xterm 适配(captureFrame)、时钟、send。
//
// 与旧 livestate.ts / ctrlstate.go 的根本差别:读屏【带属性】(反显 / 非默认底色),
// 当前项 cur 不再只认 ❯ 字形 —— 这是头号 bug(F1:无字形高亮 → 整菜单被丢)的修复点。

// ── 忠实帧:带属性的单元格网格(只留当前态需要的最小见证:inverse / 非默认底色)──
export interface Cell { char: string; inverse: boolean; dim: boolean; bold: boolean; bgDefault: boolean; width: number }
export interface Frame {
  rows: number
  cols: number
  cells: Cell[][]
  cursor: { row: number; col: number }
  text(row: number): string
}

export const BLANK: Cell = { char: ' ', inverse: false, dim: false, bold: false, bgDefault: true, width: 1 }

// ── pre:对帧的一次性特征提取(只放特征,不放结论)。所有 resolve 共读,避免重复运算。──
export interface PreOption { num: number | null; label: string; rows: number[]; cur: boolean; checked?: boolean }
export interface FramePre { options: PreOption[]; footer: string | null; isBusy: boolean; inputBox: boolean; effort: string | null; title: string }
export interface Ctx { frame: Frame; pre: FramePre }

// g1=游标字形 g2=编号 g3=复选框字形(可空,多选菜单独有) g4=标签
const RE_OPT = /^\s*(❯|›|>)?\s*(\d+)[.)]\s+([◯◉○●☑■□◻◼]|\[[ xX✔]?\])?\s*(\S.*?)\s*$/
// 力度行:含 "effort" + 箭头提示 + "adjust"(如 "◉ medium effort  ←/→ to adjust")。捕获力度短语。
const RE_EFFORT = /(\S[^←<]*?effort[^←<]*?)\s*(?:←|<).*?adjust/i

export function preFrame(frame: Frame): FramePre {
  const options: PreOption[] = []
  for (let y = 0; y < frame.rows; y++) {
    const m = RE_OPT.exec(frame.text(y))
    if (!m) continue // 先判「像不像选项」,再谈高亮 —— /compact 进度条、力度条不会被误当选项
    const cur = !!m[1] || rowHighlighted(frame.cells[y] || []) // F1:cur = ❯ 字形 或 整行被属性高亮
    let checked: boolean | undefined
    if (m[3]) checked = /[◉●☑■◼]|\[[xX✔]\]/.test(m[3]) // 实心/勾=选中,空框=未选;无复选框字形→undefined(单选)
    options.push({ num: Number(m[2]), label: m[4], rows: [y], cur, checked })
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
  // 标题:选项块上方那段文字块的【最顶行】(而非最近一行)。各 state 的 resolve 共读。
  // 旧实现取「最近的非空行」,在「标题 + 多行描述段」的菜单(/model、scope 等)上会取到描述段
  // 的末行(如 "--model."),而非真标题("Select model")。改为:先跳过紧贴选项的空行,再向上
  // 收一段连续文字块,块的上边界是【空行】或【横向分隔线 ───/▔▔▔】(菜单 chrome / 对话气泡的
  // 分界),取该块最顶行作标题。真机夹具 model-select.txt 锁此行为。
  let title = ''
  if (options.length) {
    const top = Math.min(...options.flatMap((o) => o.rows))
    let y = top - 1
    while (y >= 0 && frame.text(y).trim() === '') y-- // 跳过选项与描述之间的空行
    let titleRow = -1
    for (; y >= 0 && y >= top - 8; y--) {
      const t = frame.text(y).trim()
      if (t === '' || isRuleLine(t)) break // 空行 / 横向分隔线 = 文字块上边界
      titleRow = y
    }
    if (titleRow >= 0) title = frame.text(titleRow).trim()
  }
  return { options, footer, isBusy, inputBox, effort, title }
}

// 横向分隔线:整行(去空白后)全是 box-drawing / block-element 制表符(如 ─── 或 ▔▔▔),长度 ≥3。
// 用作标题文字块的上边界 —— 把菜单 chrome、输入框边框、上一条对话气泡的分隔从「标题块」里切开。
function isRuleLine(t: string): boolean {
  const s = t.replace(/\s+/g, '')
  return s.length >= 3 && /^[─-▟]+$/.test(s)
}

// 行是否被属性高亮:已绘制(非空)单元格里,反显 / 非默认底色占多数 → 高亮。
export function rowHighlighted(row: Cell[]): boolean {
  let painted = 0
  let hi = 0
  for (const c of row) {
    if (c.char === ' ' || c.char === '') continue
    painted++
    if (c.inverse || !c.bgDefault) hi++
  }
  return painted > 0 && hi >= Math.ceil(painted / 2)
}
