// 批次2 · 网络族控件:取网页 WebFetch / 搜网页 WebSearch。前缀 wf-/ws-。
// 依赖批次0:BlockShell(折叠卡壳)、Collapsible(折叠正文)、ResultPane(结果面板)、useToolStatus(三态+结果)。
// 复用批次1:MD(components.tsx)。视觉:web 族蓝左条;失败由 BlockShell status=err 走红边。
import { type ReactNode } from 'react'
import { BlockShell, Collapsible, ResultPane, unwrapErr, useToolStatus } from './shell'
import type { Block } from '../types'

// 取入参中的字符串字段(缺省空串)。
function str(input: Record<string, unknown> | undefined, k: string): string {
  const v = input?.[k]
  return typeof v === 'string' ? v : ''
}
// 取入参中的字符串数组字段(过滤非串项)。
function strArr(input: Record<string, unknown> | undefined, k: string): string[] {
  const v = input?.[k]
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string' && !!x.trim()) : []
}

// URL 拆 host / pathname(失败回退:整串当 host,pathname 空)。
interface UrlParts {
  host: string
  path: string
}
function parseUrl(url: string): UrlParts {
  try {
    const u = new URL(url)
    let path = u.pathname + u.search
    if (path === '/') path = ''
    return { host: u.host, path }
  } catch {
    return { host: url, path: '' }
  }
}

// ── 取网页 WebFetch ──
// brief = host 亮 + pathname 次;url 可点 target=_blank;正文 = prompt 一行可折叠 + result ResultPane md。
export interface WebFetchCardProps {
  block: Block
}
export function WebFetchCard({ block }: WebFetchCardProps) {
  const input = (block.input || {}) as Record<string, unknown>
  const url = str(input, 'url')
  const prompt = str(input, 'prompt')
  const { host, path } = parseUrl(url)
  const { status, res } = useToolStatus(block)

  const brief = (
    <span className="wf-brief">
      <span className="wf-host">{host}</span>
      {path && <span className="wf-path">{path}</span>}
    </span>
  )

  const promptLines = prompt ? prompt.split('\n').length : 0

  return (
    <BlockShell icon="🌐" title="取网页" brief={brief} accent="web" status={status} defaultOpen={status === 'err'}>
      {url && (
        <a
          className="wf-url"
          href={url}
          target="_blank"
          rel="noreferrer"
          onClick={(e) => e.stopPropagation()}
        >
          ↗ {url}
        </a>
      )}
      {prompt && (
        <div className="wf-prompt">
          <span className="wf-plabel">问</span>
          <Collapsible lines={1} count={promptLines} fade>
            <span className="wf-ptext">{prompt}</span>
          </Collapsible>
        </div>
      )}
      {res && res.content && <ResultPane content={res.content} isError={res.isError} variant="md" />}
    </BlockShell>
  )
}

// ── 搜网页 WebSearch ──
// brief = query + 结果计数 pill;allowed/blocked_domains 作 pill;
// 正文 ResultPane variant=list:对 content 轻解析成条目(标题亮 + host 蓝可点 + 摘要灰 2 行),解析不动则回退 mono。
export interface WebSearchCardProps {
  block: Block
}
export function WebSearchCard({ block }: WebSearchCardProps) {
  const input = (block.input || {}) as Record<string, unknown>
  const query = str(input, 'query')
  const allowed = strArr(input, 'allowed_domains')
  const blocked = strArr(input, 'blocked_domains')
  const { status, res } = useToolStatus(block)

  // 结果内容剥错误标签后轻解析成条目;为空走回退。
  const raw = res?.content || ''
  const { text } = unwrapErr(raw)
  const items = parseResults(text)

  const brief = (
    <span className="ws-brief">
      <span className="ws-query">{query}</span>
      {items.length > 0 && <span className="pill ws-count">{items.length}</span>}
    </span>
  )

  const headerExtra =
    allowed.length > 0 || blocked.length > 0 ? (
      <span className="ws-doms">
        {allowed.map((d) => (
          <span key={'a' + d} className="pill on ws-dom" title={'允许 ' + d}>
            ✓ {d}
          </span>
        ))}
        {blocked.map((d) => (
          <span key={'b' + d} className="pill off ws-dom" title={'屏蔽 ' + d}>
            ⊘ {d}
          </span>
        ))}
      </span>
    ) : undefined

  return (
    <BlockShell
      icon="🔎"
      title="搜网页"
      brief={brief}
      accent="web"
      status={status}
      defaultOpen={status === 'err'}
      headerExtra={headerExtra}
    >
      {res && res.content ? (
        res.isError ? (
          // 失败:统一 mono 终端(含错误头)。
          <ResultPane content={res.content} isError variant="mono" />
        ) : items.length > 0 ? (
          // 命中:解析成条目列表。
          <ResultList items={items} />
        ) : (
          // 解析不动:回退 mono。
          <ResultPane content={res.content} variant="mono" />
        )
      ) : null}
    </BlockShell>
  )
}

// ── 搜索结果条目 ──
interface SearchHit {
  title: string
  url?: string
  host?: string
  snippet?: string
}

