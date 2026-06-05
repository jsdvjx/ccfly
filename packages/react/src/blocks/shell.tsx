// 批次0 基座 · 接口冻结,全员依赖。
// 提供:折叠卡壳(BlockShell)、折叠正文(Collapsible)、限高代码画布(CodeCanvas)、
// 结果面板(ResultPane)、全屏代码阅读器(openReader + ReaderHost)、状态钩子与工具函数。
// CSS 前缀:bs-(卡) cc-(代码画布) rp-(结果) fsr-(全屏阅读器)。
import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from 'react'
// 注:无全局折叠总线。每张卡只受自身 defaultOpen + 点头部开合控制(对齐 claude TUI 折叠习惯)。
import { highlighter, LANG_SET } from '../highlight'
import { useStore } from '../store'
import { MD } from '../components'
import type { Block, PatchHunk } from '../types'

// ── 类型 ──
export type Accent = 'file' | 'exec' | 'web' | 'task' | 'skill' | 'mcp' | 'plan' | 'unknown' | 'none'
export type ToolStatus = 'running' | 'ok' | 'err' | 'pending'

// ── 子时间线结果上下文 ──
// 子代理工具卡(AgentCard 展开后由 renderItems 渲出的 Bash/Read/… 卡)调的也是 useToolStatus,
// 但子工具的 tool_result 不在主 store。故提供一个可选的「局部结果表」上下文:有它且命中 id 时,
// useToolStatus 优先读它(子时间线作用域),否则退回主 store(主时间线默认无 Provider,值为 null)。
export type ResultMap = Record<string, { content: string; isError: boolean; patch?: PatchHunk[] }>
export const SubResultContext = createContext<ResultMap | null>(null)

// ── 工具状态钩子:按 block.id 查结果 → running / ok / err,并带回结果体 ──
export interface ToolStatusResult {
  status: ToolStatus
  res?: { content: string; isError: boolean; patch?: PatchHunk[] }
}
export function useToolStatus(block: Block): ToolStatusResult {
  const sub = useContext(SubResultContext)
  const storeRes = useStore((s) => (block.id ? s.resultById[block.id] : undefined))
  // 子时间线作用域:命中局部表优先(主 store 不含子工具结果);否则退回主 store。
  const res = (sub && block.id ? sub[block.id] : undefined) || storeRes
  if (!res) return { status: 'running' }
  return { status: res.isError ? 'err' : 'ok', res }
}

// ── 剥 tool_use_error 包裹标签 ──
// Agent SDK 失败结果常被包成 <tool_use_error>…</tool_use_error>;剥掉并标记强制错误。
export interface UnwrappedErr {
  text: string
  forcedErr: boolean
}
export function unwrapErr(content: string): UnwrappedErr {
  const s = content || ''
  const m = s.match(/^\s*<tool_use_error>([\s\S]*?)<\/tool_use_error>\s*$/)
  if (m) return { text: m[1].trim(), forcedErr: true }
  return { text: s, forcedErr: false }
}

// ── 运行态指示(TUI 风):braille spinner + 实时计时 ──
// 计时起点取「正在跑的那条 item 的 ts」。工具卡不向 BlockShell 透传 block,故在此就近从主 store
// 取最末一条 item 的 ts —— 运行中的 tool_use 必属于在飞那一轮(可能多卡并行,共享同一 item),
// 该 item 恒为窗口末尾。子时间线无 store 末尾对齐保证,故 SubResultContext 在位时退化为「只转圈」。
// 拿不到合法 ts → 退化为只转圈(不显计时)。整卡卸载/状态翻 ok|err 时本组件随之卸载,动画自然停。
const SPINNER = '⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏'
// 计时兜底阈值:超过此值(6h)认为是陈旧/历史 running 态(如重载老会话里某个 AgentCard
// 历史上 status=running 且无 result,elapsed = now - 老ts 会算出「running 72h」),
// 此时只转 spinner、不显计时。真在飞的同步工具那轮 ts 是当前窗口末尾,计时合理,不受影响。
const STALE_RUN_MS = 6 * 60 * 60 * 1000

