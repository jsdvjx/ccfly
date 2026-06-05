// 批次2 · 元/族控件:Todo / Agent(Task) / Plan / Skill / Mcp / Generic 兜底。
// 每张卡 = BlockShell 包专属正文;meta 从 TOOL_META 取(图标按本期口径以各卡显式声明为准),
// 用 useToolStatus(block) 拿结果与三态。CSS 前缀:td-(todo) tk-(任务) pl-(计划)
// sk-(技能) mc-(mcp) gn-(generic)。复用现有 .todos/.gauge-bar/.gauge-fill/.kv/.pill。
import { useState, type ReactNode } from 'react'
import { BlockShell, Collapsible, ResultPane, useToolStatus } from './shell'
import { briefOf } from './meta'
import { CodeBlock } from './CodeBlock'
import { MD } from '../components'
import { useSession } from './ctx'
import { openSubagent } from '../SubagentView'
import type { Block } from '../types'

// ── 入参取值小工具(各卡复用)──
function asInput(block: Block): Record<string, unknown> {
  return (block.input || {}) as Record<string, unknown>
}
function str(input: Record<string, unknown>, k: string): string {
  const v = input[k]
  return typeof v === 'string' ? v : ''
}

// ── 待办进度(TodoCard 私有)──
interface Todo {
  content: string
  status: string
  activeForm?: string
}
const TODO_MARK: Record<string, string> = { completed: '✓', in_progress: '◐', pending: '○' }

// ── TodoCard:进度 brief + .gauge 进度条;折叠显当前 in_progress 行,展开全列表。 ──
// accent=none(无族色条),icon=📋,defaultOpen=false。复用现有 .todos / .gauge-bar / .gauge-fill。
export function TodoCard({ block }: { block: Block }) {
  const input = asInput(block)
  const todos = (Array.isArray(input.todos) ? input.todos : []) as Todo[]
  const total = todos.length
  const done = todos.filter((t) => t.status === 'completed').length
  const cur = todos.find((t) => t.status === 'in_progress')
  const pct = total > 0 ? Math.round((done / total) * 100) : 0
  const gaugeColor = done === total && total > 0 ? 'green' : 'amber'

  // brief:进度 3/7 + 限高进度条(头部内联,折叠态也可见整体进度)。
  const brief = (
    <span className="td-brief">
      <span className="td-frac">
        {done}/{total}
      </span>
      <span className="td-gauge gauge-bar">
        <span className={'gauge-fill ' + gaugeColor} style={{ width: pct + '%' }} />
      </span>
    </span>
  )

  return (
    <BlockShell icon="📋" title="Todos" brief={brief} accent="none" defaultOpen={false}>
      {/* 折叠态外的提示:当前进行项(展开后由全列表替代,这里始终给上下文一行) */}
      {cur && (
        <div className="td-cur">
          <span className="td-cur-m">◐</span>
          {cur.activeForm || cur.content}
        </div>
      )}
      <ul className="todos td-list">
        {todos.map((t, i) => (
          <li key={i} className={'todo-' + t.status}>
            <span className="todo-m">{TODO_MARK[t.status] || '○'}</span>
            {t.content}
          </li>
        ))}
      </ul>
    </BlockShell>
  )
}

