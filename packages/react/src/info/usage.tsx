import { Bars } from './shared'
import type { CardModule } from './types'

// Usage 卡(/cost 落地页)。把抓屏文本解析成结构化额度/用量,再重渲成原生控件。
// 抓屏每行带前导空格,且夹杂 tab 栏「Settings Status …」、信任提示、底部提示等噪声;
// 只盯本卡关键标记行,cost 缺失即 return null(上层回退原文)。

export interface Usage {
  cost: string
  apiDur: string
  wallDur: string
  added: string
  removed: string
  tokens: { input: string; output: string; cacheR: string; cacheW: string } | null
  limits: { label: string; pct: number; resets: string }[]
  notes: string[]
}

export function parseUsage(text: string): Usage | null {
  const lines = text.split('\n')
  const trimmed = lines.map((l) => l.trim())

  // 取某标记行冒号后的内容。
  const after = (mark: string) => {
    const hit = trimmed.find((l) => l.startsWith(mark))
    if (!hit) return ''
    return hit.slice(mark.length).trim()
  }

  const cost = after('Total cost:')
  if (!cost) return null

  const apiDur = after('Total duration (API):')
  const wallDur = after('Total duration (wall):')

  const changes = after('Total code changes:')
  const chM = changes.match(/(\d+)\s+lines added,\s*(\d+)\s+lines removed/)
  const added = chM ? chM[1] : '0'
  const removed = chM ? chM[2] : '0'

  const usage = after('Usage:')
  const uM = usage.match(/(\d+)\s+input,\s*(\d+)\s+output,\s*(\d+)\s+cache read,\s*(\d+)\s+cache write/)
  const tokens = uM ? { input: uM[1], output: uM[2], cacheR: uM[3], cacheW: uM[4] } : null

  // limits 三行结构:标签行 / 条形+「NN% used」 / 可选 Resets。
  // 扫匹配「NN% used」的行,label 取其上方最近的非空非 Resets 标签行,resets 取下方紧邻 Resets。
  const limits: Usage['limits'] = []
  trimmed.forEach((l, i) => {
    const m = l.match(/(\d+)%\s*used/)
    if (!m) return
    let label = ''
    for (let j = i - 1; j >= 0; j--) {
      const up = trimmed[j]
      if (!up || up.startsWith('Resets')) continue
      label = up
      break
    }
    let resets = ''
    const down = trimmed[i + 1]
    if (down) {
      const rM = down.match(/^Resets (.+)/)
      if (rM) resets = rM[1].trim()
    }
    limits.push({ label, pct: Number(m[1]), resets })
  })

  // notes:What's contributing 之后的百分比描述行。
  const notes: string[] = []
  const start = trimmed.findIndex((l) => l.startsWith("What's contributing"))
  if (start >= 0) {
    for (let i = start + 1; i < trimmed.length; i++) {
      const l = trimmed[i]
      if (/^\d+%/.test(l)) notes.push(l)
    }
  }

  return { cost, apiDur, wallDur, added, removed, tokens, limits, notes }
}

export function UsageCard({ data }: { data: Usage }) {
  return (
    <div className="info">
      <div className="cost-top">
        <span className="cost-big">{data.cost}</span>
        <span className="cost-sub">
          API {data.apiDur} · 墙钟 {data.wallDur}
        </span>
      </div>

      {data.tokens && (
        <div className="info-grid">
          <div className="info-cell">
            <span className="ik">输入</span>
            <span className="iv">{data.tokens.input}</span>
          </div>
          <div className="info-cell">
            <span className="ik">输出</span>
            <span className="iv">{data.tokens.output}</span>
          </div>
          <div className="info-cell">
            <span className="ik">缓存读</span>
            <span className="iv">{data.tokens.cacheR}</span>
          </div>
          <div className="info-cell">
            <span className="ik">缓存写</span>
            <span className="iv">{data.tokens.cacheW}</span>
          </div>
          <div className="info-cell">
            <span className="ik">代码+</span>
            <span className="iv">{data.added}</span>
          </div>
          <div className="info-cell">
            <span className="ik">代码−</span>
            <span className="iv">{data.removed}</span>
          </div>
        </div>
      )}

      {data.limits.length > 0 && (
        <Bars
          items={data.limits.map((x) => ({
            name: x.label,
            right: x.pct + '%' + (x.resets ? ' · 重置 ' + x.resets : ''),
            pct: x.pct,
          }))}
        />
      )}

      {data.notes.length > 0 && (
        <div>
          <div className="ik">占用构成</div>
          {data.notes.map((n, i) => (
            <div className="stat-quip" key={i}>
              {n}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

export const usage: CardModule<Usage> = { parse: parseUsage, Card: UsageCard }
