import type { TranscriptResp, SubtranscriptResp, SessionMeta, Info, WorkflowDetail } from './types'
import { stripAnsi } from './blocks/Ansi'
import { getConfig } from './config'

// 缓存改用 IndexedDB(见 idb.ts):无 3MB 上限,大会话整段缓存。
//
// ── 参数化说明(ccfly 抽包)──
// 上游用硬编码 `xb = /x/<host>` 作控制服务前缀,host 经 URL 路由解出。抽包后控制服务前缀
// 改由 CCFlyProvider 注入(config.baseUrl);所有 fetch 走 cfg.fetch(可注入鉴权/SSR/测试桩)。
// 为把组件层改动面降到最小,各导出函数「签名保持不变」(仍收 host 作首参),但 host 不再参与 URL 构造
// —— URL 一律取 getConfig().baseUrl。host 仅在 fetchSessions 里作客户端过滤用(若消费方需要)。

// 控制服务前缀:从 Provider 注入的 config 取(替代硬编码 /x/<host>)。
const xb = () => getConfig().baseUrl
// 统一 fetch 入口(走 Provider 注入的 fetch)。
const xf = (...a: Parameters<typeof fetch>) => getConfig().fetch(...a)

// 用户消息里图片的取字节 URL:后端按 sid+uuid 定位 jsonl 行,取第 idx 个图片(路径式/base64 式合计)。
export function imageUrl(_host: string, sid: string, uuid: string, idx: number): string {
  return (
    xb() +
    '/image?sid=' +
    encodeURIComponent(sid) +
    '&uuid=' +
    encodeURIComponent(uuid) +
    '&idx=' +
    idx
  )
}

// 增量(更新):从 since 字节读到 EOF。无 since=首拉尾窗(等同 fetchTranscriptTail,保留兼容)。
export async function fetchTranscript(_host: string, sid: string, since?: number): Promise<TranscriptResp> {
  const u = xb() + '/transcript?sid=' + encodeURIComponent(sid) + (since ? '&since=' + since : '')
  const r = await xf(u)
  if (!r.ok) throw new Error('transcript ' + r.status)
  return r.json()
}

// 首拉:尾窗(末尾 ~150 条)。返回 { items, cursor:EOF, firstCursor, hasMore }。
export async function fetchTranscriptTail(_host: string, sid: string): Promise<TranscriptResp> {
  const r = await xf(xb() + '/transcript?sid=' + encodeURIComponent(sid))
  if (!r.ok) throw new Error('transcript ' + r.status)
  return r.json()
}

// 上滑:取紧邻 before 之前的更老一窗。before 传上次的 firstCursor 即无缝接续(不重不漏)。
export async function fetchTranscriptOlder(_host: string, sid: string, before: number): Promise<TranscriptResp> {
  const r = await xf(xb() + '/transcript?sid=' + encodeURIComponent(sid) + '&before=' + before)
  if (!r.ok) throw new Error('transcript ' + r.status)
  return r.json()
}

// SSE 实时跟随:每条 { cursor, item }。返回关闭函数。
// 注:EventSource 不经 cfg.fetch(浏览器原生 SSE);URL 仍取 config.baseUrl。
export function streamTranscript(
  _host: string,
  sid: string,
  since: number,
  onItem: (cursor: number, item: unknown) => void,
): () => void {
  let es: EventSource | null = null
  let last = since
  let closed = false
  const connect = (from: number) => {
    es = new EventSource(xb() + '/transcript/stream?sid=' + encodeURIComponent(sid) + '&since=' + from)
    es.onmessage = (ev) => {
      try {
        const m = JSON.parse(ev.data)
        if (typeof m.cursor === 'number') last = m.cursor
        if (m.item) onItem(m.cursor, m.item)
      } catch {
        /* ignore */
      }
    }
    es.onerror = () => {
      if (es) es.close()
      es = null
      if (!closed) setTimeout(() => !closed && connect(last), 2500)
    }
  }
  connect(since)
  return () => {
    closed = true
    if (es) es.close()
  }
}

