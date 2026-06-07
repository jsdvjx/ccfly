// 批次5 · MCP 资源卡(mr-)· 补齐两个一方 MCP「资源」工具:此前落到 StructuredResultCard / GenericCard。
//   · ListMcpResourcesTool —— 列出各 MCP server 下可用资源:result 是资源对象数组(标准 MCP 字段 name/uri/mimeType/description + server)。
//   · ReadMcpResourceTool  —— 读取单个资源正文:入参 {server, uri},result 是资源体(text / JSON)。
// 这两个名字「无 mcp__ 前缀」,故不会被 McpCard 截走;由 router 精确命中本卡。
// 设计:list 视图按 server 分组(🔌 表头 + 计数药丸),组内逐资源行(name + uri 等宽药丸 + 可选描述 + mimeType 药丸);
//       read 视图给 server·uri 头,正文交给 ResultPane(markdown/json 走 md,其余 mono)。
// 与 StructuredResultCard 同款自包含模式:result 缺失或解析失败时就地回落 <GenericCard/>,故对非匹配场景 router 行为不变。
// 复用基座:BlockShell(icon='🔌' accent='mcp')、ResultPane、useToolStatus;主体复用 .pill / .kv 族,仅追加极薄 .mr- 钩子。
import { useState, type ReactNode } from 'react'
import { BlockShell, ResultPane, useToolStatus } from './shell'
import { GenericCard } from './MetaTools'
import type { Block } from '../types'

// ── 入参取值小工具(与同族卡复用同一风格)──
function asInput(block: Block): Record<string, unknown> {
  return (block.input || {}) as Record<string, unknown>
}
function str(input: Record<string, unknown>, k: string): string {
  const v = input[k]
  return typeof v === 'string' ? v : ''
}

// ── 安全 JSON 解析:仅当整串是一个 JSON 值时返回,否则 null(镜像 StructuredResultCard.tryParseJson)。 ──
// 先做廉价首字符判别({ 或 [),避免对长文本做无谓 parse。
function tryParseJson(text: string): unknown {
  const s = (text || '').trim()
  if (!s) return null
  const c = s[0]
  if (c !== '{' && c !== '[') return null
  try {
    return JSON.parse(s)
  } catch {
    return null
  }
}

// ── 卡内小节:带标签的二级折叠段(与 MetaTools/MiscTools/StructuredResultCard 同款,自包含以免跨文件耦合私有件)。 ──
function Section({ label, defaultOpen, children }: { label: string; defaultOpen: boolean; children: ReactNode }) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <div className="mr-sec">
      <button className="mr-sec-h" onClick={() => setOpen((o) => !o)}>
        <span className="mr-sec-chev">{open ? '▾' : '▸'}</span>
        {label}
      </button>
      {open && <div className="mr-sec-body">{children}</div>}
    </div>
  )
}

// ════════════════════════ 资源解析(list)════════════════════════
// 标准 MCP 资源字段(均可选,尽力而为;缺字段不报错,只少渲对应行)。
interface McpResource {
  uri: string
  name: string
  mimeType?: string
  description?: string
  server?: string
}

// 取对象上的字符串字段(容错:非字符串视为缺省)。
function pick(rec: Record<string, unknown>, k: string): string {
  const v = rec[k]
  return typeof v === 'string' ? v.trim() : ''
}

// 单个候选 → 资源(要求至少有 uri 或 name;否则视为不可识别)。
function asResource(v: unknown): McpResource | null {
  if (v == null || typeof v !== 'object' || Array.isArray(v)) return null
  const rec = v as Record<string, unknown>
  const uri = pick(rec, 'uri')
  const name = pick(rec, 'name') || pick(rec, 'title')
  if (!uri && !name) return null
  return {
    uri,
    name: name || uri, // 无 name 退化用 uri 当显示名
    mimeType: pick(rec, 'mimeType') || pick(rec, 'mime_type') || undefined,
    description: pick(rec, 'description') || undefined,
    server: pick(rec, 'server') || pick(rec, 'serverName') || undefined,
  }
}

