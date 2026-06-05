// 批次3 · 路由接线:tool_use 工具名 → 专属卡;thinking → 思考块。消息渲染层只调 renderBlock。
// 精确 name 命中 TOOL_ROUTER;mcp__ 前缀 → McpCard;其余兜底 GenericCard。
// 另:renderItems 是「子时间线」统一渲染器(AgentCard 展开后递归用),透传 depth。
import type { ReactNode } from 'react'
import type { Block, Item } from '../types'
import { ThinkingBlock, UserBubble, AssistantBody } from './TextShell'
import { SystemNotice, classifyUserItem } from './SystemNotice'
import { ImageChip, ImageUuidProvider } from './ImageBlock'
import { ReadCard, WriteCard, EditCard, MultiEditCard, NotebookEditCard } from './FileTools'
import { BashCard, BashOutput } from './ExecTools'
import { GrepCard, GlobCard, LsCard } from './SearchTools'
import { TodoCard, AgentCard, PlanCard, AskUserQuestionCard, SkillCard, McpCard, GenericCard } from './MetaTools'
import { WebFetchCard, WebSearchCard } from './WebTools'
import { WorkflowCard } from './WorkflowCard'

// 工具卡组件签名:统一收 { block } + 可选 depth(AgentCard 据此决定是否还能继续递归子代理)。
type ToolCardFn = (props: { block: Block; depth?: number }) => ReactNode

// 精确工具名 → 专属卡。Task 与 Agent 同→AgentCard。
export const TOOL_ROUTER: Record<string, ToolCardFn> = {
  Read: ReadCard,
  Write: WriteCard,
  Edit: EditCard,
  MultiEdit: MultiEditCard,
  NotebookEdit: NotebookEditCard,
  Bash: BashCard,
  BashOutput: BashOutput,
  Grep: GrepCard,
  Glob: GlobCard,
  LS: LsCard,
  TodoWrite: TodoCard,
  Task: AgentCard,
  Agent: AgentCard,
  ExitPlanMode: PlanCard,
  AskUserQuestion: AskUserQuestionCard,
  Skill: SkillCard,
  WebFetch: WebFetchCard,
  WebSearch: WebSearchCard,
  Workflow: WorkflowCard,
}

// tool_use → 卡:精确命中优先 → mcp__ 前缀 McpCard → GenericCard 兜底。depth 透传给 AgentCard。
export function renderToolUse(block: Block, depth = 0): ReactNode {
  const name = block.name || ''
  const exact = TOOL_ROUTER[name]
  if (exact) return exact({ block, depth })
  if (name.startsWith('mcp__')) return <McpCard block={block} />
  return <GenericCard block={block} />
}

// 块级路由:thinking → ThinkingBlock;tool_use → renderToolUse。text 由消息壳自行处理(用户气泡/助手正文)。
// depth:递归深度,经 renderToolUse 透传给 AgentCard(默认 0 = 主时间线)。
export function renderBlock(block: Block, key: number, depth = 0): ReactNode {
  if (block.type === 'thinking') return <ThinkingBlock key={key} text={block.text || ''} />
  if (block.type === 'tool_use') return <span key={key}>{renderToolUse(block, depth)}</span>
  return null
}

// ── 子时间线统一渲染器 ──
// 遍历 Item[](子 jsonl 与主 jsonl 结构同构):
//   真用户文本 → UserBubble;伪用户(命令/通知/系统/中断/图片)→ SystemNotice;
//   assistant 文本 → AssistantBody;其它块(thinking/tool_use)→ renderBlock(透传 depth)。
// 纯 tool_result 载体不单独渲染(结果已并入对应工具卡)。
// AgentCard 展开后调本函数渲染子代理事件;depth 透传以约束继续递归(蓝图:depth 上限 2)。
export function renderItems(items: Item[], depth: number): ReactNode {
  return items.map((it, i) => {
    const blocks = it.blocks || []
    if (it.role === 'user') {
      const cls = classifyUserItem(it)
      if (cls.kind === 'other') return null
      if (cls.kind !== 'user') return <SystemNotice key={i} cls={cls} />
      // 只渲染 base64 真图(无 path);路径式图块是 isMeta 副本,跳过(见 classifyUserItem)。
      const imgs = blocks.filter((b) => b.type === 'image' && !b.path)
      return (
        <div key={i}>
          {cls.userText.trim() && <UserBubble text={cls.userText} />}
          {imgs.length > 0 && (
            <ImageUuidProvider value={it.uuid || ''}>
              <div className="ic-row">
                {imgs.map((b, bi) => (
                  <ImageChip key={bi} block={b} />
                ))}
              </div>
            </ImageUuidProvider>
          )}
        </div>
      )
    }
    // assistant:逐块渲染(text → AssistantBody;其余 → renderBlock 透传 depth)。
    return (
      <div key={i}>
        {blocks.map((b, bi) =>
          b.type === 'text' ? <AssistantBody key={bi} text={b.text || ''} /> : renderBlock(b, bi, depth),
        )}
      </div>
    )
  })
}
