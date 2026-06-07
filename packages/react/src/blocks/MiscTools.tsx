// 批次4 · 杂项工具族:此前落到 GenericCard / 结构化兜底的几类「真·Claude Code 原生工具」补齐专属富卡。
//   · SlashCommand   —— Claude 主动调用斜杠命令(/review、/compact …):命令药丸 + 参数 + 输出。
//   · NotebookRead   —— 读 .ipynb:与 NotebookEdit 同族(📓 橙),按单元渲染 code/markdown。
//   · KillShell      —— 终止后台 shell:复用 ExecTools.KillShell(本文件只做 router 接线点说明,不重复实现)。
// 复用基座 shell.tsx(BlockShell/Collapsible/ResultPane/CodeCanvas/useToolStatus)与 components.MD;
// 视觉沿用既有族色 + 既有 className 习惯,仅追加极薄的 .mx-(杂项)/ .nbr-(notebook read)钩子。
// CSS 前缀:mx-(SlashCommand) nbr-(NotebookRead)。
import { useState, type ReactNode } from 'react'
import { BlockShell, ResultPane, CodeCanvas, useToolStatus } from './shell'
import { MD } from '../components'
import type { Block } from '../types'

// ── 入参取值小工具(与同族卡复用同一风格)──
function asInput(block: Block): Record<string, unknown> {
  return (block.input || {}) as Record<string, unknown>
}
function str(input: Record<string, unknown>, k: string): string {
  const v = input[k]
  return typeof v === 'string' ? v : ''
}

// ── 卡内小节:带标签的二级折叠段(与 MetaTools/StructuredResultCard 同款,自包含以免跨文件耦合私有件)。 ──
function Section({ label, defaultOpen, children }: { label: string; defaultOpen: boolean; children: ReactNode }) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <div className="mx-sec">
      <button className="mx-sec-h" onClick={() => setOpen((o) => !o)}>
        <span className="mx-sec-chev">{open ? '▾' : '▸'}</span>
        {label}
      </button>
      {open && <div className="mx-sec-body">{children}</div>}
    </div>
  )
}

// ───────────────────────── SlashCommand ─────────────────────────
// Claude 主动发起的斜杠命令调用(区别于「用户手敲斜杠」——那走 SystemNotice 的 command 类)。
// 入参形态(实测):{ command: "/review", ... } 或 { command: "review", args?: "..." };
//   有的实现把整条命令塞 command(含参数),有的拆 command + args。两路都兼容。
// 视觉:exec 青族(命令 = 一种「执行」);头部命令药丸 .mx-cmd;参数 kv;结果走 ResultPane md。
export function SlashCommandCard({ block }: { block: Block }) {
  const input = asInput(block)
  const { status, res } = useToolStatus(block)
  // 命令名:command / name / slash 任一;统一补上前导 '/'(去重已有的)。
  const rawCmd = str(input, 'command') || str(input, 'name') || str(input, 'slash') || ''
  // 命令体与参数:command 串里第一个空白前为命令名,其后为内联参数(若 args 字段缺省)。
  const sp = rawCmd.search(/\s/)
  const cmdName = sp > 0 ? rawCmd.slice(0, sp) : rawCmd
  const inlineArgs = sp > 0 ? rawCmd.slice(sp + 1).trim() : ''
  const args = str(input, 'args') || str(input, 'arguments') || inlineArgs
  // 展示用:确保以 '/' 起头(Claude 的 SlashCommand 工具命令通常已带 '/')。
  const display = cmdName ? (cmdName.startsWith('/') ? cmdName : '/' + cmdName) : '/命令'

  const brief = (
    <span className="mx-brief">
      <span className="mx-cmd">{display}</span>
      {args && <span className="mx-args">{args}</span>}
    </span>
  )

  return (
    <BlockShell
      icon="⌘"
      title="斜杠命令"
      brief={brief}
      accent="exec"
      status={status}
      defaultOpen={status === 'err'}
    >
      {/* 参数(若拆出独立字段或内联抓到):等宽块,长则横滚,不溢出。 */}
      {args && (
        <div className="mx-argbox" title={args}>
          {args}
        </div>
      )}
      {/* 命令产出:多为 Markdown(命令的本地输出)。 */}
      {res && res.content && (
        <Section label="产出" defaultOpen={true}>
          <ResultPane content={res.content} isError={res.isError} variant="md" />
        </Section>
      )}
    </BlockShell>
  )
}

