// 「TUI 的 ANSI 上色低侵入带进表世界」的展示原语。前缀 ans-。
// 零依赖、不用 innerHTML(逐段 <span>,XSS 安全)。只解析 SGR(`m` 结尾)着色,其余 ESC 序列原样跳过。
// 与 shiki 互斥:代码/diff 走 CodeBlock(shiki),终端/面板原文走本组件,绝不双重上色。
// 调研口径:claude TUI 仅用 256 色(38;5;N / 48;5;N)+ bold/dim/italic/underline/reverse + reset。
import type { CSSProperties, ReactElement } from 'react'

// ── ANSI/SGR 正则 ──
// stripAnsi 吃所有 CSI(含光标移动/清屏等);解析时只对 `m` 结尾的 SGR 上色,其余 ESC 跳过不渲染。
// eslint-disable-next-line no-control-regex
const RE_CSI_ALL = /\x1b\[[0-9;?]*[ -/]*[@-~]/g
// eslint-disable-next-line no-control-regex
const RE_ESC_SPLIT = /(\x1b\[[0-9;?]*[ -/]*[@-~])/

// 去掉所有 ANSI/CSI 转义序列,返回纯文本。解析器/判断器一律先吃它。
export function stripAnsi(s: string): string {
  return s.replace(RE_CSI_ALL, '')
}

// ── xterm-256 调色板(现算成 rgb,不打表占体积)──
// 0-15:标准 16 色(与下方 16 色变量映射一致的近似 rgb,仅作 256 路径里的兜底)。
// 16-231:6x6x6 立方;232-255:24 级灰阶。
const CUBE = [0, 95, 135, 175, 215, 255]
const BASE16: ReadonlyArray<string> = [
  '#000000', '#cd0000', '#00cd00', '#cdcd00', '#0000ee', '#cd00cd', '#00cdcd', '#e5e5e5',
  '#7f7f7f', '#ff0000', '#00ff00', '#ffff00', '#5c5cff', '#ff00ff', '#00ffff', '#ffffff',
]
function xterm256(n: number): string {
  if (n < 16) return BASE16[n]
  if (n < 232) {
    const i = n - 16
    const r = CUBE[Math.floor(i / 36) % 6]
    const g = CUBE[Math.floor(i / 6) % 6]
    const b = CUBE[i % 6]
    return `rgb(${r},${g},${b})`
  }
  const v = 8 + (n - 232) * 10
  return `rgb(${v},${v},${v})`
}

// ── 标准 16 色 → 项目语义变量 ──
// 终端绝大多数语义就藏在 16 色里:绿=成功、红=错、黄=警、蓝=路径、灰=弱、白=默认前景、黑=底。
// 30-37 / 90-97 共用同一映射(亮色不再单独区分,统一吃项目变量,观感更统一)。
const C16: Record<number, string> = {
  0: 'var(--bg)', // black
  1: 'var(--red)', // red
  2: 'var(--green)', // green
  3: 'var(--amber)', // yellow
  4: 'var(--acc)', // blue
  5: 'var(--acc)', // magenta(无紫变量,归蓝系)
  6: 'var(--acc)', // cyan(teal/蓝系)
  7: 'var(--fg)', // white
}

// ── SGR 状态 ──
interface SgrState {
  fg: string | null
  bg: string | null
  bold: boolean
  dim: boolean
  italic: boolean
  underline: boolean
  reverse: boolean
}
function freshState(): SgrState {
  return { fg: null, bg: null, bold: false, dim: false, italic: false, underline: false, reverse: false }
}

// 应用一条 SGR 参数序列到 state(就地改)。识别不了的参数静默丢弃。
function applySgr(st: SgrState, params: number[]) {
  for (let i = 0; i < params.length; i++) {
    const p = params[i]
    switch (p) {
      case 0:
        Object.assign(st, freshState())
        break
      case 1:
        st.bold = true
        break
      case 2:
        st.dim = true
        break
      case 3:
        st.italic = true
        break
      case 4:
        st.underline = true
        break
      case 7:
        st.reverse = true
        break
      case 22:
        st.bold = false
        st.dim = false
        break
      case 23:
        st.italic = false
        break
      case 24:
        st.underline = false
        break
      case 27:
        st.reverse = false
        break
      case 38:
      case 48: {
        // 38;5;N(256)或 38;2;r;g;b(truecolor)。消费后续参数。
        const mode = params[i + 1]
        if (mode === 5) {
          const n = params[i + 2]
          const col = Number.isInteger(n) ? xterm256(n) : null
          if (p === 38) st.fg = col
          else st.bg = col
          i += 2
        } else if (mode === 2) {
          const r = params[i + 2]
          const g = params[i + 3]
          const b = params[i + 4]
          const col = `rgb(${r | 0},${g | 0},${b | 0})`
          if (p === 38) st.fg = col
          else st.bg = col
          i += 4
        }
        break
      }
      case 39:
        st.fg = null
        break
      case 49:
        st.bg = null
        break
      default:
        if (p >= 30 && p <= 37) st.fg = C16[p - 30]
        else if (p >= 90 && p <= 97) st.fg = C16[p - 90]
        else if (p >= 40 && p <= 47) st.bg = C16[p - 40]
        else if (p >= 100 && p <= 107) st.bg = C16[p - 100]
        // 其余(含 5 闪烁等)忽略。
        break
    }
  }
}

// 把 SGR 参数串("1;38;5;114")解析成数字数组;空段记 0(SGR 默认参数)。
function parseParams(raw: string): number[] {
  if (raw === '') return [0]
  return raw.split(';').map((s) => (s === '' ? 0 : parseInt(s, 10)))
}

// state → 内联样式 + class。reverse 时交换前/背景(背景缺省取容器底、前景缺省 --fg)。dim → opacity。
function styleOf(st: SgrState): { style: CSSProperties; className: string } {
  let fg = st.fg
  let bg = st.bg
  if (st.reverse) {
    const nfg = bg ?? 'var(--bg)'
    const nbg = fg ?? 'var(--fg)'
    fg = nfg
    bg = nbg
  }
  const style: CSSProperties = {}
  if (fg) style.color = fg
  if (bg) style.background = bg
  if (st.dim) style.opacity = 0.6
  const cls: string[] = []
  if (st.bold) cls.push('ans-b')
  if (st.italic) cls.push('ans-i')
  if (st.underline) cls.push('ans-u')
  return { style, className: cls.join(' ') }
}

export interface AnsiTextProps {
  text: string
  className?: string
}

// 把带 ANSI 的文本渲成一串着色 <span>。无 ANSI 时退化为单个纯文本 span。
export function AnsiText({ text, className }: AnsiTextProps) {
  const parts = text.split(RE_ESC_SPLIT)
  const st = freshState()
  const spans: ReactElement[] = []
  let key = 0
  for (const part of parts) {
    if (part === '') continue
    const m = part.match(/^\x1b\[([0-9;?]*)([ -/]*[@-~])$/) // eslint-disable-line no-control-regex
    if (m) {
      // SGR(`m` 结尾、无中间字节)才动样式;其它 CSI(光标/清屏等)跳过不渲染。
      if (m[2] === 'm') applySgr(st, parseParams(m[1]))
      continue
    }
    const { style, className: spanCls } = styleOf(st)
    spans.push(
      <span key={key++} style={style} className={spanCls || undefined}>
        {part}
      </span>,
    )
  }
  return <span className={className}>{spans}</span>
}