// 轻解析:WebSearch 结果文本无固定 schema,尽力从多种常见排版抽条目;抽不出返回空数组(调用方回退 mono)。
// 支持:1) JSON 数组([{title,url,snippet}...]);2) 行式「标题 — URL」「URL」+ 后续缩进/段落摘要。
function parseResults(text: string): SearchHit[] {
  const body = (text || '').trim()
  if (!body) return []

  // 1) JSON 尝试(整段或被 ```json 包裹)。
  const jsonHits = tryJson(body)
  if (jsonHits.length) return jsonHits

  // 2) 行式:扫每行找 URL;URL 行作为一条命中的锚点,其上一非空行(若非 URL)作标题,其后续非 URL 行作摘要。
  const urlRe = /\bhttps?:\/\/[^\s)）'"<>]+/
  const lines = body.split('\n')
  const hits: SearchHit[] = []
  let pending: SearchHit | null = null
  let lastNonUrl = ''

  const flush = () => {
    if (pending) {
      if (pending.snippet) pending.snippet = pending.snippet.trim()
      hits.push(pending)
      pending = null
    }
  }

  for (const rawLine of lines) {
    const line = rawLine.trim()
    if (!line) {
      lastNonUrl = ''
      continue
    }
    const m = line.match(urlRe)
    if (m) {
      // 新命中:行内 URL 前的文本(或上一非空行)作标题。
      flush()
      const url = m[0].replace(/[.,;。,]+$/, '')
      const inline = line.replace(m[0], '').replace(/[-–—:|]+\s*$/, '').replace(/^\s*[-–—•*\d.]+\s*/, '').trim()
      const title = inline || lastNonUrl || hostOf(url) || url
      pending = { title, url, host: hostOf(url), snippet: '' }
      lastNonUrl = ''
    } else if (pending) {
      // URL 之后的散行并入摘要。
      pending.snippet = (pending.snippet ? pending.snippet + ' ' : '') + line
      lastNonUrl = line
    } else {
      lastNonUrl = line
    }
  }
  flush()

  // 至少要有一条带 URL 的命中才算「解析成功」,否则回退 mono。
  return hits.filter((h) => !!h.url)
}

// JSON 解析:整段或 ```json 围栏内,取数组(或 {results:[...]} / {data:[...]})。
function tryJson(body: string): SearchHit[] {
  let src = body
  const fence = body.match(/```(?:json)?\s*([\s\S]*?)```/)
  if (fence) src = fence[1].trim()
  if (!/^[[{]/.test(src)) return []
  try {
    const parsed = JSON.parse(src)
    const arr: unknown[] = Array.isArray(parsed)
      ? parsed
      : Array.isArray((parsed as Record<string, unknown>)?.results)
        ? ((parsed as Record<string, unknown>).results as unknown[])
        : Array.isArray((parsed as Record<string, unknown>)?.data)
          ? ((parsed as Record<string, unknown>).data as unknown[])
          : []
    const hits: SearchHit[] = []
    for (const it of arr) {
      if (!it || typeof it !== 'object') continue
      const o = it as Record<string, unknown>
      const url = pickStr(o, ['url', 'link', 'href'])
      const title = pickStr(o, ['title', 'name', 'heading']) || (url ? hostOf(url) : '')
      const snippet = pickStr(o, ['snippet', 'description', 'summary', 'content', 'text'])
      if (!title && !url) continue
      hits.push({ title: title || url, url: url || undefined, host: url ? hostOf(url) : undefined, snippet: snippet || undefined })
    }
    return hits
  } catch {
    return []
  }
}

function pickStr(o: Record<string, unknown>, keys: string[]): string {
  for (const k of keys) {
    const v = o[k]
    if (typeof v === 'string' && v.trim()) return v.trim()
  }
  return ''
}

function hostOf(url: string): string {
  try {
    return new URL(url).host
  } catch {
    return ''
  }
}

// ── 条目列表渲染:标题亮 + host 蓝可点 + 摘要灰(2 行夹断);条目过多套 Collapsible 软折叠 ──
const LIST_FOLD_ITEMS = 8 // 超此条数即软折叠
const ROWS_PER_ITEM = 4 // 每条约占行数(标题 + host + 2 行摘要),用于 Collapsible 截高估算
function ResultList({ items }: { items: SearchHit[] }): ReactNode {
  const body = (
    <ol className="ws-list">
      {items.map((h, i) => (
        <li key={i} className="ws-item">
          <div className="ws-title">{h.title}</div>
          {h.url && (
            <a
              className="ws-link"
              href={h.url}
              target="_blank"
              rel="noreferrer"
              onClick={(e) => e.stopPropagation()}
            >
              {h.host || h.url}
            </a>
          )}
          {h.snippet && <div className="ws-snip">{h.snippet}</div>}
        </li>
      ))}
    </ol>
  )
  return (
    <div className="rp">
      <Collapsible lines={LIST_FOLD_ITEMS * ROWS_PER_ITEM} count={items.length * ROWS_PER_ITEM} fade>
        {body}
      </Collapsible>
    </div>
  )
}
