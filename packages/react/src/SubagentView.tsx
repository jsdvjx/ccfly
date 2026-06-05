// 子代理视图(SubagentView)+ 模块级栈式弹层 host。
// 设计:照 blocks/shell.tsx 的 openReader/ReaderHost 模式 —— 模块级 store + 一处挂载 host(App 根)+ 任意处调用 open。
// 复用优先:子代理 jsonl 与主会话同构,直接用 <TranscriptView> 渲染(就是「用主界面」),只加视觉区隔横幅 + accent 色调。
// 栈式叠加:openSubagent 推入数组 state;子代理视图里再点开子-子代理就叠一层,天然处理嵌套(不再用 depth 上限/递归)。
import { useEffect, useReducer, useRef, useState, useSyncExternalStore } from 'react'
import {
  fetchSubtranscript,
  streamSubtranscript,
  fetchRunningAgents,
  fetchWorkflowAgent,
  streamWorkflowAgent,
} from './api'
import { itemKey, indexResults } from './store'
import { TranscriptView } from './components'
import { SessionContext } from './blocks/ctx'
import { SubResultContext, type ResultMap } from './blocks/shell'
import type { Item } from './types'

// ── 打开参数:host/sid + toolUseId 定位子 jsonl;agentType/description 来自调用方(也能从 meta 补全);running 控实时跟随。 ──
// workflow 钻入:传 runId+agentId 时改走 /workflowagent(否则照旧用 toolUseId 走 /subtranscript)。两路渲染 100% 共用。
export interface SubagentArgs {
  host: string
  sid: string
  toolUseId: string
  agentType?: string
  description?: string
  running?: boolean
  // workflow agent 定位(二者皆有时优先于 toolUseId,fetch/stream 改走 /workflowagent)。
  runId?: string
  agentId?: string
}

// ── 模块级栈 store(照 readerState 模式,但用数组以支持叠层)──
let stack: SubagentArgs[] = []
const subs = new Set<() => void>()
function emit() {
  for (const fn of subs) fn()
}
// 外部 API:任意处调用即压栈打开一层子代理视图。
export function openSubagent(args: SubagentArgs) {
  stack = [...stack, args]
  emit()
}
function popSubagent() {
  stack = stack.slice(0, -1)
  emit()
}
function subscribe(fn: () => void) {
  subs.add(fn)
  return () => {
    subs.delete(fn)
  }
}
function snapshot() {
  return stack
}

// 挂载一次:在 main.tsx 与 <App/>/<ReaderHost/> 并列。监听模块级栈,逐层渲染弹层(后入者叠在上方)。
export function SubagentHost() {
  const cur = useSyncExternalStore(subscribe, snapshot, snapshot)
  if (cur.length === 0) return null
  return (
    <>
      {cur.map((args, i) => (
        <SubagentView
          key={i}
          args={args}
          // 仅最顶层的 ✕ 关闭最顶层(逐层弹出);深层各自的 ✕ 也只弹自己(数组末位)。
          onClose={popSubagent}
        />
      ))}
    </>
  )
}

// ── 子时间线本地状态:items(去重后)+ 已见键集合 + 游标 + 子工具结果表(供嵌套工具卡配对)。 ──
interface SubState {
  items: Item[]
  seen: Set<string>
  results: ResultMap
  cursor: number
}
type SubAction =
  | { type: 'set'; items: Item[]; cursor: number }
  | { type: 'append'; items: Item[]; cursor: number }
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
  // append:增量(SSE / 收尾补拉),逐条去重后并入并刷新结果表。
  const seen = new Set(s.seen)
  const fresh: Item[] = []
  for (const it of a.items) {
    const k = itemKey(it)
    if (seen.has(k)) continue
    seen.add(k)
    fresh.push(it)
  }
  const cursor = Math.max(s.cursor, a.cursor)
  if (fresh.length === 0) return { ...s, cursor }
  const results = { ...s.results }
  indexResults(fresh, results)
  return { items: [...s.items, ...fresh], seen, cursor, results }
}

