// 批次 · Workflow(编排运行)卡:折叠头 = 🧩 名称 + 摘要副标 + 「N phase · M agent」+ 状态 pill;
// 展开 = 按 phase 分组(Section,组头 = phaseTitle + agent 数),组内每 agent 一行(glyph + label + 副标 + 状态徽标 + 计时);
// 底部汇总 totalTokens · totalToolCalls · durationMs。运行中(整体 status 非终态)~2s 轮询 fetchWorkflow 刷新。
// runId 取法:Workflow 的 tool_use 没把 runId 放进 input,但其 tool_result 文本里有「Run ID: wf_…」——
//   useToolStatus(block).res.content 正是该结果文本,正则抠出 runId(同一个 runId 还供 agent 钻入用)。
//   兜底:正则没命中时(老格式/结果未到)用 block.id 作 toolUseId 调 fetchWorkflowByToolUse,后端反查 runId 出摘要
//   (此兜底路径前端拿不回 runId,故 agent 钻入按钮在该情形下隐藏)。
// 钻入:点 agent 行 → openSubagent({ runId, agentId, … }),复用 SubagentView 的 workflow 分支。
import { useEffect, useState, useSyncExternalStore, type ReactNode } from 'react'
import { BlockShell, useToolStatus } from './shell'
import { useSession } from './ctx'
import { openSubagent } from '../SubagentView'
import { fetchWorkflow, fetchWorkflowByToolUse } from '../api'
import type { Block, WorkflowDetail, WorkflowAgent } from '../types'

// ── 终态判定:这些 status 视为「已结束」(停轮询、不脉动)。其余(running/pending/空)= 进行中。 ──
const TERMINAL = new Set(['completed', 'failed', 'error', 'cancelled', 'canceled', 'done', 'success'])
function isTerminal(status?: string): boolean {
  return !!status && TERMINAL.has(status.toLowerCase())
}

// ── 从结果文本抠 runId:「Run ID: wf_xxxx」。无 → ''。 ──
function parseRunId(content?: string): string {
  if (!content) return ''
  const m = content.match(/Run ID:\s*(wf_[A-Za-z0-9-]+)/)
  return m ? m[1] : ''
}
// ── 从结果文本抠摘要(摘要副标在 fetch 前先有个兜底):「Summary: …」首行。 ──
function parseSummary(content?: string): string {
  if (!content) return ''
  const m = content.match(/^Summary:\s*(.+)$/m)
  return m ? m[1].trim() : ''
}

// ── agent 状态 → 徽标(文案 + pill 配色类)。 ──
function agentStateBadge(state?: string): { label: string; cls: string } {
  const s = (state || '').toLowerCase()
  if (s === 'done' || s === 'completed' || s === 'success') return { label: '完成', cls: 'on' }
  if (s === 'failed' || s === 'error') return { label: '失败', cls: 'warn' }
  if (s === 'running' || s === 'in_progress') return { label: '运行中', cls: 'run' }
  if (s === 'pending' || s === 'queued' || s === 'waiting') return { label: '待运行', cls: 'off' }
  return { label: state || '', cls: 'off' }
}

// ── 整体状态 pill(折叠头右侧)。 ──
function statusPill(status?: string): ReactNode {
  const s = (status || '').toLowerCase()
  if (!status) return <span className="pill wfc-st">运行中</span>
  if (s === 'completed' || s === 'success' || s === 'done') return <span className="pill on">已完成</span>
  if (s === 'failed' || s === 'error') return <span className="pill warn">失败</span>
  if (s === 'cancelled' || s === 'canceled') return <span className="pill off">已取消</span>
  if (s === 'running' || s === 'pending') return <span className="pill wfc-st wfc-st--run">运行中</span>
  return <span className="pill off">{status}</span>
}

