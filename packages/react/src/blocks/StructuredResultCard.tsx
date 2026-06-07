// 批次3 · 结构化结果卡(src-)· GenericCard 之前的「更聪明的兜底」。
// 纯展示、零 tmux 交互:对未被精确路由命中的 tool_use,客户端启发式检查 input/result —
//   ① result/input 解析为「扁平对象数组」 → 紧凑键值表格(复用 .kv 观感 + 真表格语义);
//   ② result/input 是带 status/state/ok/success 字段的对象 → 状态药丸(.pill on/warn)+ kv 行;
//   ③ result/input 是「字符串列表」 → 列表(复用搜索族 li 视觉)。
// 命中其一才富渲染;否则回落到 GenericCard 同款裸 JSON 展示(见 tryStructuredResult 说明)。
// 解析全部容错(try/catch + 类型守卫),失败一律退回兜底,绝不抛错挡渲染。
// 复用基座:BlockShell(icon='🧩' accent='unknown')、ResultPane、CodeCanvas、useToolStatus。
// CSS 前缀 src-;主体复用 .kv/.kv-row/.kv-k/.kv-v 与 .pill,仅追加极薄的 .src- 钩子。
import { useState, type ReactNode } from 'react'
import { BlockShell, ResultPane, CodeCanvas, useToolStatus } from './shell'
import { briefOf } from './meta'
import { GenericCard } from './MetaTools'
import type { Block } from '../types'

// ── 入参取值小工具 ──
function asInput(block: Block): Record<string, unknown> {
  return (block.input || {}) as Record<string, unknown>
}

// ── 标量判定:可安全渲成单元格文本的值(string/number/boolean/null)。 ──
function isScalar(v: unknown): v is string | number | boolean | null {
  return v == null || typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean'
}

// 标量 → 展示文本(null/undefined → 空串;其余字面量化)。
function scalarText(v: unknown): string {
  if (v == null) return ''
  if (typeof v === 'string') return v
  if (typeof v === 'boolean') return v ? 'true' : 'false'
  return String(v)
}

// ── 安全 JSON 解析:仅当整串是一个 JSON 值时返回,否则 null(不吞普通日志文本)。 ──
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

// ════════════════════════ 分类候选(三态)════════════════════════
// 三种识别结果:扁平对象数组(表) / 状态对象 / 字符串列表;均为「足够自信才返回」。

// ① 扁平对象数组:形如 [{a,b}, {a,c}] —— 每个元素是对象、字段值皆标量。
interface TableShape {
  kind: 'table'
  cols: string[]
  rows: Array<Record<string, string>>
}
// ② 状态对象:含 status/state/ok/success 任一,渲药丸 + 其余标量字段 kv。
interface StatusShape {
  kind: 'status'
  label: string // 药丸文字
  good: boolean | null // true=on(绿)/false=warn(琥珀)/null=中性
  rows: Array<{ k: string; v: string }>
}
// ③ 字符串列表:形如 ["a","b"] —— 元素全是非空字符串。
interface ListShape {
  kind: 'list'
  items: string[]
}
type Shape = TableShape | StatusShape | ListShape

// 单个值 → 形态(命中之一返回,否则 null)。供 result 与 input 复用。
function classifyValue(v: unknown): Shape | null {
  return asTable(v) || asStatus(v) || asStringList(v)
}

// 扁平对象数组 → 表。要求:非空数组、元素均为「字段值全标量」的对象、
// 列数 1..8(超宽放弃,避免移动端横向溢出)、≤ 50 行(过长由裸 JSON 兜底更稳)。
function asTable(v: unknown): TableShape | null {
  if (!Array.isArray(v) || v.length === 0 || v.length > 50) return null
  const objs: Array<Record<string, unknown>> = []
  for (const el of v) {
    if (el == null || typeof el !== 'object' || Array.isArray(el)) return null
    const rec = el as Record<string, unknown>
    // 任一字段非标量(嵌套对象/数组)→ 放弃表格化。
    for (const k of Object.keys(rec)) {
      if (!isScalar(rec[k])) return null
    }
    objs.push(rec)
  }
  // 列:按首次出现顺序并集,去重。
  const cols: string[] = []
  const seen = new Set<string>()
  for (const o of objs) {
    for (const k of Object.keys(o)) {
      if (!seen.has(k)) {
        seen.add(k)
        cols.push(k)
      }
    }
  }
  if (cols.length === 0 || cols.length > 8) return null
  const rows = objs.map((o) => {
    const r: Record<string, string> = {}
    for (const c of cols) r[c] = scalarText(o[c])
    return r
  })
  return { kind: 'table', cols, rows }
}

