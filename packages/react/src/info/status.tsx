import type { ReactNode } from 'react'
import { KV, Pill } from './shared'
import type { CardModule } from './types'

// /status 卡:把 Claude Code status 面板抓屏解析成结构化字段 + MCP 计数,重渲成原生控件。
// 抓屏每行带前导空格,夹杂 tab 栏「Settings Status …」、信任提示、底部「Esc to cancel」等噪声,
// parser 只盯「Key:   value」标记行,缺关键字段(Version / Session ID)就返回 null。

export interface Status {
  fields: { k: string; v: string }[]
  mcp: { connected: number; needAuth: number; disabled: number } | null
}

// 需收集的字段标签,按面板出现顺序。其余 Key: value 行(噪声/未知)忽略。
const KEYS = [
  'Version',
  'Session name',
  'Session ID',
  'cwd',
  'Login method',
  'Organization',
  'Email',
  'Model',
  'MCP servers',
  'Setting sources',
]

// 英文标签 → 中文显示标签。
const LABEL: Record<string, string> = {
  Version: '版本',
  'Session name': '会话名',
  'Session ID': '会话ID',
  cwd: '目录',
  'Login method': '登录方式',
  Organization: '组织',
  Email: '邮箱',
  Model: '模型',
  'Setting sources': '设置来源',
}

const NAMELESS = '/rename to add a name'

// 从「3 connected」「need auth」等片段里取首个整数,无则 0。
const num = (s: string) => {
  const m = s.match(/\d+/)
  return m ? Number(m[0]) : 0
}

export function parseStatus(text: string): Status | null {
  const fields: { k: string; v: string }[] = []
  let mcp: Status['mcp'] = null

  for (const raw of text.split('\n')) {
    const line = raw.trim()
    const m = line.match(/^([A-Za-z][A-Za-z ]*?):\s+(.+?)\s*$/)
    if (!m) continue
    const key = m[1].trim()
    if (!KEYS.includes(key)) continue
    const val = m[2].trim()

    if (key === 'MCP servers') {
      // 剥掉尾部「 · /mcp」,再按 connected / need auth / disabled 取数。
      const body = val.replace(/\s*·\s*\/mcp\s*$/, '')
      const part = (re: RegExp) => {
        const x = body.match(re)
        return x ? num(x[0]) : 0
      }
      mcp = {
        connected: part(/\d+\s+connected/),
        needAuth: part(/\d+\s+need auth/),
        disabled: part(/\d+\s+disabled/),
      }
    }

    fields.push({ k: key, v: val })
  }

  // 防御:没有 Version 也没有 Session ID,大概率不是 status 面板。
  const has = (k: string) => fields.some((f) => f.k === k)
  if (!has('Version') && !has('Session ID')) return null

  return { fields, mcp }
}

export function StatusCard({ data }: { data: Status }) {
  const get = (k: string) => data.fields.find((f) => f.k === k)?.v

  // 标量字段:按 LABEL 顺序，会话名为占位符时渲染灰字「未命名」。
  const rows: { k: string; v: ReactNode }[] = []
  for (const key of KEYS) {
    if (!(key in LABEL)) continue
    const v = get(key)
    if (v == null) continue
    if (key === 'Session name' && v === NAMELESS) {
      rows.push({ k: LABEL[key], v: <span style={{ color: 'var(--mut)' }}>未命名</span> })
    } else {
      rows.push({ k: LABEL[key], v })
    }
  }

  const mcp = data.mcp

  return (
    <div className="info">
      <KV rows={rows} />
      {mcp && (mcp.connected > 0 || mcp.needAuth > 0 || mcp.disabled > 0) && (
        <div className="set-row">
          <span className="set-k">MCP</span>
          <span className="pills">
            {mcp.connected > 0 && <Pill tone="on">{mcp.connected} 已连接</Pill>}
            {mcp.needAuth > 0 && <Pill tone="warn">{mcp.needAuth} 待授权</Pill>}
            {mcp.disabled > 0 && <Pill tone="off">{mcp.disabled} 禁用</Pill>}
          </span>
        </div>
      )}
    </div>
  )
}

export const status: CardModule<Status> = { parse: parseStatus, Card: StatusCard }
