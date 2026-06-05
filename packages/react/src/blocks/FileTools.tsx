// 批次2 · 文件族工具卡。每张卡 = BlockShell 包专属正文,meta 从 TOOL_META 取,结果与三态由 useToolStatus 拿。
// 依赖批次0 基座(shell.tsx / meta.ts)+ 批次1(FileHeader / DiffCanvas)。
// 新增本族类一律 ft- 前缀;文件头沿用 fh-、diff 沿用 dc-,不另造。
import type { ReactNode } from 'react'
import { BlockShell, CodeCanvas, useToolStatus } from './shell'
import { TOOL_META, langOf } from './meta'
import { FileHeader, DiffCanvas, PatchCanvas } from './FileHeader'
import { MD } from '../components'
import type { Block, PatchHunk } from '../types'

// 取该工具结果里的 structuredPatch(后端透传);非空数组才返回,否则 undefined(回退 DiffCanvas)。
function patchOf(res?: { patch?: PatchHunk[] }): PatchHunk[] | undefined {
  const p = res?.patch
  return Array.isArray(p) && p.length > 0 ? p : undefined
}

// 工具入参取值小工具:string / array / number 安全读取。
function str(input: Record<string, unknown> | undefined, k: string): string {
  const v = input?.[k]
  return typeof v === 'string' ? v : ''
}
function bool(input: Record<string, unknown> | undefined, k: string): boolean {
  return input?.[k] === true
}

// ── 颜色常量(对齐 meta.ts fileIcon 与 CSS 变量族色)──
const GREEN = '#3ddc84'
const ORANGE = '#ff9f6b'

// ── 折叠态一行 brief:basename 强调(灰目录 + 亮文件名)+ ± 统计。塞进 BlockShell 头部副位。 ──
// 紧凑头里不放完整面包屑/复制按钮(展开态的 TUI 标题行才给完整路径与复制),折叠只露关键信息。
function FileBrief({ path, stat }: { path: string; stat?: ReactNode }) {
  const base = (path || '').split('/').pop() || path || ''
  const dir = path && base ? path.slice(0, path.length - base.length).replace(/\/+$/, '') : ''
  return (
    <span className="ftb">
      {dir && <span className="ftb-dir">{dir}/</span>}
      <span className="ftb-base">{base}</span>
      {stat != null && stat !== '' && <span className="ftb-stat">{stat}</span>}
    </span>
  )
}

// ── 展开态 TUI 式标题行:`● Verb(basename) · 统计`(贴 claude TUI)。点 basename 复制完整路径。 ──
function TuiHead({ verb, path, stat, dot = GREEN }: { verb: string; path: string; stat?: ReactNode; dot?: string }) {
  const base = (path || '').split('/').pop() || path || ''
  const copy = () => navigator.clipboard?.writeText(path)
  return (
    <div className="ftt">
      <span className="ftt-dot" style={{ color: dot }}>
        ●
      </span>
      <span className="ftt-verb">{verb}</span>
      <span className="ftt-paren">(</span>
      <span className="ftt-base" onClick={copy} role="button" title={path}>
        {base}
      </span>
      <span className="ftt-paren">)</span>
      {stat != null && stat !== '' && (
        <>
          <span className="ftt-sep">·</span>
          <span className="ftt-stat">{stat}</span>
        </>
      )}
    </div>
  )
}

// ───────────────────────── Read ─────────────────────────
// 👁 默认展开;正文解析 cat -n 真实行号 + shiki;过滤 system-reminder 段并脚注;长内容由 CodeCanvas 限高盒承担。
export function ReadCard({ block }: { block: Block }) {
  const meta = TOOL_META.Read
  const { status, res } = useToolStatus(block)
  const path = str(block.input, 'file_path') || str(block.input, 'path')
  const lang = langOf(path)

  // 结果体是 `cat -n` 形态(行号 + tab/空格 + 文本)。解析出起始行号与纯代码;并剥离 system-reminder 段。
  const parsed = res ? parseCatN(res.content) : null
  const stat = parsed ? readStat(parsed) : ''

  return (
    <BlockShell
      icon={meta.icon}
      title={meta.title}
      accent="file"
      status={status}
      defaultOpen={meta.defaultOpen}
    >
      <FileHeader path={path} ftag="👁" stat={stat} />
      {parsed ? (
        <>
          <CodeCanvas code={parsed.code} lang={lang} startLine={parsed.startLine} />
          {parsed.reminderTrimmed && (
            <div className="ft-foot">已折叠 system-reminder 段</div>
          )}
        </>
      ) : (
        status !== 'running' && <div className="ft-foot">无内容</div>
      )}
    </BlockShell>
  )
}

