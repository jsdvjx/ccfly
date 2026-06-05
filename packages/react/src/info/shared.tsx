import type { ReactNode } from 'react'

// 信息卡共享原语。usage/status/stats/settings 四张卡都复用这些,保证视觉统一、避免各写各的 CSS。
// 解析约定:抓屏文本每行带 3 空格缩进,且夹杂 tab 栏「Settings Status …」、信任提示、底部提示等噪声行,
// 各 parser 只盯自己关心的标记行,匹配不到就返回 null(上层回退原文)。

export const lvl = (pct: number) => (pct >= 85 ? 'red' : pct >= 60 ? 'amber' : 'green')

// 百分比条列表(额度、占比等)。free=灰条,>=85% 自动转红。
export function Bars({ items }: { items: { name: string; right: string; pct: number; free?: boolean }[] }) {
  return (
    <div className="cats">
      {items.map((c, i) => (
        <div className="cat" key={i}>
          <div className="cat-top">
            <span className="cat-n">{c.name}</span>
            <span className="cat-v">{c.right}</span>
          </div>
          <div className="cat-bar">
            <div
              className={'cat-fill' + (c.free ? ' free' : c.pct >= 85 ? ' over' : '')}
              style={{ width: Math.min(100, c.pct) + '%' }}
            />
          </div>
        </div>
      ))}
    </div>
  )
}

// 竖排键值列表(状态等)。空值自动跳过。
export function KV({ rows }: { rows: { k: string; v: ReactNode }[] }) {
  return (
    <div className="kv">
      {rows
        .filter((r) => r.v !== '' && r.v != null)
        .map((r, i) => (
          <div className="kv-row" key={i}>
            <span className="kv-k">{r.k}</span>
            <span className="kv-v">{r.v}</span>
          </div>
        ))}
    </div>
  )
}

// 状态药丸:on=绿、off=灰、warn=黄、默认中性。
export function Pill({ tone, children }: { tone?: 'on' | 'off' | 'warn'; children: ReactNode }) {
  return <span className={'pill' + (tone ? ' ' + tone : '')}>{children}</span>
}