// ── 时长格式化(ms → 1m 2s / 1h 3m / 850ms)。 ──
function fmtDur(ms?: number): string {
  if (!ms || ms <= 0) return ''
  if (ms < 1000) return ms + 'ms'
  const s = Math.floor(ms / 1000)
  if (s < 60) return s + 's'
  const m = Math.floor(s / 60)
  const r = s % 60
  if (m < 60) return m + 'm' + (r ? ' ' + r + 's' : '')
  const h = Math.floor(m / 60)
  return h + 'h ' + (m % 60) + 'm'
}
// ── token 紧凑(1234 → 1.2k)。 ──
function fmtTokens(n?: number): string {
  if (!n || n <= 0) return '0'
  if (n < 1000) return String(n)
  if (n < 1_000_000) return (n / 1000).toFixed(n < 10_000 ? 1 : 0) + 'k'
  return (n / 1_000_000).toFixed(1) + 'M'
}

// ── 拉取阶段:载入中 / 就绪(有详情)/ 空(查无此 run)/ 出错。 ──
// 关键:旧实现只有 detail(null|有)一个轴,正文「!detail → 载入工作流…」于是把
//   ① 首拉未回、② 后端 404 返 null(run 文件尚未落盘 / runId 解析不出 / 云端代理转发失败)、
//   ③ fetch 抛错
// 三者全渲成「载入工作流…」并永不退出 —— 这正是卡片「永久 loading」的根因。
// 故引入显式四态:fetch 落定(无论拿到 detail 还是 null)即翻出 loading,空/错有各自终态。
type WfPhase = 'loading' | 'ready' | 'empty' | 'error'

// ── 详情拉取钩子(WorkflowCard 与 overlay 共用):优先 runId(可钻入),否则 toolUseId 兜底反查。 ──
// forceRunning 让无工具三态的调用方(overlay)在拿到详情前先按「运行中」轮询,直到详情给出终态。
function useWorkflowDetail(
  host: string,
  sid: string,
  runId: string,
  toolUseId: string,
  forceRunning: boolean,
): { detail: WorkflowDetail | null; running: boolean; phase: WfPhase } {
  const [detail, setDetail] = useState<WorkflowDetail | null>(null)
  const [phase, setPhase] = useState<WfPhase>('loading')
  const running = detail ? !isTerminal(detail.status) : forceRunning
  useEffect(() => {
    // 入参没凑齐(无 sid 或既无 runId 又无 toolUseId)→ 没东西可拉,直接定为「空」,
    // 否则会停在初始 loading 永不动。
    if (!sid || (!runId && !toolUseId)) {
      setPhase('empty')
      return
    }
    let alive = true
    let timer = 0
    // 换 run / 重挂时复位回 loading(上一份详情与阶段不再代表本 run)。
    setDetail(null)
    setPhase('loading')
    // 运行中容错轮询的兜底次数:run 文件还没落盘时(404→null)会连吃几次空。
    // 给个上限,既覆盖「刚启动、文件晚到」的正常窗口,又保证最终一定退出 loading,
    // 不至于因后端始终查不到(已被清理 / runId 解析错)而无限空转。
    const MAX_EMPTY_POLLS = 8 // ×2s ≈ 16s
    let emptyPolls = 0
    const pull = () => {
      const p = runId ? fetchWorkflow(host, sid, runId) : fetchWorkflowByToolUse(host, sid, toolUseId)
      p.then((d) => {
        if (!alive) return
        if (d) {
          // 拿到详情:就绪,清空轮空计数。
          setDetail(d)
          setPhase('ready')
          emptyPolls = 0
          // 仍在运行 → 续轮询刷新;终态停。
          if (!isTerminal(d.status)) timer = window.setTimeout(pull, 2000)
          return
        }
        // d==null:后端 404 / 拿不到。运行中(forceRunning)时再宽限几次(等文件落盘),
        // 用尽 / 非运行中则定为「空」—— 无论如何都退出 loading。
        emptyPolls++
        if (forceRunning && emptyPolls < MAX_EMPTY_POLLS) {
          setPhase('loading')
          timer = window.setTimeout(pull, 2000)
        } else {
          setPhase('empty')
        }
      }).catch(() => {
        if (!alive) return
        // fetch 抛错(网络 / 代理断)。运行中宽限重试;否则定为「错」,不再无限重试。
        emptyPolls++
        if (forceRunning && emptyPolls < MAX_EMPTY_POLLS) {
          setPhase('loading')
          timer = window.setTimeout(pull, 2000)
        } else {
          setPhase('error')
        }
      })
    }
    pull()
    return () => {
      alive = false
      if (timer) clearTimeout(timer)
    }
  }, [host, sid, runId, toolUseId, forceRunning])
  return { detail, running, phase }
}

