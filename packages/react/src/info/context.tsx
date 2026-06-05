import { useState } from 'react'
import { MD, shortModel } from '../components'
import { lvl } from './shared'
import type { CardModule } from './types'

// /context 卡:上下文用量(模型 + 总量 + 分类占比 + MCP/Memory/Skills 明细)。
// 数据有两条来源,统一收敛到同一个 Ctx 结构、同一张漂亮卡:
//   1) 干净 markdown(jsonl 的 isMeta 消息,首选)→ parseContextMd
//   2) 抓屏剥色文本(jsonl 超时兜底)→ parseContext
// /context 打印进对话、不停留(非 modal),抓完无需 Esc。

interface Cat { name: string; tokens: string; pct: number }
interface Row3 { a: string; b: string; tokens: string } // MCP/Memory/Skills 表:三列(名/源/tokens)
export interface Ctx {
  model: string
  used: string
  limit: string
  pct: number
  cats: Cat[]
  mcp?: Row3[]
  memory?: Row3[]
  skills?: Row3[]
}

// ── markdown 解析(首选路径)──
// 防御式:正则抓 **Model:** / **Tokens:** A / B (C%);抓「### Estimated usage by category」表格;
// MCP/Memory/Skills 三张表可选解析(任一缺失就不渲对应段)。
export function parseContextMd(md: string): Ctx | null {
  if (!md) return null

  // Model:**Model:** claude-opus-4-8[1m]
  const mm = md.match(/\*\*Model:\*\*\s*([^\n*]+?)\s*(?:\n|$)/)
  const model = mm ? mm[1].trim() : ''

  // Tokens:**Tokens:** 311.9k / 1m (31%)
  const mt = md.match(/\*\*Tokens:\*\*\s*([\d.]+[kKmM]?)\s*\/\s*([\d.]+[kKmM]?)\s*\((\d+)%\)/)
  if (!mt) return null // 没有总量行 → 不是 /context,交回退

  const used = mt[1]
  const limit = mt[2]
  const pct = parseInt(mt[3], 10)

  // 「### Estimated usage by category」下的表格:| Category | Tokens | Percentage |
  const cats: Cat[] = []
  const catBlock = sliceSection(md, /###\s*Estimated usage by category/i)
  for (const cells of tableRows(catBlock)) {
    if (cells.length < 3) continue
    const name = cells[0]
    const tokens = cells[1]
    const p = parseFloat(cells[2].replace('%', ''))
    if (!name || isNaN(p)) continue
    cats.push({ name, tokens, pct: p })
  }
  if (cats.length === 0) return null // 没解析到分类 → 信息不足

  // 可选三表(三列):MCP Tools / Memory Files / Skills。表头第一行是字段名,跳过。
  const mcp = parse3(md, /###\s*MCP Tools/i)
  const memory = parse3(md, /###\s*Memory Files/i)
  const skills = parse3(md, /###\s*Skills/i)

  return { model, used, limit, pct, cats, mcp, memory, skills }
}

// 取某 ### 段标题之后、到下一个 ### / ## / 文末之间的子串。
function sliceSection(md: string, head: RegExp): string {
  const m = md.match(head)
  if (!m || m.index == null) return ''
  const start = m.index + m[0].length
  const rest = md.slice(start)
  const next = rest.search(/\n#{2,3}\s/)
  return next === -1 ? rest : rest.slice(0, next)
}

// 从一段 markdown 里抽出表格数据行(跳过表头行与 |---| 分隔行),每行返回 cell 数组。
function* tableRows(block: string): Generator<string[]> {
  let headerSkipped = false
  for (const raw of block.split('\n')) {
    const line = raw.trim()
    if (!line.startsWith('|')) continue
    if (/^\|[\s:|-]+\|?$/.test(line)) { headerSkipped = true; continue } // |---|---| 分隔行 → 此前那行是表头
    const cells = line.split('|').slice(1, -1).map((c) => c.trim())
    if (!headerSkipped) continue // 分隔行之前的(表头)不要
    yield cells
  }
}

// 解析三列可选表(名/源/tokens)。无该段或无数据行 → undefined。
function parse3(md: string, head: RegExp): Row3[] | undefined {
  const block = sliceSection(md, head)
  if (!block) return undefined
  const rows: Row3[] = []
  for (const cells of tableRows(block)) {
    if (cells.length < 2) continue
    const a = cells[0]
    const b = cells.length >= 3 ? cells[1] : ''
    const tokens = cells[cells.length - 1]
    if (!a) continue
    rows.push({ a, b, tokens })
  }
  return rows.length ? rows : undefined
}

// ── 抓屏解析(兜底路径)──
// /context 是非模态命令:抓屏含「整屏会话正文 + 底部 /context 输出」。
// 必须把解析锚定在 /context 输出块内,否则会命中正文里的「Opus/Claude…」。
export function parseContext(text: string): Ctx | null {
  const lines = text.split('\n')
  // 1) 定位用量行(要求带 "tokens" 字样,正文不会有)→ /context 块的锚点。
  const usageRe = /([\d.]+[kKmM]?)\s*\/\s*([\d.]+[kKmM]?)\s*tokens\s*\((\d+)%\)/
  let ui = -1
  let usage: RegExpMatchArray | null = null
  for (let i = lines.length - 1; i >= 0; i--) {
    const m = lines[i].match(usageRe)
    if (m) { ui = i; usage = m; break }
  }
  if (!usage || ui < 0) return null
  // 2) 真模型行在用量行上方紧邻几行(⛁ 仪表块右侧),只在该窗口里找,避开远处正文。
  const modelRe = /\b(?:Opus|Sonnet|Haiku)\b[^\n│]*/i
  let model = ''
  for (let i = ui - 1; i >= Math.max(0, ui - 6); i--) {
    const m = lines[i].match(modelRe)
    if (m) { model = stripGauge(m[0]) }
  }
  // 3) 分类行:同样收敛到 /context 块内(从块头到用量行下方的分类区)。
  const cats: Cat[] = []
  for (let i = ui + 1; i < lines.length; i++) {
    const c = lines[i].match(/([A-Za-z][A-Za-z ]+?):\s*([\d.]+[kKmM]?)\b[^\n]*?\(([\d.]+)%\)/)
    if (c) cats.push({ name: c[1].trim(), tokens: c[2], pct: parseFloat(c[3]) })
  }
  return { model, used: usage[1], limit: usage[2], pct: parseInt(usage[3], 10), cats }
}

// 剥掉模型名可能残留的行首仪表块/多余空白,得到干净模型名(如 "Opus 4.8 (1M context)")。
function stripGauge(s: string): string {
  return s.replace(/[⛀⛁⛶\s]+/g, ' ').trim().replace(/^[⛀⛁⛶ ]+/, '').trim()
}

// ── 分类配色 ── 按 TUI 语义给每个类别一个稳定色点(匹配名称关键字)。
// Free space 例外:它是「剩余」不是「用量」,迷你条走 .cat-fill.free 弱化灰。
function catColor(name: string): string {
  const n = name.toLowerCase()
  if (/free/.test(n)) return '#3a4150'
  if (/system prompt/.test(n)) return '#ffcf6b' // amber
  if (/mcp/.test(n)) return '#5ad1c5' // teal
  if (/system tools/.test(n)) return '#4f9cf9' // blue
  if (/memory/.test(n)) return '#3ddc84' // green
  if (/skill/.test(n)) return '#c792ea' // purple
  if (/message/.test(n)) return '#f78c6c' // orange
  if (/tool/.test(n)) return '#82aaff' // light blue
  return '#8b93a1' // 其他 → 中性
}

// ── 渲染 ── 顶部模型+总量大表、按类别迷你条、MCP/Memory/Skills 可折叠段、底部原始切换由上层 Head 管。
export function ContextCard({ data }: { data: Ctx }) {
  return (
    <div className="info">
      {data.model && <div className="info-model">{shortModel(data.model) || data.model}</div>}

      <div className="gauge">
        <div className="gauge-top">
          <span>上下文</span>
          <span>{data.used} / {data.limit} · {data.pct}%</span>
        </div>
        <div className="gauge-bar">
          <div className={'gauge-fill ' + lvl(data.pct)} style={{ width: Math.min(100, data.pct) + '%' }} />
        </div>
      </div>

      <div>
        <div className="grp">按类别估算</div>
        <div className="cats">
          {data.cats.map((c, i) => {
            const free = /free/i.test(c.name)
            return (
              <div className="cat" key={i}>
                <div className="cat-top">
                  <span className="cat-n">
                    <i className="ctx-dot" style={{ background: catColor(c.name) }} />
                    {c.name}
                  </span>
                  <span className="cat-v">{c.tokens} · {c.pct}%</span>
                </div>
                <div className="cat-bar">
                  <div
                    className={'cat-fill' + (free ? ' free' : '')}
                    style={free ? { width: Math.min(100, c.pct) + '%' } : { width: Math.min(100, c.pct) + '%', background: catColor(c.name) }}
                  />
                </div>
              </div>
            )
          })}
        </div>
      </div>

      <Detail title="MCP Tools" rows={data.mcp} />
      <Detail title="Memory Files" rows={data.memory} />
      <Detail title="Skills" rows={data.skills} />
    </div>
  )
}

// 可折叠明细段:默认折叠;点标题展开成 .kv 简表(左=名+源、右=tokens)。无数据不渲。
function Detail({ title, rows }: { title: string; rows?: Row3[] }) {
  const [open, setOpen] = useState(false)
  if (!rows || rows.length === 0) return null
  return (
    <div className="grp ctx-grp">
      <button className="ctx-toggle" onClick={() => setOpen(!open)}>
        <span className="ctx-caret">{open ? '▾' : '▸'}</span>
        <span>{title}</span>
        <span className="ctx-count">{rows.length}</span>
      </button>
      {open && (
        <div className="kv ctx-kv">
          {rows.map((r, i) => (
            <div className="kv-row" key={i}>
              <span className="kv-k ctx-k">
                <span className="ctx-name">{r.a}</span>
                {r.b && <span className="ctx-src">{r.b}</span>}
              </span>
              <span className="kv-v">{r.tokens}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// 既给抓屏路径(CardModule.Card,吃剥色文本 parse 出的 Ctx),也给 jsonl 路径(InfoSheet 直接调 ContextCard)。
export const context: CardModule<Ctx> = { parse: parseContext, Card: ContextCard }

// 给 InfoSheet 的 md 路径用:markdown → 结构化卡;解析失败回退 <MD>。
export function ContextMd({ md }: { md: string }) {
  const data = parseContextMd(md)
  return data ? <ContextCard data={data} /> : <MD text={md} />
}
