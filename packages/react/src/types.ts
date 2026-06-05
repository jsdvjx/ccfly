// 与后端控制服务 /transcript 的结构化输出对齐(agent/transcript.go: tItem/tBlock)。

export type BlockType = 'text' | 'thinking' | 'tool_use' | 'tool_result' | 'image'

// Edit/MultiEdit 的 structuredPatch 单个 hunk(与 TUI 同源,含上下文行)。
// lines 每行带前导符号:' ' 上下文 / '-' 删除 / '+' 新增。
export interface PatchHunk {
  oldStart: number
  oldLines: number
  newStart: number
  newLines: number
  lines: string[]
}

export interface Block {
  type: BlockType
  text?: string // text / thinking
  id?: string // tool_use id
  name?: string // tool_use 工具名
  input?: Record<string, unknown> // tool_use 入参
  forId?: string // tool_result -> tool_use_id
  content?: string // tool_result 文本
  isError?: boolean
  // Edit/MultiEdit 的 tool_result 块:后端透传的 structuredPatch hunk 数组(含上下文行,渲染带上下文 diff)。
  patch?: PatchHunk[]
  // image 块:不内联 base64,前端用 item.uuid + imgIdx 回 /image 取字节。
  path?: string // 路径式附图的真实文件路径(取 basename 作文件名)
  mediaType?: string // base64 式的媒体类型(image/png 等)
  imgIdx?: number // 该消息内第 N 个图片(两类合计,从 0 起)
}

export interface Item {
  role: 'user' | 'assistant'
  kind: string
  text: string
  ts: string
  uuid?: string // 该 jsonl 行 uuid(图片块按 uuid+imgIdx 回 /image 取字节)
  model?: string
  outTokens?: number // assistant 行输出 token(message.usage.output_tokens),供「轮注脚」累加
  blocks?: Block[]
}

export interface TranscriptResp {
  cursor: number
  items: Item[]
  hasMore: boolean
  // 本批最旧 item 所在行的起始字节;向上分页用 before=firstCursor 无缝接续(不重不漏)。
  firstCursor?: number
}

// 子代理 transcript 的 meta(对齐后端 agent-<agentId>.meta.json:agentType/description/toolUseId)。
export interface SubMeta {
  agentType?: string
  description?: string
  toolUseId?: string
}

// 子代理 transcript 响应:与 TranscriptResp 同构,外加子代理 meta(展开懒加载时取)。
export interface SubtranscriptResp {
  meta?: SubMeta
  cursor: number
  items: Item[]
  hasMore: boolean
}

// ── Workflow(编排运行)概览 ──
// 单个子代理(workflow 内一个 agent),对齐后端 /workflow 的 agents[]。字段保守可选。
export interface WorkflowAgent {
  agentId?: string
  label?: string
  phaseIndex?: number
  phaseTitle?: string
  model?: string
  state?: string
  tokens?: number
  toolCalls?: number
  durationMs?: number
  lastToolSummary?: string
  startedAt?: number // 后端 int64(epoch ms),非 ISO 串
}

// workflow 运行详情,对齐后端 GET /workflow?sid=&runId=。
export interface WorkflowDetail {
  name?: string
  summary?: string
  status?: string
  durationMs?: number
  startTime?: number // 后端 int64(epoch ms)
  defaultModel?: string
  totalTokens?: number
  totalToolCalls?: number
  phases?: { index: number; title: string }[]
  agents?: WorkflowAgent[]
}

export interface SessionMeta {
  hostname: string
  session_id: string
  title?: string
  state?: string
  turns?: number
  tokens?: number
  model?: string
  cwd?: string
  last_ts?: string
}

// 会话信息(/info):上下文用量 + 累计 token 花费 + 元信息,统一展示。对齐 agent infoResp。
export interface Info {
  model: string
  ctxTokens: number
  ctxLimit: number
  inTokens: number
  outTokens: number
  cacheTokens: number
  turns: number
  msgCount: number
  cwd: string
  gitBranch?: string
  lastTs: string
}