export function WorkflowCard({ block }: { block: Block }) {
  const { host, sid } = useSession()
  const { status: toolStatus, res } = useToolStatus(block)
  const toolUseId = block.id || ''
  // runId / 兜底摘要 / 兜底名:先从结果文本拿(无需后端)。
  const runId = parseRunId(res?.content)
  const seedSummary = parseSummary(res?.content)

  // 详情 + 运行态(工具三态 running=在飞作 forceRunning 起点)+ 拉取阶段(loading/ready/empty/error)。
  const { detail, running, phase } = useWorkflowDetail(host, sid, runId, toolUseId, toolStatus === 'running')

  const name = detail?.name || '工作流'
  const summary = detail?.summary || seedSummary
  const phases = detail?.phases || []
  const agents = detail?.agents || []
  const phaseCount = phases.length
  const agentCount = agents.length || (detail ? 0 : undefined)

  // 折叠头副标:摘要 +(已取详情后)「N phase · M agent」。
  const brief = (
    <span className="wfc-brief">
      {summary && <span className="wfc-sum">{summary}</span>}
      {(phaseCount > 0 || agentCount != null) && (
        <span className="wfc-counts">
          {phaseCount > 0 && phaseCount + ' phase'}
          {phaseCount > 0 && agentCount != null && ' · '}
          {agentCount != null && agentCount + ' agent'}
        </span>
      )}
    </span>
  )

  // BlockShell.status:整体运行中 → running(头部脉动 spinner);拉取出错 → err;
  // 终态按成功/失败上色。注意 phase==='error' 也算 err,避免错状态还顶着「运行中」脉动。
  const shellStatus =
    phase === 'error'
      ? 'err'
      : running
        ? 'running'
        : detail && (detail.status || '').toLowerCase() === 'failed'
          ? 'err'
          : 'ok'
  const headerExtra = statusPill(detail?.status)

  return (
    <BlockShell
      icon="🧩"
      title={name}
      brief={brief}
      accent="task"
      status={shellStatus}
      defaultOpen={true}
      headerExtra={headerExtra}
    >
      <WorkflowInner host={host} sid={sid} runId={runId} detail={detail} running={running} phase={phase} />
    </BlockShell>
  )
}

// ── 详情正文(phase 分组 + 各 agent 行 + 汇总脚):WorkflowCard 与 overlay 共用。 ──
// phase 四态决定无详情时显示什么:loading=载入中、empty=查无此 run、error=拉取失败。
// 旧实现只看 !detail 一律渲「载入工作流…」→ 404/出错也卡在载入,这是永久 loading 的正文侧表现。
function WorkflowInner({
  host,
  sid,
  runId,
  detail,
  running,
  phase,
}: {
  host: string
  sid: string
  runId: string
  detail: WorkflowDetail | null
  running: boolean
  phase: WfPhase
}) {
  const phases = detail?.phases || []
  const agents = detail?.agents || []
  const groups = groupByPhase(phases, agents)
  return (
    <>
      {!detail ? (
        // 无详情:按拉取阶段给出确定的终态文案,绝不停在 loading。
        phase === 'error' ? (
          <div className="wfc-wait wfc-wait--err">未能载入工作流</div>
        ) : phase === 'empty' ? (
          <div className="wfc-wait">无工作流记录</div>
        ) : (
          <div className="wfc-wait">载入工作流…</div>
        )
      ) : groups.length === 0 ? (
        <div className="wfc-wait">{running ? '工作流运行中…' : '无 agent 记录'}</div>
      ) : (
        groups.map((g, gi) => (
          <PhaseSection key={gi} title={g.title} agents={g.agents}>
            {g.agents.map((a, ai) => (
              <AgentRow key={ai} agent={a} host={host} sid={sid} runId={runId} />
            ))}
          </PhaseSection>
        ))
      )}

      {detail && (
        <div className="wfc-foot">
          {(detail.totalTokens ?? 0) > 0 && (
            <span className="wfc-f">
              <span className="wfc-f-k">tokens</span>
              {fmtTokens(detail.totalTokens)}
            </span>
          )}
          {(detail.totalToolCalls ?? 0) > 0 && (
            <span className="wfc-f">
              <span className="wfc-f-k">tools</span>
              {detail.totalToolCalls}
            </span>
          )}
          {fmtDur(detail.durationMs) && (
            <span className="wfc-f">
              <span className="wfc-f-k">耗时</span>
              {fmtDur(detail.durationMs)}
            </span>
          )}
          {detail.defaultModel && (
            <span className="wfc-f wfc-f--model">
              <span className="wfc-f-k">模型</span>
              {detail.defaultModel}
            </span>
          )}
        </div>
      )}
    </>
  )
}

