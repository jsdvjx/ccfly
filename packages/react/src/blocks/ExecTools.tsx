// 批次2 · 执行族控件:终端正文 TermOut + Bash 卡 BashCard(BashOutput/KillShell 复用)。
// 依赖批次0:BlockShell / Collapsible / openReader / useToolStatus / unwrapErr。
// 视觉:IDE 化终端 —— #0c0e13 等宽底,命令行蓝色 $ 提示符 + 原文(不 JSON.stringify),
// 输出 white-space:pre 横滚 + 限高盒,超长走 Collapsible 或全屏阅读。前缀 bash- / term-。
import { type ReactNode } from 'react'
import { BlockShell, Collapsible, openReader, useToolStatus, unwrapErr } from './shell'
import type { Block } from '../types'

// 终端正文的折叠/全屏阈值。
const TERM_COLLAPSE_LINES = 16 // 超此行数:折叠条截高
const TERM_FULLSCREEN_LINES = 24 // 超此行数:再给「⤢ 全屏阅读」入口

// ── 终端正文:#0c0e13 等宽、white-space:pre 横滚的限高盒。
// 行数适中直接出;偏长套 Collapsible 截高;很长再附「⤢ 全屏阅读」(bash 输出无语法,lang 空)。──
export interface TermOutProps {
  text: string
  isError?: boolean
}
export function TermOut({ text, isError }: TermOutProps) {
  const body = text ?? ''
  const n = body === '' ? 0 : body.replace(/\n$/, '').split('\n').length
  const longish = n > TERM_COLLAPSE_LINES
  const huge = n > TERM_FULLSCREEN_LINES
  const cls = 'term-out' + (isError ? ' term-out--err' : '')

  const pre = <pre className={cls}>{body || ' '}</pre>

  return (
    <div className="term">
      {longish ? (
        <Collapsible lines={TERM_COLLAPSE_LINES} count={n} fade>
          {pre}
        </Collapsible>
      ) : (
        pre
      )}
      {huge && (
        <button className="term-full" onClick={() => openReader(body, '')}>
          ⤢ 全屏阅读
        </button>
      )}
    </div>
  )
}

// ── 命令行正文:逐行蓝色 $ 提示符 + 命令原文(等宽,保留多行,不换行横滚)──
// 多行命令(管道/续行)每行一个提示符;原文不做任何转义/序列化。
function CommandLines({ command }: { command: string }) {
  const lines = command.replace(/\n$/, '').split('\n')
  return (
    <pre className="bash-cmd">
      {lines.map((ln, i) => (
        <div className="bash-cmd-line" key={i}>
          <span className="bash-prompt">$</span>
          <span className="bash-cmd-txt">{ln === '' ? ' ' : ln}</span>
        </div>
      ))}
    </pre>
  )
}

// ── 运行中占位:蓝脉冲点 + 运行中…(用基座 bs-run 动画风格,前缀 bash-)──
function RunningHint() {
  return (
    <div className="bash-running">
      <span className="bash-dot" aria-hidden="true" />
      <span className="bash-running-txt">运行中…</span>
    </div>
  )
}

const str = (input: Record<string, unknown> | undefined, k: string): string =>
  input && typeof input[k] === 'string' ? (input[k] as string) : ''

// ── Bash 卡:BlockShell accent=exec icon=❯ defaultOpen=false 三态。
// brief 优先 description;正文命令行(蓝 $)→ 运行中占位 / 终端输出。isError 红边由 status 驱动。──
export interface BashCardProps {
  block: Block
  // 覆盖标题/图标(BashOutput/KillShell 复用本卡时传入)。
  title?: ReactNode
  icon?: ReactNode
}
export function BashCard({ block, title, icon }: BashCardProps) {
  const input = (block.input || {}) as Record<string, unknown>
  const command = str(input, 'command')
  const description = str(input, 'description')
  const { status, res } = useToolStatus(block)

  // brief 优先 description,无则命令首行。
  const brief = description || command.split('\n')[0] || ''

  // 结果:剥 <tool_use_error> 包裹,合并强制错误态。
  const unwrapped = res ? unwrapErr(res.content) : null
  const isErr = status === 'err' || (unwrapped?.forcedErr ?? false)

  return (
    <BlockShell
      icon={icon ?? '❯'}
      title={title ?? 'Bash'}
      brief={brief}
      accent="exec"
      status={isErr ? 'err' : status}
      defaultOpen={false}
    >
      {command && <CommandLines command={command} />}
      {status === 'running' ? (
        <RunningHint />
      ) : (
        unwrapped && <TermOut text={unwrapped.text} isError={isErr} />
      )}
    </BlockShell>
  )
}

// ── BashOutput:轮询后台 shell 输出。复用 BashCard(❯ 标题改名),无 command 时简渲为纯终端输出。──
export function BashOutput({ block }: { block: Block }) {
  return <BashCard block={block} title="BashOutput" icon="❯" />
}

// ── KillShell:终止后台 shell。复用 BashCard(✕ 标题改名)。──
export function KillShell({ block }: { block: Block }) {
  return <BashCard block={block} title="KillShell" icon="✕" />
}
