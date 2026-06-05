// 批次2 · 搜索族控件:Grep / Glob / LS。前缀 gr-/gl-/ls-;命中高亮复用基座 .bs-hit。
// 依赖批次0:BlockShell(折叠卡壳)、Collapsible(折叠正文)、useToolStatus、unwrapErr。
// 视觉:IDE 化终端 —— 紧凑等宽、文件图标、命中子串黄色包高亮、按文件分组可折叠。
// 解析 Grep/Glob/LS 的 tool_result 文本(Claude Code 约定格式),失败/错误回退原文 pre。
import { useState, type ReactNode } from 'react'
import { BlockShell, Collapsible, useToolStatus, unwrapErr } from './shell'
import { fileIcon } from './meta'
import type { Block } from '../types'

// ── 小工具:取入参字符串 / 布尔 ──
function str(input: Record<string, unknown> | undefined, k: string): string {
  const v = input && input[k]
  return typeof v === 'string' ? v : ''
}
function truthy(input: Record<string, unknown> | undefined, k: string): boolean {
  return !!(input && input[k])
}

// 绝对/相对路径 → { dir, base }。dir 含尾随 '/'(无目录则空)。
function splitBase(path: string): { dir: string; base: string } {
  const p = path || ''
  const base = p.split('/').pop() || p
  const dir = p.slice(0, p.length - base.length)
  return { dir, base }
}

// ── 命中子串高亮:按 pattern 在一行内包 <mark class="bs-hit"> ──
// pattern 是正则(Grep 用);构造失败(非法正则)则原样返回不高亮,避免抛错。
function hiLine(line: string, re: RegExp | null): ReactNode {
  if (!re) return line
  re.lastIndex = 0
  const out: ReactNode[] = []
  let last = 0
  let m: RegExpExecArray | null
  let guard = 0
  while ((m = re.exec(line)) !== null) {
    if (guard++ > 500) break
    const start = m.index
    const end = start + m[0].length
    if (m[0].length === 0) {
      re.lastIndex++
      continue
    }
    if (start > last) out.push(line.slice(last, start))
    out.push(
      <mark key={start} className="bs-hit">
        {line.slice(start, end)}
      </mark>,
    )
    last = end
  }
  if (last < line.length) out.push(line.slice(last))
  return out.length ? out : line
}

// pattern → 高亮用正则(全局、忽略大小写视 -i)。非法正则返回 null。
function buildHi(pattern: string, ci: boolean): RegExp | null {
  if (!pattern) return null
  try {
    return new RegExp(pattern, ci ? 'gi' : 'g')
  } catch {
    return null
  }
}

// ════════════════════════ Grep ════════════════════════
// 解析 files_with_matches:首行可能是「Found N files」,余为路径。
function parseFilesList(text: string): string[] {
  return text
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l && !/^Found\s+\d+\s+files?/i.test(l) && !/^No\s+(files|matches)\s+found/i.test(l))
}

// 解析 count 模式:每行 path:count。
interface CountRow {
  path: string
  count: number
}
function parseCount(text: string): CountRow[] {
  const out: CountRow[] = []
  for (const raw of text.split('\n')) {
    const l = raw.trim()
    if (!l || /^Found\s+\d+/i.test(l)) continue
    const m = l.match(/^(.*):(\d+)$/)
    if (m) out.push({ path: m[1], count: Number(m[2]) })
  }
  return out
}

// 解析 content 模式:行形如 path:line:text(带 -n)或 path:text(无 -n);
// 分隔行(--)与无冒号行按上一文件的续行处理。按 path 分组。
interface ContentHit {
  line?: number
  text: string
}
interface ContentGroup {
  path: string
  hits: ContentHit[]
}
function parseContent(text: string): { groups: ContentGroup[]; total: number } {
  const groups: ContentGroup[] = []
  const byPath = new Map<string, ContentGroup>()
  let total = 0
  let lastPath = ''
  for (const raw of text.split('\n')) {
    if (raw === '' && groups.length === 0) continue
    if (/^--$/.test(raw.trim())) continue
    if (/^Found\s+\d+/i.test(raw.trim())) continue
    // 尝试 path:line:text
    let m = raw.match(/^([^\s:][^:]*?):(\d+):([\s\S]*)$/)
    let path: string
    let line: number | undefined
    let body: string
    if (m) {
      path = m[1]
      line = Number(m[2])
      body = m[3]
    } else {
      // 尝试 path:text(无行号)
      m = raw.match(/^([^\s:][^:]*?):([\s\S]*)$/)
      if (m && /[/.]/.test(m[1])) {
        path = m[1]
        body = m[2]
      } else {
        // 续行:并入上一文件最后一条命中
        if (lastPath) {
          const g = byPath.get(lastPath)
          if (g && g.hits.length) g.hits[g.hits.length - 1].text += '\n' + raw
        }
        continue
      }
    }
    let g = byPath.get(path)
    if (!g) {
      g = { path, hits: [] }
      byPath.set(path, g)
      groups.push(g)
    }
    g.hits.push({ line, text: body })
    total++
    lastPath = path
  }
  return { groups, total }
}

