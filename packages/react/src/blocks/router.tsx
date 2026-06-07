// 批次3 · 路由接线:tool_use 工具名 → 专属卡;thinking → 思考块。消息渲染层只调 renderBlock。
// 精确 name 命中 TOOL_ROUTER;mcp__ 前缀 → McpCard;其余兜底 GenericCard。
// 另:renderItems 是「子时间线」统一渲染器(AgentCard 展开后递归用),透传 depth。
import type { ReactNode } from 'react'
import type { Block, Item } from '../types'
import { ThinkingBlock, UserBubble, AssistantBody } from './TextShell'
import { SystemNotice, classifyUserItem } from './SystemNotice'
import { ImageChip, ImageUuidProvider } from './ImageBlock'
import { ReadCard, WriteCard, EditCard, MultiEditCard, NotebookEditCard } from './FileTools'
import { BashCard, BashOutput, KillShell } from './ExecTools'
import { GrepCard, GlobCard, LsCard } from './SearchTools'
import { TodoCard, AgentCard, PlanCard, AskUserQuestionCard, SkillCard, McpCard, GenericCard } from './MetaTools'
import { WebFetchCard, WebSearchCard } from './WebTools'
import { SlashCommandCard, NotebookReadCard } from './MiscTools'
import { WorkflowCard } from './WorkflowCard'
// MCP 资源富卡:两个一方「资源」工具(无 mcp__ 前缀,故 McpCard 不接;此前落 StructuredResultCard/GenericCard)。
import { McpResourceCard } from './McpResourceCard'
// 结构化结果富兜底:GenericCard 之前先试 tryStructuredResult(命中 result/input 的结构化形态
// → 表/状态/列表富渲染;否则卡内就地回落 GenericCard,故 router 行为对未结构化工具不变)。
import { tryStructuredResult } from './StructuredResultCard'

// 工具卡组件签名:统一收 { block } + 可选 depth(AgentCard 据此决定是否还能继续递归子代理)。
type ToolCardFn = (props: { block: Block; depth?: number }) => ReactNode

// 精确工具名 → 专属卡。Task 与 Agent 同→AgentCard。
export const TOOL_ROUTER: Record<string, ToolCardFn> = {
  Read: ReadCard,
  Write: WriteCard,
  Edit: EditCard,
  MultiEdit: MultiEditCard,
  NotebookEdit: NotebookEditCard,
  NotebookRead: NotebookReadCard,
  Bash: BashCard,
  BashOutput: BashOutput,
  // 终止后台 shell:复用 ExecTools.KillShell(✕ 标题的 Bash 卡)。两个历史命名都接住。
  KillShell: KillShell,
  KillBash: KillShell,
  Grep: GrepCard,
  Glob: GlobCard,
  LS: LsCard,
  TodoWrite: TodoCard,
  Task: AgentCard,
  Agent: AgentCard,
  ExitPlanMode: PlanCard,
  AskUserQuestion: AskUserQuestionCard,
  Skill: SkillCard,
  // Claude 主动调用斜杠命令(/review、/compact …);与「用户手敲斜杠」(SystemNotice)区分。
  SlashCommand: SlashCommandCard,
  WebFetch: WebFetchCard,
  WebSearch: WebSearchCard,
  Workflow: WorkflowCard,
  // MCP 资源工具:list(列资源)/ read(读单个资源)同→McpResourceCard;名字无 mcp__ 前缀,精确命中先于 mcp__ 分支。
  // 注册带/不带 Tool 后缀两套别名,任一命名都接住(schema 实名为 Resources 复数 / Resource 单数)。
  ListMcpResourcesTool: McpResourceCard,
  ReadMcpResourceTool: McpResourceCard,
  ListMcpResources: McpResourceCard, // 防御性无后缀别名
  ReadMcpResource: McpResourceCard, // 防御性无后缀别名
}

// tool_use → 卡:精确命中优先 → mcp__ 前缀 McpCard → GenericCard 兜底。depth 透传给 AgentCard。
export function renderToolUse(block: Block, depth = 0): ReactNode {
  const name = block.name || ''
  const exact = TOOL_ROUTER[name]
  if (exact) return exact({ block, depth })
  if (name.startsWith('mcp__')) return <McpCard block={block} />
  // 兜底:先试结构化富卡;命中不了由其内部回落 GenericCard(?? 退化为无害恒真,见 StructuredResultCard)。
  const rich = tryStructuredResult(block)
  return rich ?? <GenericCard block={block} />
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