// ── AgentCard:覆盖 name===Agent 或 Task。历史卡(简化版)。 ──
// 重构后子代理改用 SubagentView「直接复用主界面消息流渲染」(见 SubagentView.tsx / AgentDock.tsx),
// 故本卡不再内嵌复杂嵌套时间线,只保留:图标 🤖 / 标题(description)/ 状态徽标 / 指令段 / 最终产出 result,
// 并给一个「查看记录」按钮 → openSubagent 在弹层里看完整子代理 transcript。
// running 由 useToolStatus 推导(历史卡多为 false);后台 agent 的 async_launched 结果会被判为「已完成」,
//   但历史回看场景下这无碍 —— 运行中跟随由 AgentDock 钻入(running=true)负责。
export function AgentCard({ block }: { block: Block; depth?: number; live?: boolean }) {
  const input = asInput(block)
  const { host, sid } = useSession()
  const { status, res } = useToolStatus(block)
  const desc = str(input, 'description')
  const subtype = str(input, 'subagent_type')
  const prompt = str(input, 'prompt')
  const toolUseId = block.id || ''
  const running = status === 'running'

  const headerExtra = subtype ? <span className="pill tk-type">{subtype}</span> : undefined

  return (
    <BlockShell
      icon="🤖"
      title={desc || 'Agent'}
      accent="task"
      status={status}
      defaultOpen={true}
      headerExtra={headerExtra}
    >
      {/* (a) 指令:子代理 prompt;默认收起的弱折叠段 */}
      {prompt && (
        <Section label="指令" defaultOpen={false}>
          <div className="tk-prompt">
            <MD text={prompt} />
          </div>
        </Section>
      )}

      {/* (b) 查看完整记录:打开 SubagentView 弹层(复用主界面渲染 + 视觉区隔 + 嵌套天然叠层)。 */}
      {toolUseId && (
        <button
          className="tk-view"
          onClick={() =>
            openSubagent({ host, sid, toolUseId, agentType: subtype, description: desc, running })
          }
        >
          <span className="tk-view-i">🤖</span>
          查看记录
        </button>
      )}

      {/* (c) 最终产出 result(有则显示)。 */}
      {res && res.content && (
        <Section label="产出" defaultOpen={true}>
          <ResultPane content={res.content} isError={res.isError} variant="md" />
        </Section>
      )}
    </BlockShell>
  )
}

/* 暂时屏蔽:子代理改用 SubagentView 复用主界面(详见 AgentDock / SubagentView)。
   以下为旧的「嵌套时间线」实现 —— SubTimeline / DeepFold / SubFoot / subReducer / 递归 renderItems —— 保留备查,不再调用。
   注:为保持 build 绿(noUnusedLocals),整段以块注释封存;如需恢复请连同 MetaTools 顶部相关 import 一并还原。

// 局部子时间线状态:items(去重后)+ 已见键集合 + 结果表(供子工具卡配对)+ 游标。
interface SubState {
  items: Item[]
  seen: Set<string>
  results: ResultMap
  cursor: number
}
type SubAction =
  | { type: 'set'; items: Item[]; cursor: number }
  | { type: 'append'; item: Item; cursor: number }
const emptySub = (): SubState => ({ items: [], seen: new Set<string>(), results: {}, cursor: 0 })
function subReducer(s: SubState, a: SubAction): SubState {
  if (a.type === 'set') {
    const seen = new Set<string>()
    const items: Item[] = []
    for (const it of a.items) {
      const k = itemKey(it)
      if (seen.has(k)) continue
      seen.add(k)
      items.push(it)
    }
    const results: ResultMap = {}
    indexResults(items, results)
    return { items, seen, results, cursor: a.cursor }
  }
  const k = itemKey(a.item)
  if (s.seen.has(k)) return { ...s, cursor: Math.max(s.cursor, a.cursor) }
  const seen = new Set(s.seen)
  seen.add(k)
  const results = { ...s.results }
  indexResults([a.item], results)
  return { items: [...s.items, a.item], seen, cursor: Math.max(s.cursor, a.cursor), results }
}

function DeepFold({ host, sid }: { host: string; sid: string }) {
  // 默认无外部终端直链(ccfly 自带 /term 是 WS);仅当消费方配了 terminalUrl 才渲染链接。
  const url = terminalUrl(host, sid)
  if (!url) {
    return (
      <span className="tk-deep">
        <span className="tk-deep-i">⌨</span>
        更深层子代理活动 —— 去终端查看
      </span>
    )
  }
  return (
    <a className="tk-deep" href={url} target="_blank" rel="noreferrer">
      <span className="tk-deep-i">⌨</span>
      更深层子代理活动 —— 去终端查看
    </a>
  )
}

function SubTimeline({
  open,
  onToggle,
  loaded,
  degraded,
  running,
  sub,
  depth,
  res,
}: {
  open: boolean
  onToggle: () => void
  loaded: boolean
  degraded: boolean
  running: boolean
  sub: SubState
  depth: number
  res?: { content: string; isError: boolean }
}) {
  if (degraded) {
    return res ? (
      <Section label="产出" defaultOpen={true}>
        <ResultPane content={res.content} isError={res.isError} variant="md" />
      </Section>
    ) : (
      <div className="tk-wait">子代理运行中…</div>
    )
  }

  return (
    <div className="tk-tl">
      <button className={'tk-tl-h' + (open ? ' tk-tl-h--on' : '')} onClick={onToggle}>
        <span className="tk-tl-chev">{open ? '▾' : '▸'}</span>
        子代理时间线
        {open && loaded && <span className="tk-tl-cnt">{sub.items.length} 步</span>}
        {running && <span className="tk-tl-live" aria-label="运行中" />}
      </button>
      {open && (
        <div className="tk-sub">
          {!loaded ? (
            <div className="tk-wait">载入子时间线…</div>
          ) : sub.items.length === 0 ? (
            <div className="tk-wait">{running ? '子代理运行中…' : '无活动记录'}</div>
          ) : (
            <SubResultContext.Provider value={sub.results}>
              {renderItems(sub.items, depth + 1)}
            </SubResultContext.Provider>
          )}
          {loaded && (running ? <SubFoot running /> : <SubFoot running={false} />)}
        </div>
      )}
    </div>
  )
}

function SubFoot({ running }: { running: boolean }) {
  return running ? (
    <div className="tk-foot tk-foot--run">
      <span className="tk-foot-dot" />
      子代理进行中…
    </div>
  ) : (
    <div className="tk-foot tk-foot--ok">✓ 子代理完成</div>
  )
}
*/

