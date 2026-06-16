// __tests__/frameBuilder.ts — 用纯文本 + 可选属性构造一个忠实 Frame(替代真 xterm 抓屏)。
//
// state-engine.md §3 要求 Frame 是「JSON-constructible for tests」。这个 helper 就是它的测试入口:
// 一行字符串 = 一行默认属性;{ text, inverse/bgDefault/... } = 给该行打上属性(用来造无字形高亮的
// cur 见证,F1)。等真机 `.ansi` 夹具进来后,可加一个 ansi→Frame 的 loader 并存,二者产出同构。
import type { Cell, Frame } from '../pre'

export interface LineSpec {
  text: string
  inverse?: boolean // 整行反显(无字形高亮的 cur 见证之一,F1)
  dim?: boolean
  bold?: boolean
  bgDefault?: boolean // 默认 true;false = 非默认底色(另一种无字形高亮,F1)
}
export type Line = string | LineSpec

const norm = (l: Line): LineSpec => (typeof l === 'string' ? { text: l } : l)

export function frameFromLines(lines: Line[], cursor = { row: 0, col: 0 }): Frame {
  const specs = lines.map(norm)
  const cols = Math.max(1, ...specs.map((s) => [...s.text].length))
  const cells: Cell[][] = specs.map((s) => {
    const chars = [...s.text]
    const row: Cell[] = []
    for (let x = 0; x < cols; x++) {
      row.push({
        char: chars[x] ?? ' ',
        inverse: !!s.inverse,
        dim: !!s.dim,
        bold: !!s.bold,
        bgDefault: s.bgDefault === undefined ? true : s.bgDefault,
        width: 1,
      })
    }
    return row
  })
  const text = (y: number) => (cells[y] || []).map((c) => c.char).join('').replace(/\s+$/, '')
  return { rows: specs.length, cols, cells, cursor, text }
}
