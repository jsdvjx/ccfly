import type { CardModule } from './types'

// Stats 卡(/cost 面板的 Stats tab)。抓屏 → 结构化 → 原生渲染(非 dump 原文)。
// 该 tab 无直达命令:上层用 /cost 落地 Usage 后定向 1×Right 到达,再抓屏喂给 parseStats。

export interface Stats {
  subtabs: string[]
  ranges: string[]
  pairs: { k: string; v: string }[]
  quip: string
  heat: string
}

const LABELS: Record<string, string> = {
  'Favorite model': '常用模型',
  'Total tokens': '总 Tokens',
  Sessions: '会话数',
  'Longest session': '最长会话',
  'Active days': '活跃天数',
  'Longest streak': '最长连续',
  'Most active day': '最活跃日',
  'Current streak': '当前连续',
}

export function parseStats(text: string): Stats | null {
  const lines = text.split('\n')

  // 子 tab:含 Overview + Models 的那行
  let subtabs: string[] = []
  for (const ln of lines) {
    if (/\bOverview\b/.test(ln) && /\bModels\b/.test(ln)) {
      subtabs = ln.trim().split(/\s{2,}/).filter(Boolean)
      break
    }
  }

  // 日期范围:含 "All time" 且以 · 分隔的那行
  let ranges: string[] = []
  for (const ln of lines) {
    if (/All time/.test(ln) && ln.includes('·')) {
      ranges = ln.trim().split(/\s*·\s*/).filter(Boolean)
      break
    }
  }

  // 统计对:逐行全局抓 Label: value(一行可能两组),仅保留已知键
  const pairs: { k: string; v: string }[] = []
  const seen = new Set<string>()
  const re = /([A-Za-z][A-Za-z ]+?):\s+([^\n]+?)(?:\s{2,}|$)/g
  for (const ln of lines) {
    if (/All time/.test(ln)) continue
    re.lastIndex = 0
    let m: RegExpExecArray | null
    while ((m = re.exec(ln))) {
      const k = m[1].trim()
      if (!(k in LABELS) || seen.has(k)) continue
      seen.add(k)
      pairs.push({ k, v: m[2].trim() })
    }
  }

  // 俏皮话
  const quipLn = lines.find((l) => /Your longest session/.test(l) || /longer than|listening to/.test(l))
  const quip = quipLn ? quipLn.trim() : ''

  // 热力图:月份行 → "Less … More" 行(去公共前导缩进,保留对齐)
  let hs = -1
  let he = -1
  for (let i = 0; i < lines.length; i++) {
    if (hs < 0 && /Jun|Jul|Aug|Sep/.test(lines[i]) && /Jan|Feb|Mar/.test(lines[i])) hs = i
    if (hs >= 0 && /Less .* More/.test(lines[i])) {
      he = i
      break
    }
  }
  let heat = ''
  if (hs >= 0 && he >= hs) {
    const block = lines.slice(hs, he + 1)
    const indent = Math.min(...block.filter((l) => l.trim()).map((l) => (l.match(/^ */) || [''])[0].length))
    heat = block.map((l) => l.slice(indent).replace(/\s+$/, '')).join('\n')
  }

  if (!seen.has('Sessions') && !seen.has('Favorite model')) return null
  return { subtabs, ranges, pairs, quip, heat }
}

export function StatsCard({ data }: { data: Stats }) {
  return (
    <div className="info">
      {data.subtabs.length > 0 && (
        <div className="seg">
          {data.subtabs.map((t, i) => (
            <span key={t} className={'seg-i' + (i === 0 ? ' on' : '')}>
              {t}
            </span>
          ))}
        </div>
      )}
      {data.ranges.length > 0 && (
        <div className="seg">
          {data.ranges.map((t) => (
            <span key={t} className="seg-i">
              {t}
            </span>
          ))}
        </div>
      )}
      {data.heat && <pre className="heat">{data.heat}</pre>}
      {data.pairs.length > 0 && (
        <div className="info-grid">
          {data.pairs.map((p) => (
            <div className="info-cell" key={p.k}>
              <span className="ik">{LABELS[p.k] || p.k}</span>
              <span className="iv">{p.v}</span>
            </div>
          ))}
        </div>
      )}
      {data.quip && <div className="stat-quip">{data.quip}</div>}
    </div>
  )
}

export const stats: CardModule<Stats> = { parse: parseStats, Card: StatsCard }