// ── PlanCard:ExitPlanMode。accent=plan,icon=🧭,defaultOpen=true 的弱折叠。 ──
// brief = 待确认 / 已批准 pill(有结果即视为已批准);MD 渲 plan。
export function PlanCard({ block }: { block: Block }) {
  const input = asInput(block)
  const { status, res } = useToolStatus(block)
  const plan = str(input, 'plan')
  const approved = !!res && !res.isError
  const brief = (
    <span className={'pill ' + (approved ? 'on' : 'warn')}>{approved ? '已批准' : '待确认'}</span>
  )

  return (
    <BlockShell icon="🧭" title="Plan" brief={brief} accent="plan" status={status} defaultOpen={true}>
      <div className="pl-body">
        <MD text={plan} />
      </div>
      {res && res.content && (
        <Section label="回应" defaultOpen={true}>
          <ResultPane content={res.content} isError={res.isError} variant="md" />
        </Section>
      )}
    </BlockShell>
  )
}

// ── AskUserQuestionCard:覆盖 name==='AskUserQuestion'。纯展示一次「向用户提问」的记录。 ──
// 数据形态(基于实测会话):
//   input  = { questions: [{ question, header, multiSelect, options: [{ label, description, preview? }] }] }
//   result = 文本「Your questions have been answered: "<问题>"="<答案>", …. You can now …」
//            答案以「问题文本」为键,值要么是命中的 option.label,要么是用户自由作答的文本。
// accent=plan(绿,与决策/计划同族);status 用 useToolStatus(无 result=等待回答中,有=ok)。
// 已答高亮:把 result 解析成 {问题→答案},某 option.label 与该问题的答案文本互含即标 ✓;匹配不到不强标。
interface AqOption {
  label: string
  description?: string
  preview?: string
}
interface AqQuestion {
  question: string
  header?: string
  multiSelect?: boolean
  options: AqOption[]
}
export function AskUserQuestionCard({ block }: { block: Block }) {
  const input = asInput(block)
  const { status, res } = useToolStatus(block)
  const questions = (Array.isArray(input.questions) ? input.questions : []) as AqQuestion[]
  // result 文本 → {问题文本: 答案文本};无 result = 仍在等待回答。
  const answers = res ? parseAqAnswers(res.content) : null
  const title = questions.length > 0 ? questions[0].header || '提问' : '提问'
  const brief =
    questions.length > 1 ? <span className="aq-cnt">{questions.length} 问</span> : undefined

  return (
    <BlockShell icon="🗳" title={title} brief={brief} accent="plan" status={status} defaultOpen={true}>
      {!res && <div className="aq-wait">等待回答…</div>}
      {questions.map((q, qi) => {
        const ans = answers ? answerFor(answers, q.question) : ''
        return (
          <div className="aq-q" key={qi}>
            <div className="aq-qhead">
              {q.header && <span className="aq-chip">{q.header}</span>}
              <span className="aq-stem">{q.question}</span>
            </div>
            <div className="aq-opts">
              {q.options.map((o, oi) => {
                const picked = !!ans && optionMatches(o.label, ans)
                const mark = q.multiSelect ? (picked ? '☑' : '□') : picked ? '●' : '○'
                return (
                  <div className={'aq-opt' + (picked ? ' aq-opt--on' : '')} key={oi}>
                    <span className="aq-mark">{mark}</span>
                    <div className="aq-otext">
                      <div className="aq-label">
                        {o.label}
                        {picked && <span className="aq-tick">✓</span>}
                      </div>
                      {o.description && <div className="aq-desc">{o.description}</div>}
                      {o.preview && (
                        <Section label="预览" defaultOpen={false}>
                          <PreviewBox text={o.preview} />
                        </Section>
                      )}
                    </div>
                  </div>
                )
              })}
            </div>
          </div>
        )
      })}
    </BlockShell>
  )
}