// 条件徽标:-i / glob / type / 路径范围。
function GrepBadges({ input }: { input: Record<string, unknown> | undefined }) {
  const badges: string[] = []
  if (truthy(input, '-i')) badges.push('-i')
  const glob = str(input, 'glob')
  if (glob) badges.push('glob ' + glob)
  const type = str(input, 'type')
  if (type) badges.push('type ' + type)
  if (truthy(input, '-n')) badges.push('-n')
  const ctx = ['-A', '-B', '-C'].map((k) => {
    const v = input && input[k]
    return typeof v === 'number' ? k + ' ' + v : ''
  }).filter(Boolean)
  badges.push(...ctx)
  if (!badges.length) return null
  return (
    <span className="gr-badges">
      {badges.map((b, i) => (
        <span key={i} className="pill gr-badge">
          {b}
        </span>
      ))}
    </span>
  )
}

// 单文件分组(content 模式):文件头(图标 + basename + 目录灰 + 命中数)可点折叠 + 行号 dim + 命中高亮。
function GrepGroup({ group, re }: { group: ContentGroup; re: RegExp | null }) {
  const [open, setOpen] = useState(true)
  const { dir, base } = splitBase(group.path)
  const { glyph, color } = fileIcon(group.path)
  return (
    <div className="gr-group">
      <div className="gr-ghead" onClick={() => setOpen((o) => !o)} role="button">
        <span className="gr-chev">{open ? '▾' : '▸'}</span>
        <span className="gr-gicon" style={{ color }}>
          {glyph}
        </span>
        <span className="gr-gbase">{base}</span>
        {dir && <span className="gr-gdir">{dir}</span>}
        <span className="gr-gcnt">{group.hits.length}</span>
      </div>
      {open && (
        <pre className="gr-lines">
          {group.hits.map((h, i) => (
            <div className="gr-line" key={i}>
              {h.line != null && <span className="gr-no">{h.line}</span>}
              <span className="gr-code">{hiLine(h.text, re)}</span>
            </div>
          ))}
        </pre>
      )}
    </div>
  )
}

export interface GrepCardProps {
  block: Block
}
export function GrepCard({ block }: GrepCardProps) {
  const { status, res } = useToolStatus(block)
  const input = (block.input || {}) as Record<string, unknown>
  const pattern = str(input, 'pattern')
  const mode = str(input, 'output_mode') || 'files_with_matches'
  const ci = truthy(input, '-i')
  const re = buildHi(pattern, ci)

  let body: ReactNode = null
  let stat: ReactNode = null
  if (res) {
    const { text, forcedErr } = unwrapErr(res.content)
    const err = res.isError || forcedErr
    if (err) {
      body = <pre className="gr-raw gr-raw--err">{text || ' '}</pre>
    } else if (mode === 'files_with_matches') {
      const files = parseFilesList(text)
      stat = <span className="gr-stat">{files.length} 文件</span>
      body = (
        <Collapsible lines={14} count={files.length} fade>
          <ul className="gr-files">
            {files.map((p, i) => {
              const { dir, base } = splitBase(p)
              const { glyph, color } = fileIcon(p)
              return (
                <li className="gr-file" key={i}>
                  <span className="gr-ficon" style={{ color }}>
                    {glyph}
                  </span>
                  <span className="gr-fbase">{base}</span>
                  {dir && <span className="gr-fdir">{dir}</span>}
                </li>
              )
            })}
          </ul>
        </Collapsible>
      )
    } else if (mode === 'count') {
      const rows = parseCount(text)
      const totalHits = rows.reduce((a, r) => a + r.count, 0)
      stat = (
        <span className="gr-stat">
          {rows.length} 文件 · {totalHits} 命中
        </span>
      )
      body = (
        <Collapsible lines={14} count={rows.length} fade>
          <ul className="gr-files">
            {rows.map((r, i) => {
              const { dir, base } = splitBase(r.path)
              const { glyph, color } = fileIcon(r.path)
              return (
                <li className="gr-file" key={i}>
                  <span className="gr-ficon" style={{ color }}>
                    {glyph}
                  </span>
                  <span className="gr-fbase">{base}</span>
                  {dir && <span className="gr-fdir">{dir}</span>}
                  <span className="gr-fcnt">{r.count}</span>
                </li>
              )
            })}
          </ul>
        </Collapsible>
      )
    } else {
      // content
      const { groups, total } = parseContent(text)
      stat = (
        <span className="gr-stat">
          {groups.length} 文件 · {total} 命中
        </span>
      )
      const totalLines = groups.reduce((a, g) => a + g.hits.length + 1, 0)
      body = (
        <Collapsible lines={20} count={totalLines} fade>
          <div className="gr-groups">
            {groups.map((g, i) => (
              <GrepGroup key={i} group={g} re={re} />
            ))}
          </div>
        </Collapsible>
      )
    }
  }

  return (
    <BlockShell
      icon="⌕"
      title="Grep"
      brief={pattern}
      accent="exec"
      status={status}
      defaultOpen={false}
      headerExtra={stat}
    >
      <GrepBadges input={input} />
      {body}
    </BlockShell>
  )
}