// ───────────────────────── Write ─────────────────────────
// 折叠:头部「Create/Write · basename · +N」一行。展开:TUI 标题 + 文件全文(CodeCanvas 行号 1..N,全增视觉)。
export function WriteCard({ block }: { block: Block }) {
  const meta = TOOL_META.Write
  const { status } = useToolStatus(block)
  const path = str(block.input, 'file_path') || str(block.input, 'path')
  const content = str(block.input, 'content')
  const lang = langOf(path)
  const n = countLines(content)
  // 新建文件用 Create(更贴 TUI),已有则 Write;无从判断时一律 Create(Write 工具多用于新建/全量覆盖)。
  const verb = 'Create'

  return (
    <BlockShell
      icon={meta.icon}
      title={verb}
      accent="file"
      status={status}
      defaultOpen={meta.defaultOpen}
      brief={<FileBrief path={path} stat={`+${n}`} />}
    >
      <TuiHead verb={verb} path={path} stat={`+${n}`} dot={GREEN} />
      <CodeCanvas code={content} lang={lang} startLine={1} />
    </BlockShell>
  )
}

// ───────────────────────── Edit ─────────────────────────
// 折叠:头部「Edit · basename · +A −B」一行(replace_all 追加角标)。展开:TUI 标题 + DiffCanvas(行号红绿,始终可见)。
export function EditCard({ block }: { block: Block }) {
  const meta = TOOL_META.Edit
  const { status, res } = useToolStatus(block)
  const path = str(block.input, 'file_path') || str(block.input, 'path')
  const oldStr = str(block.input, 'old_string')
  const newStr = str(block.input, 'new_string')
  const all = bool(block.input, 'replace_all')
  const lang = langOf(path)
  const stat = editStat(oldStr, newStr)
  const verb = all ? 'Edit all' : 'Edit'
  // 优先用后端透传的 structuredPatch(含上下文行,与 TUI 同源);没有再回退 DiffCanvas(old/new 小片段)。
  const patch = patchOf(res)

  return (
    <BlockShell
      icon={meta.icon}
      title={verb}
      accent="file"
      status={status}
      defaultOpen={meta.defaultOpen}
      brief={<FileBrief path={path} stat={stat} />}
    >
      <TuiHead verb={verb} path={path} stat={stat} />
      {patch ? <PatchCanvas patch={patch} lang={lang} /> : <DiffCanvas oldStr={oldStr} newStr={newStr} lang={lang} />}
    </BlockShell>
  )
}

// ───────────────────────── MultiEdit ─────────────────────────
// ✎×N stat=+ΣA −ΣB·N处;edits 逐段 DiffCanvas 带小标题 #1 + 段折叠。
interface EditEntry {
  old_string?: string
  new_string?: string
}
export function MultiEditCard({ block }: { block: Block }) {
  const meta = TOOL_META.MultiEdit
  const { status, res } = useToolStatus(block)
  const path = str(block.input, 'file_path') || str(block.input, 'path')
  const lang = langOf(path)
  const raw = block.input?.edits
  const edits: EditEntry[] = Array.isArray(raw) ? (raw as EditEntry[]) : []
  // structuredPatch 已把全部 edits 合并成多 hunk(各带真实文件行号),优先用它整体渲染;
  // 没有再回退「逐段 DiffCanvas」(每段只有 old/new 片段、无上下文)。
  const patch = patchOf(res)

  // 统计:Σ新增行 / Σ删除行 / 段数。
  let addSum = 0
  let delSum = 0
  for (const e of edits) {
    const o = typeof e.old_string === 'string' ? e.old_string : ''
    const n = typeof e.new_string === 'string' ? e.new_string : ''
    delSum += countLines(o)
    addSum += countLines(n)
  }
  const stat = `+${addSum} −${delSum}`
  const verb = `Edit ×${edits.length}`

  return (
    <BlockShell
      icon={meta.icon}
      title={verb}
      accent="file"
      status={status}
      defaultOpen={meta.defaultOpen}
      brief={<FileBrief path={path} stat={stat} />}
    >
      <TuiHead verb={verb} path={path} stat={`${stat} · ${edits.length}处`} />
      {patch ? (
        <PatchCanvas patch={patch} lang={lang} />
      ) : (
        edits.map((e, i) => <MultiEditSeg key={i} idx={i + 1} edit={e} lang={lang} />)
      )}
    </BlockShell>
  )
}

// 单段:小标题 #N + 段折叠(默认展开,点标题收起)。
function MultiEditSeg({ idx, edit, lang }: { idx: number; edit: EditEntry; lang?: string }) {
  return (
    <details className="ft-seg" open>
      <summary className="ft-seg-h">
        <span className="ft-seg-n">#{idx}</span>
      </summary>
      <DiffCanvas
        oldStr={typeof edit.old_string === 'string' ? edit.old_string : ''}
        newStr={typeof edit.new_string === 'string' ? edit.new_string : ''}
        lang={lang}
      />
    </details>
  )
}