// ── 子代理 transcript:展开 AgentCard 时懒加载;形同 /transcript 但按 toolUseId 定位子 jsonl,并带 meta ──
export async function fetchSubtranscript(
  _host: string,
  sid: string,
  toolUseId: string,
  since?: number,
): Promise<SubtranscriptResp> {
  const u =
    xb() +
    '/subtranscript?sid=' +
    encodeURIComponent(sid) +
    '&toolUseId=' +
    encodeURIComponent(toolUseId) +
    (since ? '&since=' + since : '')
  const r = await xf(u)
  if (!r.ok) throw new Error('subtranscript ' + r.status)
  return r.json()
}

// 子代理 SSE 实时跟随(运行中且已展开才连):照抄 streamTranscript,URL 换 /subtranscript/stream 且带 toolUseId。
export function streamSubtranscript(
  _host: string,
  sid: string,
  toolUseId: string,
  since: number,
  onItem: (cursor: number, item: unknown) => void,
): () => void {
  let es: EventSource | null = null
  let last = since
  let closed = false
  const connect = (from: number) => {
    es = new EventSource(
      xb() +
        '/subtranscript/stream?sid=' +
        encodeURIComponent(sid) +
        '&toolUseId=' +
        encodeURIComponent(toolUseId) +
        '&since=' +
        from,
    )
    es.onmessage = (ev) => {
      try {
        const m = JSON.parse(ev.data)
        if (typeof m.cursor === 'number') last = m.cursor
        if (m.item) onItem(m.cursor, m.item)
      } catch {
        /* ignore */
      }
    }
    es.onerror = () => {
      if (es) es.close()
      es = null
      if (!closed) setTimeout(() => !closed && connect(last), 2500)
    }
  }
  connect(since)
  return () => {
    closed = true
    if (es) es.close()
  }
}

// ── 运行中子代理概览:后端按「启动事件↔完成事件」配对扫主 jsonl,返回此刻仍在跑的子代理 ──
// (注意:后台 agent 启动 3ms 即写一条 async_launched 的 tool_result,故不能用「无 result=运行中」判定;
//  权威判定在后端 /subagents,前端只消费。)
export interface RunningAgent {
  toolUseId: string
  agentType: string
  description: string
  startedAt: string // ISO 时间;前端据此本地计 elapsed
  // kind 区分种类:缺省/"agent" = 普通子代理;"workflow" = 编排运行(显示「🧩 工作流」行,钻入开 WorkflowCard 详情)。
  kind?: 'agent' | 'workflow'
  runId?: string // 仅 workflow:wf_<id>,供钻入 WorkflowCard
}
export async function fetchRunningAgents(_host: string, sid: string): Promise<RunningAgent[]> {
  try {
    const r = await xf(xb() + '/subagents?sid=' + encodeURIComponent(sid))
    if (!r.ok) return []
    const a = await r.json()
    return Array.isArray(a) ? a : []
  } catch {
    return []
  }
}

// ── Workflow(编排运行)：概览 + 单代理 transcript(增量 / SSE)──
// 概览:后端按 sid+runId 聚合 phases/agents 与汇总指标。容错失败返回 null。
export async function fetchWorkflow(_host: string, sid: string, runId: string): Promise<WorkflowDetail | null> {
  try {
    const u = xb() + '/workflow?sid=' + encodeURIComponent(sid) + '&runId=' + encodeURIComponent(runId)
    const r = await xf(u)
    if (!r.ok) return null
    return r.json()
  } catch {
    return null
  }
}

// 概览(兜底):只有 toolUseId、拿不到 runId 时用 —— 后端扫主 jsonl 反查 runId 再聚合。
// 注意:此路径下前端拿不回 runId(摘要不含),故 agent 钻入需另外从结果文本里解析 runId。
export async function fetchWorkflowByToolUse(_host: string, sid: string, toolUseId: string): Promise<WorkflowDetail | null> {
  try {
    const u = xb() + '/workflow?sid=' + encodeURIComponent(sid) + '&toolUseId=' + encodeURIComponent(toolUseId)
    const r = await xf(u)
    if (!r.ok) return null
    return r.json()
  } catch {
    return null
  }
}