// ── phase 分组:{ title, agents } 列表。优先用 detail.phases 排序定标题;再把 agent 归入对应 index。 ──
interface PhaseGroup {
  title: string
  agents: WorkflowAgent[]
}
function groupByPhase(phases: { index: number; title: string }[], agents: WorkflowAgent[]): PhaseGroup[] {
  if (agents.length === 0) return []
  if (phases.length > 0) {
    const groups: PhaseGroup[] = phases.map((p) => ({ title: p.title, agents: [] }))
    const byIndex = new Map(phases.map((p, i) => [p.index, i]))
    const extra: WorkflowAgent[] = []
    for (const a of agents) {
      const gi = a.phaseIndex != null ? byIndex.get(a.phaseIndex) : undefined
      if (gi != null) groups[gi].agents.push(a)
      else extra.push(a)
    }
    if (extra.length) groups.push({ title: extra[0].phaseTitle || '其它', agents: extra })
    // 丢掉空 phase(无 agent 的阶段不渲染头)。
    return groups.filter((g) => g.agents.length > 0)
  }
  // 无 phases:按 agent 自带 phaseTitle/phaseIndex 现拼。
  const ordered: PhaseGroup[] = []
  const map = new Map<string, PhaseGroup>()
  for (const a of agents) {
    const key = a.phaseTitle || (a.phaseIndex != null ? 'Phase ' + a.phaseIndex : '阶段')
    let g = map.get(key)
    if (!g) {
      g = { title: key, agents: [] }
      map.set(key, g)
      ordered.push(g)
    }
    g.agents.push(a)
  }
  return ordered
}

// ── phase 小节:组头(title + agent 数)可折叠;默认展开。 ──
function PhaseSection({ title, agents, children }: { title: string; agents: WorkflowAgent[]; children: ReactNode }) {
  const [open, setOpen] = useState(true)
  return (
    <div className="wfc-ph">
      <button className="wfc-ph-h" onClick={() => setOpen((o) => !o)}>
        <span className="wfc-ph-chev">{open ? '▾' : '▸'}</span>
        <span className="wfc-ph-title">{title}</span>
        <span className="wfc-ph-cnt">{agents.length}</span>
      </button>
      {open && <div className="wfc-ph-body">{children}</div>}
    </div>
  )
}

// ── 单 agent 行:glyph + label(主)+ model/lastToolSummary(副)+ 状态徽标 + 计时。点行钻入(有 runId 才可)。 ──
function AgentRow({
  agent,
  host,
  sid,
  runId,
}: {
  agent: WorkflowAgent
  host: string
  sid: string
  runId: string
}) {
  const badge = agentStateBadge(agent.state)
  const sub = agent.lastToolSummary || agent.model || ''
  const dur = fmtDur(agent.durationMs)
  const canDrill = !!(runId && agent.agentId)
  const onOpen = () => {
    if (!canDrill) return
    openSubagent({
      host,
      sid,
      toolUseId: '', // workflow 路:走 runId+agentId,toolUseId 不用
      runId,
      agentId: agent.agentId,
      agentType: agent.model,
      description: agent.label,
      running: badge.cls === 'run',
    })
  }
  return (
    <div
      className={'wfc-row' + (canDrill ? ' wfc-row--clk' : '')}
      onClick={canDrill ? onOpen : undefined}
      role={canDrill ? 'button' : undefined}
    >
      <span className="wfc-row-g">{badge.cls === 'run' ? '◐' : '◆'}</span>
      <span className="wfc-row-main">
        <span className="wfc-row-label">{agent.label || agent.agentId || 'agent'}</span>
        {sub && <span className="wfc-row-sub">{sub}</span>}
      </span>
      {dur && <span className="wfc-row-dur">{dur}</span>}
      {badge.label && <span className={'pill wfc-badge wfc-badge--' + badge.cls}>{badge.label}</span>}
    </div>
  )
}

