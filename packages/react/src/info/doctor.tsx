import type { ReactNode } from 'react'
import { KV } from './shared'
import type { CardModule } from './types'

// /doctor 卡:把 Claude Code Diagnostics 面板抓屏解析成结构化分段(段标题 + 行),重渲成原生控件。
// 抓屏每行带前导空格,夹杂噪声:顶部信任提示块、底部「↓ N below」「Esc to …」、url 等;树形行以
// ├/└ 开头,要剥掉。parser 只在「某行下方紧跟树符行」时才认其为段标题——顶部噪声不会紧跟树符,
// 据此天然滤掉。关键标记缺失(0 段、或全程无 kv/check 行)就返回 null,上层回退原文。

export type DoctorRow =
  | { kind: 'kv'; k: string; v: string }
  | { kind: 'check'; ok: boolean; text: string }
  | { kind: 'text'; text: string }

export interface DoctorSection {
  title: string
  rows: DoctorRow[]
}

export interface Doctor {
  sections: DoctorSection[]
}

// 是否树符行(剥掉前导空格后以 ├/└ 起头)。
const isTree = (line: string) => /^[├└]/.test(line)

// 剥掉行首的树符(├/└)及其后紧跟的连接符/空白,再 trim 得到正文。
const stripTree = (line: string) => line.replace(/^[├└][─-]?\s*/, '').trim()

// 把一条「正文」(已剥树符)归类成 kv / check / text。
function classify(body: string): DoctorRow {
  if (body.startsWith('✓') || body.startsWith('✗')) {
    return { kind: 'check', ok: body.startsWith('✓'), text: body.slice(1).trim() }
  }
  const m = body.match(/^([^:]+):\s*(.+)$/)
  if (m) return { kind: 'kv', k: m[1].trim(), v: m[2].trim() }
  return { kind: 'text', text: body }
}

export function parseDoctor(text: string): Doctor | null {
  const lines = text.split('\n').map((l) => l.trim())
  const sections: DoctorSection[] = []
  let cur: DoctorSection | null = null

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (!line) continue

    if (isTree(line)) {
      // 树符行属于当前段;没有当前段则丢弃(防御:树符出现在任何标题之前)。
      if (cur) cur.rows.push(classify(stripTree(line)))
      continue
    }

    // 普通行:仅当「下一条非空行」是树符行时,才视为新段标题——借此跳过顶部信任提示等噪声。
    let j = i + 1
    while (j < lines.length && !lines[j]) j++
    if (j < lines.length && isTree(lines[j])) {
      cur = { title: line, rows: [] }
      sections.push(cur)
    }
    // 否则:孤立普通行(噪声 / 底部提示 / url),忽略。
  }

  // 防御:没有任何段,或所有段里都没有 kv/check 行,大概率不是 doctor 面板。
  if (sections.length === 0) return null
  const hasData = sections.some((s) => s.rows.some((r) => r.kind === 'kv' || r.kind === 'check'))
  if (!hasData) return null

  return { sections }
}

export function DoctorCard({ data }: { data: Doctor }) {
  return (
    <div className="info">
      {data.sections.map((sec, si) => (
        <div key={si}>
          <div className="sec-h">{sec.title}</div>
          {renderRows(sec.rows)}
        </div>
      ))}
    </div>
  )
}

// 段内逐行渲染:连续的 kv 收进一个 KV 组件,check/text 各自成块,整体保持原顺序。
function renderRows(rows: DoctorRow[]): ReactNode[] {
  const out: ReactNode[] = []
  let kvBuf: { k: string; v: ReactNode }[] = []

  const flush = (key: string) => {
    if (kvBuf.length) {
      out.push(<KV key={key} rows={kvBuf} />)
      kvBuf = []
    }
  }

  rows.forEach((r, i) => {
    if (r.kind === 'kv') {
      // 路径类值用等宽 + 自动换行,长路径在窄屏不溢出。
      const v: ReactNode = r.k === 'Path' ? <span className="info-cwd">{r.v}</span> : r.v
      kvBuf.push({ k: r.k, v })
      return
    }
    flush('kv-' + i)
    if (r.kind === 'check') {
      out.push(
        <div className="chk" key={i}>
          <span className={'chk-i' + (r.ok ? '' : ' bad')}>{r.ok ? '✓' : '✗'}</span>
          <span>{r.text}</span>
        </div>,
      )
    } else {
      out.push(
        <div className="note" key={i}>
          {r.text}
        </div>,
      )
    }
  })

  flush('kv-end')
  return out
}

export const doctor: CardModule<Doctor> = { parse: parseDoctor, Card: DoctorCard }