// ───────────────────────── NotebookRead ─────────────────────────
// 读 Jupyter notebook(.ipynb)。入参:{ notebook_path | file_path, cell_id? }。
// 结果形态多样(各 SDK 不一):或为整本 JSON、或为「单元拼接文本」。本卡尽力按单元结构化:
//   ① 结果是 JSON 且含 cells[] → 逐单元渲染(code→CodeCanvas python / markdown→MD);
//   ② 否则 → 整段 ResultPane mono(原样,绝不臆造)。
// 视觉:与 NotebookEdit 同族(📓 橙 file 族);头部 brief = basename + 可选 cell 计数。
interface NbCell {
  cell_type?: string
  source?: string | string[]
}
function cellsFromResult(content: string): NbCell[] | null {
  const s = (content || '').trim()
  if (!s || (s[0] !== '{' && s[0] !== '[')) return null
  try {
    const j = JSON.parse(s)
    // 形态 A:{ cells: [...] }(标准 .ipynb)。
    if (j && typeof j === 'object' && Array.isArray((j as Record<string, unknown>).cells)) {
      return (j as { cells: NbCell[] }).cells
    }
    // 形态 B:直接是单元数组。
    if (Array.isArray(j) && j.every((c) => c && typeof c === 'object')) {
      return j as NbCell[]
    }
    return null
  } catch {
    return null
  }
}
// .ipynb 的 source 可能是字符串或字符串数组(逐行);归一成单串。
function cellSource(c: NbCell): string {
  const s = c.source
  if (Array.isArray(s)) return s.join('')
  return typeof s === 'string' ? s : ''
}

export function NotebookReadCard({ block }: { block: Block }) {
  const input = asInput(block)
  const { status, res } = useToolStatus(block)
  const path = str(input, 'notebook_path') || str(input, 'file_path') || str(input, 'path')
  const base = (path || '').split('/').pop() || path || 'notebook'
  const cellId = str(input, 'cell_id')

  const cells = res ? cellsFromResult(res.content) : null
  const brief = (
    <span className="nbr-brief">
      <span className="nbr-base">{base}</span>
      {cells ? (
        <span className="pill nbr-cnt">{cells.length} 单元</span>
      ) : cellId ? (
        <span className="pill nbr-cnt">{cellId}</span>
      ) : null}
    </span>
  )

  return (
    <BlockShell
      icon="📓"
      title="读 Notebook"
      brief={brief}
      accent="file"
      status={status}
      defaultOpen={false}
    >
      {cells ? (
        <div className="nbr-cells">
          {cells.map((c, i) => {
            const ct = (c.cell_type || 'code').toLowerCase()
            const src = cellSource(c)
            return (
              <div className="nbr-cell" key={i}>
                <div className="nbr-cell-h">
                  <span className={'pill nbr-ct nbr-ct--' + (ct === 'markdown' ? 'md' : 'code')}>
                    {ct === 'markdown' ? 'md' : 'code'}
                  </span>
                  <span className="nbr-cell-n">#{i + 1}</span>
                </div>
                {ct === 'markdown' ? (
                  <div className="nbr-md">
                    <MD text={src} />
                  </div>
                ) : (
                  <CodeCanvas code={src || ' '} lang="python" startLine={1} />
                )}
              </div>
            )
          })}
        </div>
      ) : (
        // 解析不出单元结构 → 原样 mono(绝不臆造 notebook 结构)。
        res && res.content ? (
          <ResultPane content={res.content} isError={res.isError} variant="mono" />
        ) : (
          status !== 'running' && <div className="nbr-empty">无内容</div>
        )
      )}
      {/* 仅读取了某个单元:把入参 cell_id 作脚注线索(结果未结构化时尤为有用)。 */}
      {!cells && cellId && <div className="nbr-foot">单元 · {cellId}</div>}
    </BlockShell>
  )
}