// 单代理 transcript(展开某 agent 时懒加载;与 /subtranscript 同构,定位换 runId+agentId)。
export async function fetchWorkflowAgent(
  _host: string,
  sid: string,
  runId: string,
  agentId: string,
  since?: number,
): Promise<SubtranscriptResp> {
  const u =
    xb() +
    '/workflowagent?sid=' +
    encodeURIComponent(sid) +
    '&runId=' +
    encodeURIComponent(runId) +
    '&agentId=' +
    encodeURIComponent(agentId) +
    (since ? '&since=' + since : '')
  const r = await xf(u)
  if (!r.ok) throw new Error('workflowagent ' + r.status)
  return r.json()
}

// 单代理 SSE 实时跟随(运行中且已展开才连):照抄 streamSubtranscript,URL 换 /workflowagent/stream 且带 runId+agentId。
export function streamWorkflowAgent(
  _host: string,
  sid: string,
  runId: string,
  agentId: string,
  since: number,
  onItem: (cursor: number, item: unknown) => void,
): () => void {
  let es: EventSource | null = null
  let last = since
  let closed = false
  const connect = (from: number) => {
    es = new EventSource(
      xb() +
        '/workflowagent/stream?sid=' +
        encodeURIComponent(sid) +
        '&runId=' +
        encodeURIComponent(runId) +
        '&agentId=' +
        encodeURIComponent(agentId) +
        '&since=' +
        from,
    )
    es.onmessage = (ev) => {
      try {
        const m = JSON.parse(ev.data)
        if (typeof m.cursor === 'number') last = m.cursor
        if (m.item) onItem(m.cursor, m.item)
      } catch {
        /* ignore */
      }
    }
    es.onerror = () => {
      if (es) es.close()
      es = null
      if (!closed) setTimeout(() => !closed && connect(last), 2500)
    }
  }
  connect(since)
  return () => {
    closed = true
    if (es) es.close()
  }
}

// 会话列表:URL 由 config.sessionsUrl 提供(替代硬编码 /api/claude-sessions)。
// 不配置 → 返回 []。host 仅用于客户端过滤(消费方若把多 host 列在同一接口里)。
export async function fetchSessions(host: string): Promise<SessionMeta[]> {
  const cfg = getConfig()
  if (!cfg.sessionsUrl) return []
  try {
    const r = await cfg.fetch(cfg.sessionsUrl)
    if (!r.ok) return []
    const arr: SessionMeta[] = await r.json()
    // host 为空 → 不过滤(单设备消费方常如此);非空 → 仅留 hostname 匹配的。
    return host ? arr.filter((s) => s.hostname === host) : arr
  } catch {
    return []
  }
}

// ── 控件层:当前控件状态 / 发键 / 起会话 ──
export interface CtrlState {
  kind: 'offline' | 'busy' | 'select' | 'input'
  title?: string
  options?: { num: string; label: string; cur: boolean }[]
  effort?: string
  actions?: { label: string; keys?: string[]; text?: string }[]
  verb?: string // busy:里世界当前工作动词(如 Zesting),后端从抓屏解析,可空
  tokens?: string // busy:本轮 token 数(如 "1.2k"),后端解析,可空
  tip?: string // busy:里世界 Tip 行,后端解析,可空
  elapsed?: string // busy:里世界真实运行时间(如 "7s"),后端抓屏解析;抓不到时前端本地兜底
  hint?: string // input:底部提示行(如 "? for shortcuts"),可空
  suggest?: string // input:里世界「输入建议」(Prompt suggestions)整条文本,后端抓屏解析;无建议则空
}

// tmux 会话名:由 Provider 注入(替代硬编码 'cc-' + sid.slice(0,8))。
export const tmuxName = (sid: string) => getConfig().tmuxName(sid)

export async function fetchState(_host: string, tsess: string): Promise<CtrlState> {
  try {
    const r = await xf(xb() + '/state?session=' + encodeURIComponent(tsess))
    if (!r.ok) return { kind: 'offline' }
    return r.json()
  } catch {
    return { kind: 'offline' }
  }
}

export async function sendKeys(
  _host: string,
  tsess: string,
  body: { text?: string; keys?: string[]; enter?: boolean },
): Promise<boolean> {
  try {
    const r = await xf(xb() + '/sendkeys', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session: tsess, ...body }),
    })
    return r.ok
  } catch {
    return false
  }
}