function runStartTs(): number {
  const items = useStore.getState().items
  const last = items.length ? items[items.length - 1] : undefined
  if (!last || !last.ts) return 0
  const t = Date.parse(last.ts)
  return Number.isNaN(t) ? 0 : t
}

function fmtElapsed(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  if (s < 60) return s + 's'
  const m = Math.floor(s / 60)
  const r = s % 60
  if (m < 60) return m + 'm' + (r ? ' ' + r + 's' : '')
  const h = Math.floor(m / 60)
  return h + 'h ' + (m % 60) + 'm'
}

function RunIndicator() {
  // 起点 ts 在挂载时定格(运行期间不会变);无 SubResultContext(主时间线)才取 store 末尾。
  const sub = useContext(SubResultContext)
  const startRef = useRef<number>(sub ? 0 : runStartTs())
  const [, force] = useState(0)
  // 走帧重渲:spinner 转 + 计时每秒刷新。120ms ≈ 8fps,braille 转圈顺滑。组件卸载(status 翻态)即清。
  useEffect(() => {
    const id = setInterval(() => force((n) => n + 1), 120)
    return () => clearInterval(id)
  }, [])
  const start = startRef.current
  const frame = SPINNER[Math.floor(Date.now() / 120) % SPINNER.length]
  const elapsed = start > 0 ? Date.now() - start : 0
  // 超阈值=陈旧/历史态(非真在飞):只转 spinner,不显「天文」计时。
  const showTime = start > 0 && elapsed < STALE_RUN_MS
  return (
    <span className="bs-run" aria-label="运行中" aria-live="off">
      <span className="bs-run-spin" aria-hidden="true">
        {frame}
      </span>
      {showTime && <span className="bs-run-t">running {fmtElapsed(elapsed)}</span>}
    </span>
  )
}

// ── 卡壳:一行头部(可点折叠)+ 折叠正文。开合仅由自身 defaultOpen + 点头部控制 ──
export interface BlockShellProps {
  icon: ReactNode
  title: ReactNode
  brief?: ReactNode
  accent?: Accent
  status?: ToolStatus
  defaultOpen?: boolean
  foldable?: boolean
  headerExtra?: ReactNode
  children?: ReactNode
}
export function BlockShell({
  icon,
  title,
  brief,
  accent = 'unknown',
  status = 'ok',
  defaultOpen = false,
  foldable = true,
  headerExtra,
  children,
}: BlockShellProps) {
  const [open, setOpen] = useState(defaultOpen)

  const isErr = status === 'err'
  const cls =
    'bs' + (accent !== 'none' && accent !== 'unknown' ? ' bs--' + accent : ' bs--unknown') + (isErr ? ' bs--err' : '')

  return (
    <div className={cls}>
      <div
        className={'bs-head' + (foldable ? ' bs-clk' : '')}
        onClick={foldable ? () => setOpen((o) => !o) : undefined}
        role={foldable ? 'button' : undefined}
      >
        <span className="bs-icon">{icon}</span>
        <span className="bs-title">{title}</span>
        {brief != null && brief !== '' && <span className="bs-brief">{brief}</span>}
        {headerExtra}
        {isErr && <span className="bs-x">err</span>}
        {status === 'running' && <RunIndicator />}
        {status === 'pending' && <span className="bs-pend" aria-label="待定" />}
        {foldable && <span className="bs-chev">{open ? '▾' : '▸'}</span>}
      </div>
      {(open || !foldable) && <div className="bs-body">{children}</div>}
    </div>
  )
}