// ───────────────────────── NotebookEdit ─────────────────────────
// 修 bug:统计 / brief 读 notebook_path(而非 file_path)。.ipynb 橙 📓。
// 按 edit_mode:replace→DiffCanvas、insert→绿块、delete→红块;cell_type code→CodeCanvas、markdown→MD。
export function NotebookEditCard({ block }: { block: Block }) {
  const { status } = useToolStatus(block)
  // bug 修复点:notebook_path 优先,file_path 仅兜底。
  const path = str(block.input, 'notebook_path') || str(block.input, 'file_path')
  const mode = (str(block.input, 'edit_mode') || 'replace').toLowerCase()
  const cellType = (str(block.input, 'cell_type') || 'code').toLowerCase()
  const source = str(block.input, 'new_source') || str(block.input, 'source')
  const oldSource = str(block.input, 'old_source')
  const cellId = str(block.input, 'cell_id')

  const verb = modeVerb(mode)
  const stat = cellType + (cellId ? ' · ' + cellId : '')

  return (
    <BlockShell
      icon="📓"
      title={verb}
      accent="file"
      status={status}
      defaultOpen={false}
      brief={<FileBrief path={path} stat={stat} />}
    >
      <TuiHead verb={verb} path={path} stat={stat} dot={ORANGE} />
      <NotebookBody mode={mode} cellType={cellType} source={source} oldSource={oldSource} />
    </BlockShell>
  )
}

// notebook 正文:edit_mode × cell_type 决定渲染。
function NotebookBody({
  mode,
  cellType,
  source,
  oldSource,
}: {
  mode: string
  cellType: string
  source: string
  oldSource: string
}) {
  const isMd = cellType === 'markdown'
  // replace:有旧源走 diff,否则按 cell_type 展示新源。
  if (mode === 'replace') {
    if (oldSource) {
      return <DiffCanvas oldStr={oldSource} newStr={source} lang={isMd ? 'md' : 'python'} />
    }
    return <CellContent cellType={cellType} source={source} />
  }
  // insert:绿框包裹新源。
  if (mode === 'insert') {
    return (
      <div className="ft-nb ft-nb--ins">
        <div className="ft-nb-h">＋ 新增单元</div>
        <CellContent cellType={cellType} source={source} />
      </div>
    )
  }
  // delete:红框,展示被删源(若有)。
  return (
    <div className="ft-nb ft-nb--del">
      <div className="ft-nb-h">－ 删除单元</div>
      {(oldSource || source) && <CellContent cellType={cellType} source={oldSource || source} />}
    </div>
  )
}

// 单元内容:code → CodeCanvas(python 高亮)、markdown → MD。
function CellContent({ cellType, source }: { cellType: string; source: string }) {
  if (cellType === 'markdown') {
    return (
      <div className="ft-nb-md">
        <MD text={source} />
      </div>
    )
  }
  return <CodeCanvas code={source} lang="python" startLine={1} />
}

// ───────────────────────── 工具函数 ─────────────────────────

// 行数:空串算 0;否则去掉尾随换行后按行算。
function countLines(s: string): number {
  if (!s) return 0
  return s.replace(/\n$/, '').split('\n').length
}

// Edit stat:+新增行 −删除行(单行替换给「1 行替换」更直观)。
function editStat(oldStr: string, newStr: string): string {
  const a = countLines(newStr)
  const d = countLines(oldStr)
  return `+${a} −${d}`
}

// Read stat:解析后给「N 行」或「起–止」区间。
interface CatN {
  code: string
  startLine: number
  reminderTrimmed: boolean
}
function readStat(p: CatN): string {
  const n = countLines(p.code)
  if (n === 0) return '空'
  if (p.startLine > 1) return `${p.startLine}–${p.startLine + n - 1}`
  return `${n} 行`
}

// 解析 `cat -n` 结果:每行形如 "   123\t内容"。取首行行号为 startLine,剥掉行号前缀;
// 并过滤 <system-reminder>…</system-reminder> 段(置 reminderTrimmed)。容错:非 cat -n 形态原样返回(startLine=1)。
const SR_RE = /<system-reminder>[\s\S]*?<\/system-reminder>\s*/g
function parseCatN(content: string): CatN {
  let raw = content || ''
  const reminderTrimmed = SR_RE.test(raw)
  SR_RE.lastIndex = 0
  if (reminderTrimmed) raw = raw.replace(SR_RE, '').replace(/\n+$/, '')

  const lines = raw.split('\n')
  // 探测 cat -n:首个非空行需匹配「空白*数字 + 制表/双空格」。
  const head = lines.find((l) => l.trim() !== '')
  const m = head ? head.match(/^\s*(\d+)\t/) : null
  if (!m) {
    return { code: raw, startLine: 1, reminderTrimmed }
  }
  const startLine = parseInt(m[1], 10) || 1
  const code = lines
    .map((l) => {
      const mm = l.match(/^\s*\d+\t(.*)$/)
      return mm ? mm[1] : l
    })
    .join('\n')
  return { code, startLine, reminderTrimmed }
}

// NotebookEdit edit_mode → TUI 动词(头部标题 / TUI 标题行同用)。
function modeVerb(mode: string): string {
  if (mode === 'insert') return 'Insert cell'
  if (mode === 'delete') return 'Delete cell'
  return 'Notebook'
}