// 抓当前 tmux 画面原文(读屏)。供「信息/展示类命令」跑完后取真实输出展示。
// ansi=true → 后端带 ANSI 上色(展示用,带 &ansi=1);默认无色。注意:解析/判定一律先 stripAnsi。
export async function fetchCapture(_host: string, tsess: string, lines = 100, ansi = false): Promise<string> {
  try {
    const u = xb() + '/capture?session=' + encodeURIComponent(tsess) + '&lines=' + lines + (ansi ? '&ansi=1' : '')
    const r = await xf(u)
    if (!r.ok) return ''
    const t = await r.text()
    // 兼容修复:老设备的 /capture?ansi=1 走 `tmux capture-pane -e`(缺 -p)会写进 buffer、stdout 返回空,
    // 导致所有信息卡(/cost /status /mcp …)抓屏永远拿到空串 → 解析失败 → 「未能打开」。
    // 兜底:ansi 抓屏若返回空,回退到无色抓屏(解析一律先 stripAnsi,无损;仅「原始」视图少了颜色)。
    if (ansi && t === '') {
      const r2 = await xf(xb() + '/capture?session=' + encodeURIComponent(tsess) + '&lines=' + lines)
      if (r2.ok) return r2.text()
    }
    return t
  } catch {
    return ''
  }
}

// 按需取「信息类斜杠命令」的结构化结果:后端从 since 字节起扫主 jsonl,返回首条
// type:user + isMeta:true 的干净 Markdown(实测 /context 写 isMeta)。摆脱抓屏 + ANSI 解析。
// 用法:发命令前先取当前游标作 since,提交命令,再轮询本接口直到 found。
export async function fetchCmdResult(
  _host: string,
  sid: string,
  since: number,
): Promise<{ found: boolean; markdown: string; cursor: number }> {
  try {
    const u = xb() + '/cmdresult?sid=' + encodeURIComponent(sid) + '&since=' + (since || 0)
    const r = await xf(u)
    if (!r.ok) return { found: false, markdown: '', cursor: since || 0 }
    return r.json()
  } catch {
    return { found: false, markdown: '', cursor: since || 0 }
  }
}

export const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms))

// 抓屏直到内容「可解析」(ok 返回 true)或用尽重试。
// 固定延时常抓到「半渲染」→ 解析失败 → 渲染失误;改为轮询到解析得过为止,大幅降低失误。
// ansi=true:返回带色原文供展示;但 ok 判定一律先 stripAnsi(解析逻辑零影响)。
export async function captureUntil(
  host: string,
  tsess: string,
  opts: { ok: (t: string) => boolean; lines?: number; tries?: number; gap?: number; ansi?: boolean },
): Promise<string> {
  const { ok, lines = 200, tries = 5, gap = 550, ansi = false } = opts
  let last = ''
  for (let i = 0; i < tries; i++) {
    last = (await fetchCapture(host, tsess, lines, ansi)).replace(/\s+$/, '')
    if (ok(stripAnsi(last))) return last
    if (i < tries - 1) await sleep(gap)
  }
  return last
}

// 会话信息(模型/上下文用量/累计花费):后端从 jsonl 派生,非侵入(备用,需后端 /info)。
export async function fetchInfo(_host: string, sid: string): Promise<Info | null> {
  try {
    const r = await xf(xb() + '/info?sid=' + encodeURIComponent(sid))
    if (!r.ok) return null
    return r.json()
  } catch {
    return null
  }
}

export async function startSession(_host: string, tsess: string, cwd: string, cmd: string): Promise<boolean> {
  try {
    const r = await xf(xb() + '/start', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session: tsess, cwd, cmd }),
    })
    return r.ok
  } catch {
    return false
  }
}

// 「打开终端」:由 Provider 注入的 terminalUrl 生成(替代硬编码 ttyd /d 直链;attach cc-<id8> 并 --resume)。
export function terminalUrl(_host: string, sid: string, cwd?: string): string {
  return getConfig().terminalUrl(sid, cwd)
}