// option.preview:等宽折叠框(超 14 行截高 + 渐隐,展开/收起复用基座 Collapsible)。
function PreviewBox({ text }: { text: string }) {
  const count = text.split('\n').length
  return (
    <Collapsible lines={14} count={count} fade>
      <pre className="aq-preview">{text || ' '}</pre>
    </Collapsible>
  )
}

// 解析回答文本 → 「问题→答案」对。形态:Your questions have been answered: "Q"="A", "Q2"="A2". You can now…
// 用配对正则抓所有 "…"="…";解析不出时返回空表(则无任何高亮,符合「匹配不到不强标」)。
function parseAqAnswers(content: string): Array<{ q: string; a: string }> {
  const out: Array<{ q: string; a: string }> = []
  if (!content) return out
  const re = /"((?:[^"\\]|\\.)*)"\s*=\s*"((?:[^"\\]|\\.)*)"/g
  let m: RegExpExecArray | null
  while ((m = re.exec(content)) !== null) {
    out.push({ q: unq(m[1]), a: unq(m[2]) })
  }
  return out
}
// 还原被转义的引号/反斜杠。
function unq(s: string): string {
  return s.replace(/\\(["\\])/g, '$1')
}
// 取某问题对应的答案:先精确等于,再退化为互含(应对题干前后空白/标点细差)。
function answerFor(pairs: Array<{ q: string; a: string }>, question: string): string {
  const q = question.trim()
  const exact = pairs.find((p) => p.q.trim() === q)
  if (exact) return exact.a
  const loose = pairs.find((p) => p.q.includes(q) || q.includes(p.q))
  return loose ? loose.a : ''
}
// option.label 是否被该问题的答案命中:互含即可(答案多为字面 label,也可能是含 label 的自由作答)。
function optionMatches(label: string, answer: string): boolean {
  const l = label.trim()
  const a = answer.trim()
  if (!l || !a) return false
  return a === l || a.includes(l) || l.includes(a)
}

// ── SkillCard:accent=skill,icon=⚙,title=技能·名;参数走 .kv;result 走 ResultPane md。 ──
export function SkillCard({ block }: { block: Block }) {
  const input = asInput(block)
  const { status, res } = useToolStatus(block)
  // 技能名:优先 command/name/skill 字段。
  const skill = str(input, 'command') || str(input, 'name') || str(input, 'skill') || block.name || 'skill'
  // 参数行:除技能名字段外的标量入参,展示成 .kv。
  const rows = scalarRows(input, ['command', 'name', 'skill'])

  return (
    <BlockShell icon="⚙" title={'技能 · ' + skill} accent="skill" status={status} defaultOpen={false}>
      {rows.length > 0 && (
        <div className="kv sk-kv">
          {rows.map((r, i) => (
            <div className="kv-row" key={i}>
              <span className="kv-k">{r.k}</span>
              <span className="kv-v">{r.v}</span>
            </div>
          ))}
        </div>
      )}
      {res && res.content && (
        <Section label="产出" defaultOpen={true}>
          <ResultPane content={res.content} isError={res.isError} variant="md" />
        </Section>
      )}
    </BlockShell>
  )
}

// ── McpCard:name 去 mcp__ 前缀按 __ 拆 server/tool。accent=mcp,icon=🔌。 ──
// input → CodeBlock json;result → ResultPane(mono)。
export function McpCard({ block }: { block: Block }) {
  const input = asInput(block)
  const { status, res } = useToolStatus(block)
  const { server, tool } = parseMcpName(block.name || '')

  const title = (
    <span className="mc-name">
      {server && <span className="mc-srv">{server}</span>}
      {server && <span className="mc-sep">·</span>}
      <span className="mc-tool">{tool}</span>
    </span>
  )

  const json = JSON.stringify(input, null, 2)
  return (
    <BlockShell icon="🔌" title={title} brief={server ? 'MCP' : 'MCP 工具'} accent="mcp" status={status} defaultOpen={false}>
      {Object.keys(input).length > 0 && <CodeBlock code={json} lang="json" />}
      {res && res.content && (
        <Section label="结果" defaultOpen={true}>
          <ResultPane content={res.content} isError={res.isError} variant="mono" />
        </Section>
      )}
    </BlockShell>
  )
}

// ── GenericCard:兜底。accent=unknown,icon=🧩,title=name,brief=briefOf。 ──
// input → CodeBlock json;result → ResultPane(mono)。
export function GenericCard({ block }: { block: Block }) {
  const input = asInput(block)
  const { status, res } = useToolStatus(block)
  const name = block.name || 'tool'
  const brief = briefOf(input)
  const json = JSON.stringify(input, null, 2)

  return (
    <BlockShell icon="🧩" title={name} brief={brief} accent="unknown" status={status} defaultOpen={false}>
      {Object.keys(input).length > 0 && <CodeBlock code={json} lang="json" />}
      {res && res.content && (
        <Section label="结果" defaultOpen={true}>
          <ResultPane content={res.content} isError={res.isError} variant="mono" />
        </Section>
      )}
    </BlockShell>
  )
}

// ── 卡内小节:带标签的二级折叠段(指令/产出/结果等)。卡级折叠由 BlockShell 承担,
// 本组件做卡内的「段」开合(只受本地 defaultOpen 控制)。 ──
function Section({ label, defaultOpen, children }: { label: string; defaultOpen: boolean; children: ReactNode }) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <div className="mt-sec">
      <button className="mt-sec-h" onClick={() => setOpen((o) => !o)}>
        <span className="mt-sec-chev">{open ? '▾' : '▸'}</span>
        {label}
      </button>
      {open && <div className="mt-sec-body">{children}</div>}
    </div>
  )
}

// ── 标量入参 → kv 行(跳过 skip 列出的字段与对象/数组类型)。 ──
function scalarRows(input: Record<string, unknown>, skip: string[]): Array<{ k: string; v: string }> {
  const out: Array<{ k: string; v: string }> = []
  for (const k of Object.keys(input)) {
    if (skip.includes(k)) continue
    const v = input[k]
    if (v == null) continue
    if (typeof v === 'object') continue
    out.push({ k, v: String(v) })
  }
  return out
}

// ── 解析 MCP 工具名:mcp__server__tool → {server, tool};拆不出时整体当 tool。 ──
export function parseMcpName(name: string): { server: string; tool: string } {
  const stripped = name.replace(/^mcp__/, '')
  const parts = stripped.split('__')
  if (parts.length >= 2) return { server: parts[0], tool: parts.slice(1).join('__') }
  return { server: '', tool: stripped || name }
}
