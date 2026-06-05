import { Pill } from './shared'
import type { CardModule } from './types'

// /mcp 卡:把 Claude Code「Manage MCP servers」面板抓屏解析成结构化的服务器清单,重渲成原生控件。
// 抓屏每行带前导空格,夹杂顶部标题/计数、help url、底部「↑/↓ to navigate · …」等噪声,
// 行首可能有光标符「❯」或滚动符「↓」「↑」,要先剥掉。parser 只盯组标题行(「… MCPs …」)、
// 含「 · 」的服务器行、以及「→ Show unused connectors (N)」一行;缺关键标记(无服务器)就返回 null。

export interface McpServer {
  name: string
  status: string
  tools: string
  group: string
}

export interface Mcp {
  count: string
  servers: McpServer[]
  unused: number
}

// 剥掉行首光标/滚动符与首尾空白:抓屏每行带缩进,选中行还会冠上「❯」,滚动到边界会冠上「↓」「↑」。
const strip = (raw: string) => raw.replace(/^\s*[❯↓↑]?\s*/, '').replace(/\s+$/, '')

// 组标题精简:「User MCPs (/Users/...)」「Built-in MCPs (always available)」→ 去掉括号说明,留「User MCPs」。
const groupName = (line: string) => line.replace(/\s*\(.*\)\s*$/, '').trim()

export function parseMcp(text: string): Mcp | null {
  let count = ''
  let group = ''
  let unused = 0
  const servers: McpServer[] = []

  for (const raw of text.split('\n')) {
    const line = strip(raw)
    if (!line) continue

    // 跳过顶部标题。
    if (/^Manage MCP servers$/i.test(line)) continue

    // 「N servers」计数行(也作防御标记之一)。
    const mc = line.match(/^(\d+)\s+servers?$/i)
    if (mc) {
      count = mc[1]
      continue
    }

    // 「→ Show unused connectors (N)」:取 N,不计入 servers。
    const mu = line.match(/Show unused connectors\s*\((\d+)\)/i)
    if (mu) {
      unused = Number(mu[1])
      continue
    }

    // 跳过 help url 行与底部导航行。
    if (/^https?:\/\//i.test(line)) continue
    if (/\b(?:to navigate|to confirm|to cancel|to select|to toggle)\b/i.test(line)) continue

    // 组标题:含「… MCPs …」的行(如 User MCPs (...)、Built-in MCPs (always available))。
    if (/\bMCPs\b/.test(line)) {
      group = groupName(line)
      continue
    }

    // 服务器行:含「 · 」分隔。拆成 [name, statusPart?, toolsPart?]。
    if (line.includes(' · ')) {
      const parts = line.split(' · ').map((p) => p.trim())
      const name = parts[0]
      if (!name) continue
      let status = ''
      let tools = ''
      for (const p of parts.slice(1)) {
        if (/\btools?\b/i.test(p)) tools = p
        else if (!status) status = p
      }
      servers.push({ name, status, tools, group })
      continue
    }

    // 无 status 的裸服务器名(如「claude.ai」,视为待授权)。必须在已识别到某个组之后,
    // 且排除明显的提示性句子(含空格的多词噪声)。允许域名/标识符样式的单 token。
    if (group && /^[\w.@:/+-]+$/.test(line)) {
      servers.push({ name: line, status: '', tools: '', group })
    }
  }

  // 防御:没解析到任何服务器,大概率不是 /mcp 面板。
  if (servers.length === 0) return null

  return { count, servers, unused }
}

// 状态片段 → 中文药丸。connected→已连接(on)、disabled→禁用(off)、其余/空→待授权(warn)。
function statusPill(status: string) {
  if (/\bconnected\b/i.test(status)) return <Pill tone="on">已连接</Pill>
  if (/\bdisabled\b/i.test(status)) return <Pill tone="off">禁用</Pill>
  return <Pill tone="warn">待授权</Pill>
}

export function McpCard({ data }: { data: Mcp }) {
  // 按 group 保序分组(沿用首次出现顺序),组内保留服务器原顺序。
  const order: string[] = []
  const byGroup: Record<string, McpServer[]> = {}
  for (const s of data.servers) {
    const g = s.group || '其他'
    if (!byGroup[g]) {
      byGroup[g] = []
      order.push(g)
    }
    byGroup[g].push(s)
  }

  return (
    <div className="info">
      <div className="grp">{data.count || data.servers.length} servers</div>

      {order.length === 0 ? (
        <div className="empty">没有 MCP 服务器</div>
      ) : (
        order.map((g) => (
          <div key={g}>
            <div className="grp">{g}</div>
            <div className="set-list">
              {byGroup[g].map((s, i) => (
                <div className="set-row" key={i}>
                  <span className="set-k">
                    <span className="set-kn">{s.name}</span>
                  </span>
                  <span className="pills">
                    {statusPill(s.status)}
                    {s.tools && <Pill>{s.tools}</Pill>}
                  </span>
                </div>
              ))}
            </div>
          </div>
        ))
      )}

      {data.unused > 0 && <div className="note">另有 {data.unused} 个未使用的连接器</div>}
    </div>
  )
}

export const mcp: CardModule<Mcp> = { parse: parseMcp, Card: McpCard }
