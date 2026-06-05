// 批次1 · 文件族最重资产。导出 FileHeader(文件头/面包屑)与 DiffCanvas(行级 diff 画布)。
// 依赖批次0:meta.ts(fileIcon)。CSS 前缀:fh-(文件头) / dc-(diff 画布)。
// DiffCanvas.lang 由调用方传(通常 langOf(path) 取自 meta.ts)。
// 不复用现有 .diff/.d-add(迁移期保留);所有类带 fh-/dc- 前缀。
import { useEffect, useState, type ReactNode } from 'react'
import { diffLines, diffWords, type Change } from 'diff'
import { fileIcon } from './meta'
import { highlighter, LANG_SET } from '../highlight'
import type { PatchHunk } from '../types'

// ── 复制 + 短暂 toast 钩子(自绘,不用原生 alert)──
function useCopyToast(): [boolean, (text: string) => void] {
  const [toast, setToast] = useState(false)
  const copy = (text: string) => {
    navigator.clipboard?.writeText(text).then(
      () => {
        setToast(true)
        setTimeout(() => setToast(false), 1400)
      },
      () => {},
    )
  }
  return [toast, copy]
}

// ── 文件头:图标 + 面包屑(父目录 dim / basename 粗,窄屏中段省略)+ ftag + 右侧 stat ──
// 点路径整体 = 复制完整路径 + toast。
export interface FileHeaderProps {
  path: string
  ftag?: ReactNode // 文件标签(如「新建」「+12 -3」),贴在面包屑后
  ftagColor?: string // ftag 文本/边框色,默认 mut
  stat?: ReactNode // 右侧统计(行数/大小等)
}
export function FileHeader({ path, ftag, ftagColor, stat }: FileHeaderProps) {
  const [toast, copy] = useCopyToast()
  const { glyph, color } = fileIcon(path)
  const { lead, dir, base } = splitPath(path)

  return (
    <div className="fh">
      <span className="fh-icon" style={{ color }}>
        {glyph}
      </span>
      <span className="fh-crumb" onClick={() => copy(path)} role="button" title={path}>
        {lead && <span className="fh-lead">{lead}</span>}
        {dir && <span className="fh-dir">{dir}</span>}
        {/* 分隔斜杠常驻于 base 段(不可省略),避免 dir 的 ellipsis/RTL 吃掉它或与 lead 拼出双斜杠 */}
        <span className="fh-base">
          {dir && <span className="fh-sep">/</span>}
          {base}
        </span>
      </span>
      {ftag != null && ftag !== '' && (
        <span className="fh-tag" style={ftagColor ? { color: ftagColor, borderColor: ftagColor } : undefined}>
          {ftag}
        </span>
      )}
      {stat != null && stat !== '' && <span className="fh-stat">{stat}</span>}
      {toast && <span className="fh-toast">已复制路径</span>}
    </div>
  )
}

// 拆路径成三段:lead(~/ 或 / 前缀,始终可见)、dir(中段父目录,窄屏 CSS 省略)、base(文件名,粗、始终可见)。
interface PathParts {
  lead: string
  dir: string
  base: string
}
function splitPath(path: string): PathParts {
  const p = path || ''
  const base = p.split('/').pop() || p
  let rest = p.slice(0, p.length - base.length) // 含尾随 '/'
  let lead = ''
  // 前导根:~/ 或 / 始终保留在左,中段才是可省略的父目录。
  if (rest.startsWith('~/')) {
    lead = '~/'
    rest = rest.slice(2)
  } else if (rest.startsWith('/')) {
    lead = '/'
    rest = rest.slice(1)
  }
  // dir 去掉尾随斜杠:分隔符由 base 段的 .fh-sep 常驻渲染,dir 自身只剩纯父目录文本,
  // 这样 .fh-dir 的 ellipsis/RTL 不会吞掉分隔斜杠,也不会与 lead 拼出双斜杠。
  rest = rest.replace(/\/+$/, '')
  return { lead, dir: rest, base }
}