// ── 折叠正文:行数超阈值时截高 + 可选渐隐 + 展开/收起按钮 ──
export interface CollapsibleProps {
  lines?: number
  count: number
  fade?: boolean
  children?: ReactNode
}
export function Collapsible({ lines = 14, count, fade, children }: CollapsibleProps) {
  const [open, setOpen] = useState(false)
  const long = count > lines
  const clamped = long && !open
  const style = clamped ? { maxHeight: (lines * 1.55).toFixed(2) + 'em', overflow: 'hidden' as const } : undefined
  return (
    <div className="bs-collapse">
      <div style={style} className={clamped && fade ? 'bs-fadewrap' : undefined}>
        {children}
        {clamped && fade && <span className="bs-fade" />}
      </div>
      {long && (
        <button className="bs-more" onClick={() => setOpen((o) => !o)}>
          {open ? '收起' : `展开 (${count} 行)`}
        </button>
      )}
    </div>
  )
}

// ── 限高代码画布:逐行行号 + shiki 高亮;超 24 行底部「⤢ 全屏阅读」 ──
export interface CodeCanvasProps {
  code: string
  lang?: string
  startLine?: number
  gutter?: boolean
}
const FULLSCREEN_THRESHOLD = 24
interface HiCache {
  code: string
  lang: string
  lines: string[]
}
export function CodeCanvas({ code, lang = '', startLine = 1, gutter = true }: CodeCanvasProps) {
  const known = !!lang && LANG_SET.has(lang)
  // 缓存连同其来源 code/lang 一起存,渲染期比对 → 陈旧高亮自动失效,无需在 effect 里同步重置 state。
  const [hi, setHi] = useState<HiCache | null>(null)
  useEffect(() => {
    if (!known) return
    let alive = true
    highlighter()
      .then((h) => {
        if (!alive) return
        // 整段高亮后按行拆分,逐行套行号槽(每行用 .line span 包裹,shiki github-dark)。
        const html = h.codeToHtml(code, { lang, theme: 'github-dark' })
        setHi({ code, lang, lines: extractLines(html) })
      })
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [code, lang, known])

  const rawLines = code.replace(/\n$/, '').split('\n')
  const n = rawLines.length
  const tokens = hi && hi.code === code && hi.lang === lang ? hi.lines : null
  const useTok = !!tokens && tokens.length === n
  const showFull = n > FULLSCREEN_THRESHOLD

  return (
    <div className="cc">
      <div className="cc-scroll">
        <pre className="cc-pre">
          {rawLines.map((ln, i) => (
            <div className="cc-line" key={i}>
              {gutter && <span className="cc-gut">{startLine + i}</span>}
              {useTok ? (
                <span className="cc-txt" dangerouslySetInnerHTML={{ __html: tokens![i] }} />
              ) : (
                <span className="cc-txt">{ln === '' ? ' ' : ln}</span>
              )}
            </div>
          ))}
        </pre>
      </div>
      {showFull && (
        <button className="cc-full" onClick={() => openReader(code, lang)}>
          ⤢ 全屏阅读
        </button>
      )}
    </div>
  )
}

// shiki 输出的 <pre><code>…</code></pre> 拆成逐行 innerHTML(每行 <span class="line">…</span>)。
function extractLines(html: string): string[] {
  const codeMatch = html.match(/<code[^>]*>([\s\S]*?)<\/code>/)
  const inner = codeMatch ? codeMatch[1] : html
  const lineRe = /<span class="line"[^>]*>([\s\S]*?)<\/span>(?=\n|$|<span class="line")/g
  const out: string[] = []
  let m: RegExpExecArray | null
  while ((m = lineRe.exec(inner)) !== null) out.push(m[1])
  if (out.length === 0) return inner.split('\n')
  return out
}

// ── 结果面板:剥错误标签 → mono 终端 pre / md / list 三态,长内容套 Collapsible ──
export type ResultVariant = 'mono' | 'md' | 'list'
export interface ResultPaneProps {
  content?: string
  isError?: boolean
  variant?: ResultVariant
  collapseLines?: number
  children?: ReactNode // variant=list 时由调用方提供
}
export function ResultPane({ content = '', isError, variant = 'mono', collapseLines = 14 }: ResultPaneProps) {
  const { text, forcedErr } = unwrapErr(content)
  const err = !!isError || forcedErr
  const body = err ? text : text

  if (variant === 'md') {
    return (
      <div className={'rp' + (err ? ' rp--err' : '')}>
        {err && <div className="rp-errhead">✗ 失败</div>}
        <MD text={body} />
      </div>
    )
  }
  // mono(默认):#0c0e13 终端 pre + Collapsible 截高
  const count = body.split('\n').length
  return (
    <div className={'rp' + (err ? ' rp--err' : '')}>
      {err && <div className="rp-errhead">✗ 失败</div>}
      <Collapsible lines={collapseLines} count={count} fade>
        <pre className={'rp-mono' + (err ? ' rp-mono--err' : '')}>{body || ' '}</pre>
      </Collapsible>
    </div>
  )
}

// ── 全屏代码阅读器:模块级 store + ReaderHost(挂 App 根渲染一次)+ openReader 触发 ──
interface ReaderItem {
  text: string
  lang: string
}
let readerState: ReaderItem | null = null
const readerSubs = new Set<() => void>()
function readerEmit() {
  for (const fn of readerSubs) fn()
}
// 外部 API:任意卡片调用即打开全屏阅读器。
export function openReader(text: string, lang?: string) {
  readerState = { text, lang: lang || '' }
  readerEmit()
}
function closeReader() {
  readerState = null
  readerEmit()
}
function readerSubscribe(fn: () => void) {
  readerSubs.add(fn)
  return () => {
    readerSubs.delete(fn)
  }
}
function readerSnapshot() {
  return readerState
}

// 挂载一次:在 main.tsx 与 <App/> 并列渲染。监听模块级 store,打开时全屏覆盖。
export function ReaderHost() {
  const item = useSyncExternalStore(readerSubscribe, readerSnapshot, readerSnapshot)
  // 高亮结果连同来源 text 一起缓存,渲染期比对 → 关闭/换内容时自动失效,无需在 effect 里同步清空。
  const [hi, setHi] = useState<{ text: string; html: string } | null>(null)
  const [toast, setToast] = useState(false)

  // Esc 关闭(仅订阅,不动 state)。
  useEffect(() => {
    if (!item) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') closeReader()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [item])

  // 异步高亮(已知语言才跑;setState 只在 .then 里发生)。
  useEffect(() => {
    if (!item || !item.lang || !LANG_SET.has(item.lang)) return
    let alive = true
    highlighter()
      .then((h) => alive && setHi({ text: item.text, html: h.codeToHtml(item.text, { lang: item.lang, theme: 'github-dark' }) }))
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [item])

  if (!item) return null
  const html = hi && hi.text === item.text ? hi.html : ''
  const copy = () => {
    navigator.clipboard?.writeText(item.text).then(
      () => {
        setToast(true)
        setTimeout(() => setToast(false), 1400)
      },
      () => {},
    )
  }
  return (
    <div className="fsr" onClick={closeReader}>
      <div className="fsr-box" onClick={(e) => e.stopPropagation()}>
        <div className="fsr-bar">
          <span className="fsr-lang">{item.lang || 'text'}</span>
          <span className="fsr-spacer" />
          <button className="fsr-btn" onClick={copy}>
            复制
          </button>
          <button className="fsr-btn fsr-close" onClick={closeReader} aria-label="关闭">
            ✕
          </button>
        </div>
        <div className="fsr-scroll">
          {html ? (
            <div className="fsr-code" dangerouslySetInnerHTML={{ __html: html }} />
          ) : (
            <pre className="fsr-code">{item.text}</pre>
          )}
        </div>
      </div>
      {toast && <div className="fsr-toast">已复制</div>}
    </div>
  )
}