// 状态对象 → 药丸。要求:对象(非数组)且含 status|state|ok|success 任一关键字段。
// good 判定:ok/success 取真值;status/state 文本按词典映射(ok/success/done… → 绿;
// error/fail/denied… → 琥珀;其余中性 null)。其余标量字段做 kv 行。
const GOOD_WORDS = ['ok', 'success', 'succeeded', 'done', 'complete', 'completed', 'pass', 'passed', 'active', 'ready', 'healthy', 'enabled', 'true', 'on']
const BAD_WORDS = ['error', 'err', 'fail', 'failed', 'failure', 'denied', 'deny', 'reject', 'rejected', 'blocked', 'unhealthy', 'disabled', 'false', 'off', 'timeout', 'cancelled', 'canceled']
function statusVerdict(word: string): boolean | null {
  const w = word.trim().toLowerCase()
  if (!w) return null
  if (GOOD_WORDS.includes(w)) return true
  if (BAD_WORDS.includes(w)) return false
  return null
}
function asStatus(v: unknown): StatusShape | null {
  if (v == null || typeof v !== 'object' || Array.isArray(v)) return null
  const rec = v as Record<string, unknown>
  const keys = Object.keys(rec)
  if (keys.length === 0) return null
  // 关键字段优先级:status > state > ok > success。
  let label = ''
  let good: boolean | null = null
  let hitKey = ''
  if (typeof rec.status === 'string' || typeof rec.status === 'number') {
    label = scalarText(rec.status)
    good = statusVerdict(label)
    hitKey = 'status'
  } else if (typeof rec.state === 'string' || typeof rec.state === 'number') {
    label = scalarText(rec.state)
    good = statusVerdict(label)
    hitKey = 'state'
  } else if (typeof rec.ok === 'boolean') {
    good = rec.ok
    label = rec.ok ? 'ok' : 'not ok'
    hitKey = 'ok'
  } else if (typeof rec.success === 'boolean') {
    good = rec.success
    label = rec.success ? 'success' : 'failed'
    hitKey = 'success'
  } else {
    return null
  }
  // 其余标量字段 → kv 行(跳过命中字段;嵌套/数组字段忽略,避免噪声)。
  const rows: Array<{ k: string; v: string }> = []
  for (const k of keys) {
    if (k === hitKey) continue
    const val = rec[k]
    if (!isScalar(val)) continue
    const text = scalarText(val)
    if (text === '') continue
    rows.push({ k, v: text })
  }
  return { kind: 'status', label, good, rows }
}

// 字符串列表 → 列表。要求:非空数组、元素全为非空字符串、≤ 100 项。
function asStringList(v: unknown): ListShape | null {
  if (!Array.isArray(v) || v.length === 0 || v.length > 100) return null
  const items: string[] = []
  for (const el of v) {
    if (typeof el !== 'string' || el.trim() === '') return null
    items.push(el)
  }
  return { kind: 'list', items }
}

// ── 数据来源判别:优先 result(已完成结果更具展示价值),退回 input。 ──
// 返回命中的形态 + 来源(决定头部 brief 文案)。命中不了返回 null。
interface Picked {
  shape: Shape
  from: 'result' | 'input'
}
function pickShape(block: Block, resultText: string | undefined): Picked | null {
  // (a) result:整串解析为 JSON 值后分类。
  if (resultText) {
    const parsed = tryParseJson(resultText)
    if (parsed != null) {
      const s = classifyValue(parsed)
      if (s) return { shape: s, from: 'result' }
    }
  }
  // (b) input:整体是数组/状态对象?(少见但可能)
  const input = asInput(block)
  const whole = classifyValue(input)
  if (whole) return { shape: whole, from: 'input' }
  // (c) input 的某个字段是「扁平对象数组」或「字符串列表」—— 只在恰好一个字段命中时采纳(避免歧义)。
  const hits: Picked[] = []
  for (const k of Object.keys(input)) {
    const s = asTable(input[k]) || asStringList(input[k])
    if (s) hits.push({ shape: s, from: 'input' })
  }
  if (hits.length === 1) return hits[0]
  return null
}

// ════════════════════════ 子视图 ════════════════════════