// result 解析为资源数组。兼容两种形态:
//   ① 直接是数组 [{…}, …];
//   ② 包裹对象 { resources: [...] }(部分实现的封装)。
// 任一元素解析失败即整体放弃(返回 null → 回落 GenericCard),不臆造半截列表。
function parseResources(text: string | undefined): McpResource[] | null {
  if (!text) return null
  const parsed = tryParseJson(text)
  if (parsed == null) return null
  let arr: unknown[] | null = null
  if (Array.isArray(parsed)) {
    arr = parsed
  } else if (typeof parsed === 'object') {
    const rec = parsed as Record<string, unknown>
    if (Array.isArray(rec.resources)) arr = rec.resources as unknown[]
    else if (Array.isArray(rec.contents)) arr = rec.contents as unknown[]
  }
  if (!arr || arr.length === 0 || arr.length > 500) return null
  const out: McpResource[] = []
  for (const el of arr) {
    const r = asResource(el)
    if (!r) return null
    out.push(r)
  }
  return out
}

// 按 server 分组(保留首次出现顺序;无 server 字段归入「(未标注)」桶,但若入参带 server 则以之为名)。
interface ServerGroup {
  server: string
  items: McpResource[]
}
function groupByServer(resources: McpResource[], fallbackServer: string): ServerGroup[] {
  const order: string[] = []
  const map = new Map<string, McpResource[]>()
  for (const r of resources) {
    const key = r.server || fallbackServer || '(未标注)'
    if (!map.has(key)) {
      map.set(key, [])
      order.push(key)
    }
    map.get(key)!.push(r)
  }
  return order.map((server) => ({ server, items: map.get(server)! }))
}

// ════════════════════════ 子视图 ════════════════════════

// 单条资源行:name(主)+ uri 等宽药丸(wrap-safe)+ 可选描述 + mimeType 药丸。
function ResourceRow({ r }: { r: McpResource }) {
  return (
    <div className="mr-res">
      <div className="mr-res-top">
        <span className="mr-res-name">{r.name}</span>
        {r.mimeType && <span className="pill mr-mime">{r.mimeType}</span>}
      </div>
      {r.uri && r.uri !== r.name && <span className="mr-uri">{r.uri}</span>}
      {r.description && <div className="mr-res-desc">{r.description}</div>}
    </div>
  )
}

// server 分组:🔌 表头 + 计数药丸,组内逐资源行。
function ServerSection({ group }: { group: ServerGroup }) {
  return (
    <div className="mr-grp">
      <div className="mr-grp-h">
        <span className="mr-grp-i" aria-hidden="true">
          🔌
        </span>
        <span className="mr-grp-srv">{group.server}</span>
        <span className="pill mr-grp-cnt">{group.items.length}</span>
      </div>
      <div className="mr-grp-body">
        {group.items.map((r, i) => (
          <ResourceRow key={i} r={r} />
        ))}
      </div>
    </div>
  )
}

