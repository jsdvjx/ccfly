import { useEffect, useState } from 'react'
import { fetchRunningAgents, fetchState, tmuxName, type RunningAgent, type CtrlState } from './api'
import { useLiveState, useLiveDegraded } from './livestate'
import { openSubagent } from './SubagentView'
import { openWorkflow } from './blocks/WorkflowCard'

// 表世界「运行中子代理概览」—— 映射里世界 TUI 底部 `↓ to manage` 的 running-agent 概览。
// 形态:有子代理在跑时,输入框上方常驻一条迷你状态条(N 个运行中 + 脉动点);点开展开列表
//(顶部 main 行复用主代理 busy 态 + 各运行中子代理行,右侧 elapsed);点某行钻入弹层看其实时内容。
// 数据:运行中列表来自后端 /subagents(权威,按启动↔完成事件配对);main 行来自 /state。无子代理则整体消失。

const SPIN = ['◐', '◓', '◑', '◒'] // 脉动月相,示意「运行中」
const fmtDur = (s: number) => (s < 60 ? s + 's' : Math.floor(s / 60) + 'm' + (s % 60) + 's')
function elapsedOf(startedAt: string, nowMs: number): string {
  const t = Date.parse(startedAt)
  if (isNaN(t)) return ''
  return fmtDur(Math.max(0, Math.floor((nowMs - t) / 1000)))
}

export function AgentDock({ host, sid }: { host: string; sid: string }) {
  const tsess = tmuxName(sid)
  const [agents, setAgents] = useState<RunningAgent[]>([])
  const [open, setOpen] = useState(false)
  const [frame, setFrame] = useState(0)
  const [now, setNow] = useState(() => Date.now())

  // 主代理状态(供 main 行):WS 镜像在线 → useLiveState;降级 → 回退 /state 轮询。
  const liveSt = useLiveState()
  const degraded = useLiveDegraded()
  const [polledSt, setPolledSt] = useState<CtrlState>({ kind: 'input' })
  const st = degraded ? polledSt : liveSt

  // 运行中子代理列表:WS 派生不了(它要按「启动↔完成事件」配对扫主 jsonl),故 /subagents 轮询恒保留。
  useEffect(() => {
    let alive = true
    const poll = () => {
      fetchRunningAgents(host, sid).then((a) => alive && setAgents(a)).catch(() => {})
    }
    poll()
    const t = window.setInterval(poll, 2000)
    return () => {
      alive = false
      clearInterval(t)
    }
  }, [host, sid])

  // main 行状态:仅降级时轮询 /state;WS 在线靠 useLiveState。
  useEffect(() => {
    if (!degraded) return
    let alive = true
    const poll = () => {
      fetchState(host, tsess).then((s) => alive && setPolledSt(s)).catch(() => {})
    }
    poll()
    const t = window.setInterval(poll, 2000)
    return () => {
      alive = false
      clearInterval(t)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [host, sid, degraded])

  // 脉动 + elapsed 计时:仅在有子代理在跑时转(省电)。
  const busy = agents.length > 0
  useEffect(() => {
    if (!busy) return
    const a = window.setInterval(() => setFrame((f) => (f + 1) % SPIN.length), 500)
    const b = window.setInterval(() => setNow(Date.now()), 1000)
    return () => {
      clearInterval(a)
      clearInterval(b)
    }
  }, [busy])

  // 没有运行中子代理 → 整条消失(自动隐现)。
  if (!agents.length) {
    return null
  }

  const mainBusy = st.kind === 'busy'
  // 钻入。普通子代理 → SubagentView 弹层(复用主界面渲染);running=true 强制实时跟随,完成判定由其自身轮询 /subagents 处理。
  //   workflow → openWorkflow 弹层(WorkflowCard 同构详情:phase/agent + 钻入单 agent),按 runId 拉详情。
  const openDrill = (a: RunningAgent) => {
    if (a.kind === 'workflow') {
      if (!a.runId) return // runId 未补全(async_launched 还没到)→ 暂不可钻入
      openWorkflow({ host, sid, runId: a.runId, name: a.description, running: true })
      return
    }
    openSubagent({
      host,
      sid,
      toolUseId: a.toolUseId,
      agentType: a.agentType,
      description: a.description,
      running: true,
    })
  }

  // 概览计数:子代理数 / 工作流数(分别报,任一为 0 则省略其分句)。
  const wfCount = agents.filter((a) => a.kind === 'workflow').length
  const subCount = agents.length - wfCount
  const countLabel = [
    subCount > 0 ? subCount + ' 个子代理' : '',
    wfCount > 0 ? wfCount + ' 个工作流' : '',
  ]
    .filter(Boolean)
    .join(' · ') + '运行中'

  return (
    <div className="adock">
      <button className="adock-bar" onClick={() => setOpen((o) => !o)}>
        <span className="adock-spin">{SPIN[frame]}</span>
        <span className="adock-n">{countLabel}</span>
        <span className="adock-chev">{open ? '▾' : '▸'}</span>
      </button>

      {open && (
        <div className="adock-list">
          {/* main 行:主代理当前活动(复用 /state busy 态)*/}
          <div className="adock-row adock-main">
            <span className="adock-glyph">⏺</span>
            <span className="adock-type">main</span>
            <span className="adock-desc">{mainBusy ? st.verb || '工作中…' : '空闲'}</span>
            <span className="adock-meta">{mainBusy ? st.elapsed || '' : ''}</span>
          </div>
          {/* 各运行中条目行 → 点击钻入。workflow 行用 🧩 图标 + 「工作流」类型 + adock-wf 样式区隔。 */}
          {agents.map((a) => {
            const isWF = a.kind === 'workflow'
            return (
              <button
                key={a.toolUseId}
                className={'adock-row adock-sub' + (isWF ? ' adock-wf' : '')}
                onClick={() => openDrill(a)}
              >
                <span className={'adock-glyph adock-run'}>{isWF ? '🧩' : '◯'}</span>
                <span className="adock-type">{isWF ? '工作流' : a.agentType || 'agent'}</span>
                <span className="adock-desc">{a.description || '(无描述)'}</span>
                <span className="adock-meta">{elapsedOf(a.startedAt, now)}</span>
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}