// 表格:复用 .kv 容器观感,以真表格语义渲染列;横向滚动包一层 .src-scroll 防移动端溢出。
function TableView({ shape }: { shape: TableShape }) {
  return (
    <div className="src-scroll">
      <table className="src-tbl">
        <thead>
          <tr>
            {shape.cols.map((c) => (
              <th key={c} className="src-th">
                {c}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {shape.rows.map((r, i) => (
            <tr key={i} className="src-tr">
              {shape.cols.map((c) => (
                <td key={c} className="src-td">
                  {r[c]}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// 状态:药丸(on/warn/中性)+ 其余标量字段 kv。
function StatusView({ shape }: { shape: StatusShape }) {
  const pillCls = 'pill' + (shape.good === true ? ' on' : shape.good === false ? ' warn' : '')
  return (
    <div className="src-status">
      <span className={pillCls}>{shape.label}</span>
      {shape.rows.length > 0 && (
        <div className="kv src-kv">
          {shape.rows.map((r, i) => (
            <div className="kv-row" key={i}>
              <span className="kv-k">{r.k}</span>
              <span className="kv-v">{r.v}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// 字符串列表:复用搜索族列表视觉(项目符号 + 等宽文本)。
function ListView({ shape }: { shape: ListShape }) {
  return (
    <ul className="src-list">
      {shape.items.map((s, i) => (
        <li className="src-li" key={i}>
          <span className="src-li-m">·</span>
          <span className="src-li-t">{s}</span>
        </li>
      ))}
    </ul>
  )
}

// 形态 → 子视图分发。
function ShapeView({ shape }: { shape: Shape }) {
  if (shape.kind === 'table') return <TableView shape={shape} />
  if (shape.kind === 'status') return <StatusView shape={shape} />
  return <ListView shape={shape} />
}

// ── 卡内小节:带标签的二级折叠段(与 MetaTools 同款,自包含以免跨文件依赖私有件)。 ──
function Section({ label, defaultOpen, children }: { label: string; defaultOpen: boolean; children: ReactNode }) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <div className="src-sec">
      <button className="src-sec-h" onClick={() => setOpen((o) => !o)}>
        <span className="src-sec-chev">{open ? '▾' : '▸'}</span>
        {label}
      </button>
      {open && <div className="src-sec-body">{children}</div>}
    </div>
  )
}

// ════════════════════════ 主组件 ════════════════════════
// 与 GenericCard 同壳:icon='🧩' accent='unknown',头部带 briefOf,状态由 useToolStatus 推导。
// 正文:结构化子视图(表/状态/列表)+ 折叠的原始结果(ResultPane mono)+ 折叠的入参(CodeCanvas json)。
// 关键:result 来源的命中只能在组件内用 useToolStatus 取到 —— 故本卡自己判定 pickShape;
//   命中不了则就地回落 <GenericCard/>(与 router 兜底一致),使 result-only 的结构化场景也能被识别。
export interface StructuredResultCardProps {
  block: Block
}
export function StructuredResultCard({ block }: StructuredResultCardProps) {
  const input = asInput(block)
  const { status, res } = useToolStatus(block)
  const picked = pickShape(block, res?.content)

  // 未命中(含「结果尚未到达且 input 也不结构化」)→ 与 router 兜底等价的裸 JSON 卡。
  if (!picked) return <GenericCard block={block} />

  const name = block.name || 'tool'
  const brief = briefOf(input)
  const json = JSON.stringify(input, null, 2)
  // 来源标识:result 命中 → 「结构化结果」;input 命中 → 「结构化入参」。
  const fromTag = picked.from === 'result' ? '结构化结果' : '结构化入参'

  return (
    <BlockShell
      icon="🧩"
      title={name}
      brief={brief}
      accent="unknown"
      status={status}
      defaultOpen={true}
      headerExtra={<span className="pill src-tag">{fromTag}</span>}
    >
      <div className="src">
        <ShapeView shape={picked.shape} />

        {/* 原始结果:命中后默认收起(结构化视图已是主角)。 */}
        {res && res.content && (
          <Section label="原始结果" defaultOpen={false}>
            <ResultPane content={res.content} isError={res.isError} variant="mono" />
          </Section>
        )}

        {/* 入参 JSON:默认收起,供需要时核对(数据来自 result 时尤为辅助)。 */}
        {Object.keys(input).length > 0 && (
          <Section label="入参" defaultOpen={false}>
            <CodeCanvas code={json} lang="json" gutter={false} />
          </Section>
        )}
      </div>
    </BlockShell>
  )
}

// ── 集成入口:router 兜底处调用 const rich = tryStructuredResult(block); return rich ?? <GenericCard …/>。 ──
// 设计取舍:tool_use 的 result 存在主 store 里,只能用 useToolStatus(组件内)同步取到;
//   纯函数无法预判 result 是否结构化。为不漏掉「result-only」的结构化结果(本卡核心场景),
//   本函数总是返回 <StructuredResultCard/>,由卡内 pickShape 做最终判定 ——
//   命中则富渲染,未命中则就地回落 <GenericCard/>(与 router 的 ?? 兜底完全一致,故 router 行为不变)。
// 即:返回值用作 router 的 `rich ?? <GenericCard/>` 时,?? 退化为无害恒真(rich 始终非空),
//   真正的「是否富渲染」决策内聚在卡里;router 无需感知。
export function tryStructuredResult(block: Block): ReactNode {
  return <StructuredResultCard block={block} />
}

export default StructuredResultCard