// ── Read 正文变体判定:JSON / 看似 markdown → md;其余 → mono(原样终端 pre)。 ──
// 仅做廉价启发,绝不臆造;判定不出一律 mono(最保守)。
function readVariant(content: string, mimeType: string): 'md' | 'mono' {
  const mt = (mimeType || '').toLowerCase()
  if (mt.includes('markdown')) return 'md'
  if (mt.includes('json')) return 'md' // MD 组件能把 ```json 代码块渲得更稳;此处先经 prettyJson 包裹
  const s = (content || '').trim()
  if (!s) return 'mono'
  // 看似整串 JSON → md(交给下游 prettyJson)。
  if ((s[0] === '{' || s[0] === '[') && tryParseJson(s) != null) return 'md'
  // 看似 markdown:含标题/列表/代码围栏等常见标记。
  if (/^#{1,6}\s|\n#{1,6}\s|```|\n[-*]\s|\n\d+\.\s|\[[^\]]+\]\([^)]+\)/.test(s)) return 'md'
  return 'mono'
}

// JSON 正文美化:若整串是 JSON 则两空格缩进重排并包成 ```json 代码块,供 MD 高亮;否则原样返回。
function prettyForMd(content: string): string {
  const s = (content || '').trim()
  if (s[0] !== '{' && s[0] !== '[') return content
  const parsed = tryParseJson(s)
  if (parsed == null) return content
  try {
    return '```json\n' + JSON.stringify(parsed, null, 2) + '\n```'
  } catch {
    return content
  }
}

// ════════════════════════ 主组件 ════════════════════════
// 与 McpCard 同壳观感:icon='🔌' accent='mcp';按 block.name 分 list / read 两支。
// 关键:result 只能在组件内用 useToolStatus 取到 —— 故由卡自身判定是否可富渲染,
//   不可解析(result 缺失/非资源 JSON)则就地回落 <GenericCard/>(与 router 兜底一致,router 行为不变)。
export interface McpResourceCardProps {
  block: Block
}

// 工具名归一:去掉可能的 Tool 后缀,便于 list / read 分支判断(router 已注册带/不带后缀两套别名)。
function kindOf(name: string): 'list' | 'read' | 'unknown' {
  const n = (name || '').replace(/Tool$/, '')
  if (n === 'ListMcpResources') return 'list'
  if (n === 'ReadMcpResource') return 'read'
  return 'unknown'
}

export function McpResourceCard({ block }: McpResourceCardProps) {
  const input = asInput(block)
  const { status, res } = useToolStatus(block)
  const kind = kindOf(block.name || '')

  // ── Read 分支:server·uri 头 + 正文(md/mono)。 ──
  if (kind === 'read') {
    const server = str(input, 'server')
    const uri = str(input, 'uri')
    // result 缺失 → 与 router 兜底等价:交给 GenericCard(其会显示 running/入参/结果)。
    if (!res || !res.content) return <GenericCard block={block} />

    const title = (
      <span className="mr-name">
        {server && <span className="mr-srv">{server}</span>}
        {server && uri && <span className="mr-sep">·</span>}
        <span className="mr-tool">{uri || '资源'}</span>
      </span>
    )
    const variant = readVariant(res.content, '')
    const body = variant === 'md' ? prettyForMd(res.content) : res.content

    return (
      <BlockShell
        icon="🔌"
        title={title}
        brief="读取资源"
        accent="mcp"
        status={status}
        defaultOpen={true}
        headerExtra={<span className="pill mr-tag">MCP</span>}
      >
        <ResultPane content={body} isError={res.isError} variant={variant} />
      </BlockShell>
    )
  }

  // ── List 分支:按 server 分组渲染资源清单。 ──
  if (kind === 'list') {
    const resources = parseResources(res?.content)
    // 解析不出资源(result 缺失/非资源 JSON)→ 回落 GenericCard。
    if (!resources) return <GenericCard block={block} />

    const fallbackServer = str(input, 'server')
    const groups = groupByServer(resources, fallbackServer)
    const total = resources.length
    const brief = (
      <span className="mr-list-brief">
        <span className="pill mr-total">{total} 资源</span>
        {groups.length > 1 && <span className="mr-srv-cnt">· {groups.length} server</span>}
      </span>
    )

    return (
      <BlockShell
        icon="🔌"
        title="MCP 资源"
        brief={brief}
        accent="mcp"
        status={status}
        defaultOpen={true}
        headerExtra={<span className="pill mr-tag">MCP</span>}
      >
        <div className="mr-list">
          {groups.map((g, i) => (
            <ServerSection key={i} group={g} />
          ))}

          {/* 原始结果:富渲染后默认收起,供需要时核对完整 JSON。 */}
          {res && res.content && (
            <Section label="原始结果" defaultOpen={false}>
              <ResultPane content={res.content} isError={res.isError} variant="mono" />
            </Section>
          )}
        </div>
      </BlockShell>
    )
  }

  // 未知名(理论上 router 不会路由到此)→ 与兜底一致。
  return <GenericCard block={block} />
}

export default McpResourceCard