// ── 单层子代理视图:横幅(身份 + 状态)+ TranscriptView 复用主界面渲染。 ──
function SubagentView({ args, onClose }: { args: SubagentArgs; onClose: () => void }) {
  const { host, sid, toolUseId, runId, agentId } = args
  // workflow 模式:runId+agentId 齐备时 fetch/stream 改走 /workflowagent;否则走 toolUseId 的 /subtranscript。
  const wf = !!(runId && agentId)
  // 「能定位」标识:两路任一齐备即可初拉/连流(替代旧的「仅看 toolUseId」)。
  const locatable = wf || !!toolUseId
  const [sub, dispatch] = useReducer(subReducer, undefined, emptySub)
  const [agentType, setAgentType] = useState(args.agentType || '')
  const [description, setDescription] = useState(args.description || '')
  // running 由父层初始给定;运行中靠轮询 /subagents 探测「已不在列表 → 完成」,转 false。
  const [running, setRunning] = useState(!!args.running)
  const [loaded, setLoaded] = useState(false)
  const curCursor = useRef(0)
  curCursor.current = sub.cursor

  // 挂载即初拉(也从返回 meta 补全 agentType/description)。两路同构,只是定位 URL 不同。
  useEffect(() => {
    if (!locatable) {
      setLoaded(true)
      return
    }
    let alive = true
    const init = wf ? fetchWorkflowAgent(host, sid, runId!, agentId!) : fetchSubtranscript(host, sid, toolUseId)
    init
      .then((r) => {
        if (!alive) return
        dispatch({ type: 'set', items: r.items || [], cursor: r.cursor || 0 })
        if (r.meta?.agentType) setAgentType((v) => v || r.meta!.agentType!)
        if (r.meta?.description) setDescription((v) => v || r.meta!.description!)
        setLoaded(true)
      })
      .catch(() => {
        if (alive) setLoaded(true)
      })
    return () => {
      alive = false
    }
  }, [host, sid, toolUseId, wf, runId, agentId, locatable])

  // running → SSE 实时追加(去重用 itemKey);running 转 false / 卸载时收流。两路同构,只是 URL 不同。
  useEffect(() => {
    if (!running || !loaded || !locatable) return
    const onItem = (cursor: number, item: unknown) => {
      dispatch({ type: 'append', items: [item as Item], cursor })
    }
    const close = wf
      ? streamWorkflowAgent(host, sid, runId!, agentId!, curCursor.current, onItem)
      : streamSubtranscript(host, sid, toolUseId, curCursor.current, onItem)
    return close
    // curCursor 经 ref 取值,不入依赖(避免重连);running 转 false 即收流。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running, loaded, host, sid, toolUseId, wf, runId, agentId, locatable])

  // 运行→完成判定(修 review M1):running 时每 ~2s 轮询 /subagents;该 toolUseId 不再在列表
  //   → 视为完成:since=游标补拉一次尾部把末几条补齐,再置 running=false(上面 SSE effect 随之收流)。
  //   绝不像旧 drill 永远「运行中」。
  // 注:/subagents 仅对「主 jsonl 子代理」有效;workflow 钻入(wf)的运行态由上游 WorkflowCard 用
  //   fetchWorkflow 轮询掌握,这里不接入 /subagents(否则 workflow agent 永远「不在列表」→ 误判秒完成),
  //   wf 模式只靠 SSE 跟随,不在此做完成翻转。
  useEffect(() => {
    if (wf || !running || !loaded || !toolUseId) return
    let alive = true
    const t = window.setInterval(() => {
      fetchRunningAgents(host, sid)
        .then((list) => {
          if (!alive) return
          if (!list.some((a) => a.toolUseId === toolUseId)) {
            // 收尾补拉:SSE 尾部可能没追上,用游标补齐一次(append 去重,重复无害)。
            fetchSubtranscript(host, sid, toolUseId, curCursor.current)
              .then((r) => {
                if (alive && r.items?.length) dispatch({ type: 'append', items: r.items, cursor: r.cursor || 0 })
              })
              .catch(() => {})
              .finally(() => {
                if (alive) setRunning(false)
              })
          }
        })
        .catch(() => {})
    }, 2000)
    return () => {
      alive = false
      clearInterval(t)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wf, running, loaded, host, sid, toolUseId])

  return (
    <div className="sav">
      <div className="sav-box" onClick={(e) => e.stopPropagation()}>
        {/* 视觉区隔横幅:🤖 + 「子代理」标签 + agentType pill + description;running 脉动「进行中」、done「✓ 完成」;✕ 关(不点外关)。 */}
        <div className="sav-banner">
          <span className="sav-bot">🤖</span>
          <span className="sav-tag">子代理</span>
          {agentType && <span className="pill sav-type">{agentType}</span>}
          {description && <span className="sav-desc">{description}</span>}
          <span className="sav-spacer" />
          {running ? (
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
        {/* 正文:复用主界面渲染。外层提供 host/sid(供嵌套 AgentCard 等),并用 SubResultContext 喂子工具结果表。 */}
        <div className="sav-body">
          <SessionContext.Provider value={{ host, sid }}>
            <SubResultContext.Provider value={sub.results}>
              {!loaded ? (
                <div className="sav-wait">载入子代理记录…</div>
              ) : sub.items.length === 0 ? (
                <div className="sav-wait">{running ? '子代理运行中…' : '无活动记录'}</div>
              ) : (
                <TranscriptView items={sub.items} />
              )}
            </SubResultContext.Provider>
          </SessionContext.Provider>
        </div>
      </div>
    </div>
  )
}