// ── Workflow overlay(AgentDock workflow 行的钻入入口)──
// WorkflowCard 是块流里由 tool_use 驱动的卡;AgentDock 没有 block(只有 runId),故另起一个轻量弹层:
// 复用 useWorkflowDetail 取详情 + WorkflowInner 渲染同样的 phase/agent 正文,套 .sav 模态壳保持视觉一致。
// 模式同 SubagentHost:模块级栈 store + 一处挂载 host(main.tsx)+ 任意处 openWorkflow 压栈。
export interface WorkflowOverlayArgs {
  host: string
  sid: string
  runId: string
  name?: string // 折叠头兜底名(详情到达前)
  running?: boolean // 由 AgentDock 给:运行中起点,详情到达后以 detail.status 为准
}
let wfStack: WorkflowOverlayArgs[] = []
const wfSubs = new Set<() => void>()
function wfEmit() {
  for (const fn of wfSubs) fn()
}
// 外部 API:压栈打开一层 workflow 详情弹层。
export function openWorkflow(args: WorkflowOverlayArgs) {
  wfStack = [...wfStack, args]
  wfEmit()
}
function popWorkflow() {
  wfStack = wfStack.slice(0, -1)
  wfEmit()
}
function wfSubscribe(fn: () => void) {
  wfSubs.add(fn)
  return () => {
    wfSubs.delete(fn)
  }
}
function wfSnapshot() {
  return wfStack
}

// 挂载一次:在 main.tsx 与 SubagentHost 并列。
export function WorkflowOverlayHost() {
  const cur = useSyncExternalStore(wfSubscribe, wfSnapshot, wfSnapshot)
  if (cur.length === 0) return null
  return (
    <>
      {cur.map((args, i) => (
        <WorkflowOverlay key={i} args={args} onClose={popWorkflow} />
      ))}
    </>
  )
}

// 单层 workflow 详情弹层:.sav 模态壳 + 横幅(🧩 工作流 + 名 + 状态)+ WorkflowInner 正文。
function WorkflowOverlay({ args, onClose }: { args: WorkflowOverlayArgs; onClose: () => void }) {
  const { host, sid, runId } = args
  const { detail, running, phase } = useWorkflowDetail(host, sid, runId, '', !!args.running)
  const name = detail?.name || args.name || '工作流'
  return (
    <div className="sav">
      <div className="sav-box" onClick={(e) => e.stopPropagation()}>
        <div className="sav-banner">
          <span className="sav-bot">🧩</span>
          <span className="sav-tag">工作流</span>
          <span className="sav-desc">{name}</span>
          <span className="sav-spacer" />
          {phase === 'error' ? (
            // 拉取失败:别再顶「✓ 完成」误导,显式标错(.sav-state--err)。
            <span className="sav-state sav-state--err">载入失败</span>
          ) : running ? (
            <span className="sav-state sav-state--run">
              <span className="sav-dot" />
              进行中
            </span>
          ) : (
            <span className="sav-state sav-state--ok">✓ 完成</span>
          )}
          <button className="sav-close" onClick={onClose} aria-label="关闭">
            ✕
          </button>
        </div>
        <div className="sav-body">
          <WorkflowInner host={host} sid={sid} runId={runId} detail={detail} running={running} phase={phase} />
        </div>
      </div>
    </div>
  )
}