// ════════════════════════ Glob ════════════════════════
// 解析:newline 分隔的路径列表(可能首/尾空行)。
function parseGlob(text: string): string[] {
  return text
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l && !/^No\s+files\s+found/i.test(l) && !/^Found\s+\d+/i.test(l))
}

export interface GlobCardProps {
  block: Block
}
export function GlobCard({ block }: GlobCardProps) {
  const { status, res } = useToolStatus(block)
  const input = (block.input || {}) as Record<string, unknown>
  const pattern = str(input, 'pattern')

  let body: ReactNode = null
  let stat: ReactNode = null
  if (res) {
    const { text, forcedErr } = unwrapErr(res.content)
    const err = res.isError || forcedErr
    if (err) {
      body = <pre className="gl-raw gl-raw--err">{text || ' '}</pre>
    } else {
      const files = parseGlob(text)
      stat = <span className="gl-stat">{files.length} 文件</span>
      body =
        files.length === 0 ? (
          <div className="gl-empty">无匹配</div>
        ) : (
          <Collapsible lines={14} count={files.length} fade>
            <ul className="gl-files">
              {files.map((p, i) => {
                const { dir, base } = splitBase(p)
                const { glyph, color } = fileIcon(p)
                return (
                  <li className="gl-file" key={i}>
                    <span className="gl-ficon" style={{ color }}>
                      {glyph}
                    </span>
                    <span className="gl-fbase">{base}</span>
                    {dir && <span className="gl-fdir">{dir}</span>}
                  </li>
                )
              })}
            </ul>
          </Collapsible>
        )
    }
  }

  return (
    <BlockShell
      icon="☰"
      title="Glob"
      brief={pattern}
      accent="exec"
      status={status}
      defaultOpen={false}
      headerExtra={stat}
    >
      {body}
    </BlockShell>
  )
}

// ════════════════════════ LS ════════════════════════
// LS 结果是缩进目录树(Claude Code 约定:每行「- name/」带前导空格表层级)。
// 保留缩进(white-space:pre),不解析层级;只统计行数 + 超 14 行折叠。
export interface LsCardProps {
  block: Block
}
export function LsCard({ block }: LsCardProps) {
  const { status, res } = useToolStatus(block)
  const input = (block.input || {}) as Record<string, unknown>
  const path = str(input, 'path')

  let body: ReactNode = null
  let stat: ReactNode = null
  if (res) {
    const { text, forcedErr } = unwrapErr(res.content)
    const err = res.isError || forcedErr
    const tree = text.replace(/\n$/, '')
    const lines = tree.split('\n')
    if (err) {
      body = <pre className="ls-raw ls-raw--err">{text || ' '}</pre>
    } else {
      stat = <span className="ls-stat">{lines.length} 行</span>
      body = (
        <Collapsible lines={14} count={lines.length} fade>
          <pre className="ls-tree">{tree || ' '}</pre>
        </Collapsible>
      )
    }
  }

  return (
    <BlockShell
      icon="▤"
      title="LS"
      brief={path}
      accent="exec"
      status={status}
      defaultOpen={false}
      headerExtra={stat}
    >
      {body}
    </BlockShell>
  )
}