// ── Diff 画布:diffLines → 双槽(行号 | +/-/空 | 文本);新增/上下文走 shiki,删除红底。 ──
// 连续未变行 > 6 折成「⋯ N 行未变」可展开;仅当单行替换(old/new 各一行)时词级高亮。
export interface DiffCanvasProps {
  oldStr: string
  newStr: string
  lang?: string // 直接给 shiki lang;省略则不高亮(调用方可用 langOf(path) 传入)
  startLine?: number // 起始行号(默认 1);Edit 通常给原文件中的行号
}

const CONTEXT_FOLD = 6 // 连续未变行超过此值即折叠

// 渲染行的统一描述。
type Sign = '+' | '-' | ' '
interface DiffRow {
  sign: Sign
  text: string
  // 词级 chunk(仅单行替换场景生成);为空走整行高亮/纯文本。
  words?: Array<{ text: string; on: boolean }>
}

export function DiffCanvas({ oldStr, newStr, lang, startLine = 1 }: DiffCanvasProps) {
  const o = oldStr || ''
  const n = newStr || ''
  const singleLine = isSingleLine(o) && isSingleLine(n) && o !== n
  const rows = singleLine ? wordRows(o, n) : lineRows(o, n)

  // 异步整段高亮:把 +/上下文 的可见文本拼成一段送 shiki,再按行回填(删除行不高亮)。
  const known = !!lang && LANG_SET.has(lang)
  const hiSource = rows
    .filter((r) => r.sign !== '-')
    .map((r) => r.text)
    .join('\n')
  const [hi, setHi] = useState<{ src: string; lang: string; lines: string[] } | null>(null)
  useEffect(() => {
    if (!known || !hiSource) return
    let alive = true
    highlighter()
      .then((h) => {
        if (!alive) return
        const html = h.codeToHtml(hiSource, { lang: lang!, theme: 'github-dark' })
        setHi({ src: hiSource, lang: lang!, lines: extractLines(html) })
      })
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [hiSource, lang, known])
  const hiOk = hi && hi.src === hiSource && hi.lang === lang
  // 高亮行游标:仅在非删除行上前进(与 hiSource 拼接顺序一致)。
  let hiIdx = -1

  return (
    <div className="dc">
      <pre className="dc-pre">
        <FoldedRows
          rows={rows}
          startLine={startLine}
          renderText={(r) => {
            if (r.words) {
              // 词级:逐 chunk;按 sign 决定哪些 chunk 高亮(+行高亮新增、-行高亮删除)。
              return (
                <span className="dc-txt">
                  {r.words.map((w, i) => (
                    <span key={i} className={w.on ? (r.sign === '+' ? 'dc-w-add' : 'dc-w-del') : undefined}>
                      {w.text}
                    </span>
                  ))}
                </span>
              )
            }
            if (r.sign !== '-') {
              hiIdx++
              const tok = hiOk ? hi!.lines[hiIdx] : undefined
              if (tok != null)
                return <span className="dc-txt" dangerouslySetInnerHTML={{ __html: tok === '' ? ' ' : tok }} />
            }
            return <span className="dc-txt">{r.text === '' ? ' ' : r.text}</span>
          }}
        />
      </pre>
    </div>
  )
}

// 行号会随折叠跳变,所以折叠/计号逻辑独立成组件:遍历 rows,把连续未变行(> CONTEXT_FOLD)折成可展开的「⋯」。
interface FoldedRowsProps {
  rows: DiffRow[]
  startLine: number
  renderText: (r: DiffRow) => ReactNode
}
function FoldedRows({ rows, startLine, renderText }: FoldedRowsProps) {
  // 双侧行号:旧文件号(随 - 与上下文前进)/新文件号(随 + 与上下文前进)。展示统一取「新号」(无新号则旧号)。
  // 先扫一遍给每行算出展示行号,再做连续未变折叠。
  let oldNo = startLine
  let newNo = startLine
  const numbered = rows.map((r) => {
    let no: number
    if (r.sign === '+') {
      no = newNo++
    } else if (r.sign === '-') {
      no = oldNo++
    } else {
      no = newNo
      oldNo++
      newNo++
    }
    return { r, no }
  })

  // 折叠:把连续的未变行(sign===' ')分段;段长 > CONTEXT_FOLD 时,只有「中间」可折叠(保留首尾各 2 行做上下文)。
  const out: ReactNode[] = []
  let i = 0
  let key = 0
  while (i < numbered.length) {
    if (numbered[i].r.sign === ' ') {
      let j = i
      while (j < numbered.length && numbered[j].r.sign === ' ') j++
      const run = numbered.slice(i, j)
      if (run.length > CONTEXT_FOLD) {
        const head = run.slice(0, 2)
        const tail = run.slice(run.length - 2)
        const hidden = run.length - 4
        head.forEach((x) => out.push(<Line key={key++} sign={x.r.sign} no={x.no} body={renderText(x.r)} />))
        out.push(<FoldRow key={key++} count={hidden} rows={run.slice(2, run.length - 2)} renderText={renderText} />)
        tail.forEach((x) => out.push(<Line key={key++} sign={x.r.sign} no={x.no} body={renderText(x.r)} />))
      } else {
        run.forEach((x) => out.push(<Line key={key++} sign={x.r.sign} no={x.no} body={renderText(x.r)} />))
      }
      i = j
    } else {
      const x = numbered[i]
      out.push(<Line key={key++} sign={x.r.sign} no={x.no} body={renderText(x.r)} />)
      i++
    }
  }
  return <>{out}</>
}

// 折叠占位行:点击展开藏起的未变行。
interface FoldRowProps {
  count: number
  rows: Array<{ r: DiffRow; no: number }>
  renderText: (r: DiffRow) => ReactNode
}
function FoldRow({ count, rows, renderText }: FoldRowProps) {
  const [open, setOpen] = useState(false)
  if (open)
    return (
      <>
        {rows.map((x, i) => (
          <Line key={i} sign={x.r.sign} no={x.no} body={renderText(x.r)} />
        ))}
      </>
    )
  return (
    <div className="dc-fold" onClick={() => setOpen(true)} role="button">
      <span className="dc-fold-i">⋯</span> {count} 行未变
    </div>
  )
}

// 单行:行号槽 | 符号槽 | 文本。
function Line({ sign, no, body }: { sign: Sign; no: number; body: ReactNode }) {
  const cls = 'dc-row' + (sign === '+' ? ' dc-add' : sign === '-' ? ' dc-del' : '')
  return (
    <div className={cls}>
      <span className="dc-no">{no}</span>
      <span className="dc-sign">{sign}</span>
      {body}
    </div>
  )
}

// ── diff 计算 ──

function isSingleLine(s: string): boolean {
  return s.replace(/\n$/, '').indexOf('\n') < 0
}

// 行级:diffLines → 逐行 DiffRow。
function lineRows(oldStr: string, newStr: string): DiffRow[] {
  const parts = diffLines(oldStr, newStr)
  const rows: DiffRow[] = []
  for (const p of parts) {
    const sign: Sign = p.added ? '+' : p.removed ? '-' : ' '
    const lines = p.value.replace(/\n$/, '').split('\n')
    for (const ln of lines) rows.push({ sign, text: ln })
  }
  return rows
}

// 单行替换:diffWords 切词,产出两行(- 旧 / + 新),各自标出变化 chunk。
function wordRows(oldStr: string, newStr: string): DiffRow[] {
  const o = oldStr.replace(/\n$/, '')
  const n = newStr.replace(/\n$/, '')
  const parts: Change[] = diffWords(o, n)
  // 删除行:保留「未变 + 删除」chunk;新增行:保留「未变 + 新增」chunk。on 标变化态。
  const delWords = parts.filter((p) => !p.added).map((p) => ({ text: p.value, on: !!p.removed }))
  const addWords = parts.filter((p) => !p.removed).map((p) => ({ text: p.value, on: !!p.added }))
  return [
    { sign: '-', text: o, words: delWords },
    { sign: '+', text: n, words: addWords },
  ]
}

// shiki <pre><code> 拆逐行 innerHTML(与 shell.tsx 的 extractLines 同构,各自私有避免跨文件耦合)。
function extractLines(html: string): string[] {
  const codeMatch = html.match(/<code[^>]*>([\s\S]*?)<\/code>/)
  const inner = codeMatch ? codeMatch[1] : html
  const lineRe = /<span class="line"[^>]*>([\s\S]*?)<\/span>(?=\n|$|<span class="line")/g
  const out: string[] = []
  let m: RegExpExecArray | null
  while ((m = lineRe.exec(inner)) !== null) out.push(m[1])
  if (out.length === 0) return inner.split('\n')
  return out
}

// ── Patch 画布:直接吃 Edit/MultiEdit 的 structuredPatch(含上下文行,与 TUI 同源)。 ──
// 取代 DiffCanvas(diffLines(old,new) 只有改动行、几乎无上下文)。每个 hunk 按 oldStart/newStart 给真实行号;
// lines 里 ' ' 上下文=普通行、'-'=红删、'+'=绿增;多 hunk 间插「⋯」分隔。复用 .dc-* 行号/红绿样式 + shiki。
export interface PatchCanvasProps {
  patch: PatchHunk[]
  lang?: string // shiki lang(通常 langOf(path));省略不高亮
}

// 一个 hunk 摊平成带真实行号的渲染行。
interface PatchRow {
  sign: Sign
  text: string
  no: number // 展示行号:+/上下文取新号,-取旧号
}

// 把 structuredPatch 摊平成渲染行(逐 hunk 按 old/new 起始号推进)。
function flattenHunks(patch: PatchHunk[]): PatchRow[][] {
  return patch.map((h) => {
    let oldNo = h.oldStart || 1
    let newNo = h.newStart || 1
    const rows: PatchRow[] = []
    for (const raw of h.lines || []) {
      const c = raw.charAt(0)
      const text = raw.slice(1)
      if (c === '+') {
        rows.push({ sign: '+', text, no: newNo++ })
      } else if (c === '-') {
        rows.push({ sign: '-', text, no: oldNo++ })
      } else {
        rows.push({ sign: ' ', text, no: newNo })
        oldNo++
        newNo++
      }
    }
    return rows
  })
}

export function PatchCanvas({ patch, lang }: PatchCanvasProps) {
  const hunks = flattenHunks(patch || [])

  // 整段 shiki:把所有 hunk 的「非删除行」拼成一段送 shiki,再按序回填(删除行不高亮,走红底)。
  const known = !!lang && LANG_SET.has(lang)
  const hiSource = hunks
    .flat()
    .filter((r) => r.sign !== '-')
    .map((r) => r.text)
    .join('\n')
  const [hi, setHi] = useState<{ src: string; lang: string; lines: string[] } | null>(null)
  useEffect(() => {
    if (!known || !hiSource) return
    let alive = true
    highlighter()
      .then((h) => {
        if (!alive) return
        const html = h.codeToHtml(hiSource, { lang: lang!, theme: 'github-dark' })
        setHi({ src: hiSource, lang: lang!, lines: extractLines(html) })
      })
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [hiSource, lang, known])
  const hiOk = hi && hi.src === hiSource && hi.lang === lang
  let hiIdx = -1 // 非删除行游标(与 hiSource 拼接顺序一致)

  const renderText = (r: PatchRow): ReactNode => {
    if (r.sign !== '-') {
      hiIdx++
      const tok = hiOk ? hi!.lines[hiIdx] : undefined
      if (tok != null)
        return <span className="dc-txt" dangerouslySetInnerHTML={{ __html: tok === '' ? ' ' : tok }} />
    }
    return <span className="dc-txt">{r.text === '' ? ' ' : r.text}</span>
  }

  return (
    <div className="dc">
      <pre className="dc-pre">
        {hunks.map((rows, hi) => (
          <div key={hi}>
            {/* 多 hunk 之间留分隔:跳过的上下文(省略号 + 起始行号)*/}
            {hi > 0 && (
              <div className="dc-fold dc-hunk-gap">
                <span className="dc-fold-i">⋯</span> @@ {rows.length ? rows[0].no : ''}
              </div>
            )}
            {rows.map((r, i) => (
              <Line key={i} sign={r.sign} no={r.no} body={renderText(r)} />
            ))}
          </div>
        ))}
      </pre>
    </div>
  )
}
