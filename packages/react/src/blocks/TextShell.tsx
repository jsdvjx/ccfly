// 批次1 · 文本族控件:用户气泡 / 助手正文 / 思考块 / 消息头。前缀 tx-。
// 依赖批次0:Collapsible(折叠正文)。复用 components.tsx 的 MD。思考块自绘(仿 TUI),不走 BlockShell 卡壳。
// 视觉:IDE 化终端 —— 文本走 MD,等宽长块降级为 pre;思考默认折叠并 dim。
import { useState, type ReactNode } from 'react'
import { MD, shortModel } from '../components'
import { Collapsible } from './shell'

// ── 消息头:角色标签 + 可选模型徽标。仅在角色/模型切换处由调用方渲染 ──
export interface MsgHeaderProps {
  role: 'user' | 'assistant'
  model?: string
  showModel?: boolean
  extra?: ReactNode
}
export function MsgHeader({ role, model, showModel, extra }: MsgHeaderProps) {
  const isUser = role === 'user'
  return (
    <div className={'tx-head' + (isUser ? ' tx-head--user' : ' tx-head--asst')}>
      <span className="tx-role">{isUser ? '你' : 'Claude'}</span>
      {!isUser && showModel && model && <span className="tx-model">{shortModel(model)}</span>}
      {extra}
    </div>
  )
}

// ── 文本族通用容器:头部(可选)+ 子内容。供消息渲染层组合用 ──
export interface TextShellProps {
  role: 'user' | 'assistant'
  model?: string
  showModel?: boolean
  showHeader?: boolean
  children?: ReactNode
}
export function TextShell({ role, model, showModel, showHeader = true, children }: TextShellProps) {
  const isUser = role === 'user'
  return (
    <div className={'tx-shell' + (isUser ? ' tx-shell--user' : ' tx-shell--asst') + (showHeader ? '' : ' tx-cont')}>
      {showHeader && <MsgHeader role={role} model={model} showModel={showModel} />}
      {children}
    </div>
  )
}

// ── 判定:文本是否含 markdown 结构(有则走 MD,无则可降级 pre)──
// 命中标题/列表/引用/代码围栏/表格/链接/强调/行内代码即视为有结构。
const MD_STRUCT =
  /(^|\n)\s{0,3}(#{1,6}\s|[-*+]\s|\d+\.\s|>\s|```|\||\[.+?\]\(.+?\))|[*_`~]{1,3}\S/
function hasMarkdown(text: string): boolean {
  return MD_STRUCT.test(text)
}

const USER_PRE_LINES = 6 // 等宽长块降级 pre 的默认可见行
const USER_LINE_LIMIT = 20 // 单段超此行数视为「长」

// ── 用户气泡:有 markdown 结构走 MD;单段超 20 行或无结构的等宽长块降级 pre + Collapsible(默认 6 行)──
export interface UserBubbleProps {
  text: string
}
export function UserBubble({ text }: UserBubbleProps) {
  const body = text || ''
  if (!body.trim()) return null
  const lines = body.split('\n')
  const n = lines.length
  const md = hasMarkdown(body)
  // 降级条件:长段(>20 行)或 无 markdown 结构的多行等宽块。
  const degrade = n > USER_LINE_LIMIT || (!md && n > 1)

  if (degrade) {
    return (
      <div className="tx-bubble tx-bubble--user tx-bubble--pre">
        <Collapsible lines={USER_PRE_LINES} count={n} fade>
          <pre className="tx-pre">{body}</pre>
        </Collapsible>
      </div>
    )
  }
  return (
    <div className="tx-bubble tx-bubble--user">
      <MD text={body} />
    </div>
  )
}

const ASST_LINE_LIMIT = 120 // 助手正文软折叠阈值

// ── 助手正文:走 MD;超 120 行软折叠(Collapsible 截高 + 渐隐)──
export interface AssistantBodyProps {
  text: string
}
export function AssistantBody({ text }: AssistantBodyProps) {
  const body = text || ''
  if (!body.trim()) return null
  const n = body.split('\n').length
  if (n > ASST_LINE_LIMIT) {
    return (
      <div className="tx-body tx-body--asst">
        <Collapsible lines={ASST_LINE_LIMIT} count={n} fade>
          <MD text={body} />
        </Collapsible>
      </div>
    )
  }
  return (
    <div className="tx-body tx-body--asst">
      <MD text={body} />
    </div>
  )
}

// ── 思考块首句摘要:取首个非空行,截约 40 字 ──
const THINK_BRIEF_CHARS = 40
function thinkBrief(text: string): string {
  const firstLine = (text.split('\n').find((l) => l.trim()) || '').trim()
  // 进一步截到首句(中英句末标点),再做长度兜底。
  const sentEnd = firstLine.search(/[。!?.!?]/)
  let s = sentEnd > 0 && sentEnd < THINK_BRIEF_CHARS ? firstLine.slice(0, sentEnd + 1) : firstLine
  if (s.length > THINK_BRIEF_CHARS) s = s.slice(0, THINK_BRIEF_CHARS) + '…'
  return s
}

// ── 思考块(仿 TUI):不再用 BlockShell 卡壳(💭+边框+字数卡,与 TUI 不符)。
// 改为 TUI 的「✻ 思考」布局 —— dim 星标 + dim 标题,默认折叠时尾随首句摘要;
// 展开后正文 dim 斜体、左竖线缩进(对齐 claude TUI 思考的内联 dim 呈现)。
export interface ThinkingBlockProps {
  text: string
}
export function ThinkingBlock({ text }: ThinkingBlockProps) {
  const [open, setOpen] = useState(false)
  const body = text || ''
  if (!body.trim()) return null
  return (
    <div className={'tx-think' + (open ? ' tx-think--open' : '')}>
      <div className="tx-think-head" onClick={() => setOpen((o) => !o)} role="button">
        <span className="tx-think-star" aria-hidden="true">
          ✻
        </span>
        <span className="tx-think-label">思考</span>
        {!open && <span className="tx-think-brief">{thinkBrief(body)}</span>}
        <span className="tx-think-chev" aria-hidden="true">
          {open ? '▾' : '▸'}
        </span>
      </div>
      {open && (
        <div className="tx-think-body">
          <MD text={body} />
        </div>
      )}
    </div>
  )
}
