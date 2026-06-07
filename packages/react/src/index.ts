// @ccfly/react — public entry.
//
// React 表世界控件库:把一个 Claude Code 会话(里世界 tmux)渲成可控的 Web 视图。
// 参数化抽包:控制服务端点 / 存储前缀 / tmux 命名等由 CCFlyProvider 注入。
//
// 典型用法:
//   import { CCFlyProvider, SessionView, CCFlyHosts } from '@ccfly/react'
//   import '@ccfly/react/style.css'
//
//   <CCFlyProvider config={{ baseUrl: '/x/mac', sessionsUrl: '/api/claude-sessions' }}>
//     <SessionView sid={sid} />
//     <CCFlyHosts />
//   </CCFlyProvider>
//
// 终端实时镜像走 ccfly 自带的 /term WebSocket(Go 服务里 PTY + tmux,ttyd 帧兼容),不依赖外部 ttyd。
// wsBaseUrl 缺省 = baseUrl(终端与 REST 同前缀);仅当 WS 走另一前缀/主机时才需显式传。

// ── 样式:tsup 把这两份 CSS 合并产出 dist/style.css(消费方 import '@ccfly/react/style.css')。 ──
// 注:xterm 自身 css 由 LiveTerm.tsx 内部 import('@xterm/xterm/css/xterm.css'),也会并入产物。
import './styles.css'
import './blocks/blocks.css'

// ── 顶层 API ──
export { CCFlyProvider, useCCFly } from './CCFlyProvider'
export type { CCFlyProviderConfig } from './CCFlyProvider'
export { CCFlyHosts } from './hosts'
export { SessionView } from './SessionView'
export type { SessionViewProps } from './SessionView'

// ── 配置类型/工具(高级消费方:自定义 fetch、storageKey、读 config)──
export { getConfig, setConfig, storageKey } from './config'
export type { CCFlyConfig } from './config'

// ── 控件层组件(可单独组合)──
export { ControlBar } from './ControlBar'
// 空闲 input 态的快捷 chip 行(模型/力度/压缩/清空);ControlBar 已内嵌,单独导出供自组控件层复用。
export { ComposeChips } from './ComposeChips'
export type { ComposeChipsProps } from './ComposeChips'
// 图片/文件附件条 + 其状态 hook + 预判扫描器;ControlBar 已内嵌,单独导出供自组控件层复用。
// (uploadFile 经下方 `export * from './api'` 已对外暴露,无需在此重复。)
export { AttachmentBar, useAttachments, scanWantsImage } from './AttachmentBar'
export type { Attachment, AttachmentsHandle } from './AttachmentBar'

// ── 富 select 组件(客户端分类后替换通用 select 分支)+ 分类器/统一 props 契约 ──
// 高级消费方可单独复用/组合;selectKind 是分类入口,RichSelectProps/RichSelectHelpers 为统一契约。
export { selectKind } from './select/selectKind'
export type { RichSelectProps, RichSelectHelpers, SelectKind } from './select/selectKind'
export { RichModelSelect, isModelSelect } from './select/RichModelSelect'
export { RichPermissionSelect, isPermissionSelect } from './select/RichPermissionSelect'
export { RichEffortSelect, isEffortSelect } from './select/RichEffortSelect'
export { RichConfirmSelect } from './select/RichConfirmSelect'
export { RichSessionScopeSelect, isSessionScope } from './select/RichSessionScopeSelect'
export { RichMultiSelect, isMultiSelect } from './select/RichMultiSelect'
export { RichListSelect, isListSelect } from './select/RichListSelect'
export { AgentDock } from './AgentDock'
export { LiveTerm } from './LiveTerm'
export { SlashPalette } from './Palette'
// 注:单层视图组件 SubagentView 是内部实现(经 SubagentHost 渲染),不导出。
// 钻入子代理用 openSubagent;在 App 根挂 SubagentHost(或一站式 CCFlyHosts)。
export { SubagentHost, openSubagent } from './SubagentView'
export type { SubagentArgs } from './SubagentView'

// ── 信息卡(/cost /status /mcp /doctor /hooks /skills …:后台驱动命令抓屏 → 解析成原生卡)──
// SessionView 默认已接线(ControlBar 缺省用 registry.isInfoCmd 判定、onRunCmd 开 InfoSheet)。
// 仅当消费方自组控件层、或要扩展/自定义命令表时才需直接用这些导出。
export { InfoSheet } from './info/InfoSheet'
export { CARDS, cardFor, groupOf, isInfoCmd } from './info/registry'
export type { CmdCard } from './info/registry'
export { useCapture, relTime } from './info/useCapture'
export type { Capture, Phase } from './info/useCapture'
export type { CardModule } from './info/types'

// ── 消息流渲染器(主会话 / 子代理共用)──
export { TranscriptView, Message, MD, shortModel } from './components'

// ── blocks 注册表 + 路由 + 单张工具卡 ──
export { TOOL_ROUTER, renderBlock, renderToolUse, renderItems } from './blocks/router'
export {
  ReadCard,
  WriteCard,
  EditCard,
  MultiEditCard,
  NotebookEditCard,
} from './blocks/FileTools'
export { BashCard, BashOutput, KillShell } from './blocks/ExecTools'
export { GrepCard, GlobCard, LsCard } from './blocks/SearchTools'
export {
  TodoCard,
  AgentCard,
  PlanCard,
  AskUserQuestionCard,
  SkillCard,
  McpCard,
  GenericCard,
} from './blocks/MetaTools'
export { WebFetchCard, WebSearchCard } from './blocks/WebTools'
export { SlashCommandCard, NotebookReadCard } from './blocks/MiscTools'
export { StructuredResultCard, tryStructuredResult } from './blocks/StructuredResultCard'
export type { StructuredResultCardProps } from './blocks/StructuredResultCard'
export { WorkflowCard, WorkflowOverlayHost, openWorkflow } from './blocks/WorkflowCard'
export type { WorkflowOverlayArgs } from './blocks/WorkflowCard'

// ── blocks 基座(自定义卡复用:卡壳/折叠/代码画布/结果面板/状态钩子/全屏阅读器)──
export {
  BlockShell,
  Collapsible,
  CodeCanvas,
  ResultPane,
  useToolStatus,
  unwrapErr,
  openReader,
  ReaderHost,
  SubResultContext,
} from './blocks/shell'
export type {
  Accent,
  ToolStatus,
  ToolStatusResult,
  ResultMap,
  BlockShellProps,
  CollapsibleProps,
  CodeCanvasProps,
  ResultPaneProps,
  ResultVariant,
} from './blocks/shell'
export { CodeBlock } from './blocks/CodeBlock'
export { ImageChip, ImageUuidProvider, openLightbox, LightboxHost } from './blocks/ImageBlock'
export { AnsiText, stripAnsi } from './blocks/Ansi'
export { SystemNotice, classifyUserItem } from './blocks/SystemNotice'
export { SessionContext, useSession } from './blocks/ctx'
export type { SessionCtx } from './blocks/ctx'
export { TOOL_META, fileIcon, langOf, briefOf } from './blocks/meta'
export type { ToolMeta, FileIcon } from './blocks/meta'

// ── 状态层(zustand store / live 镜像状态 / 发键)──
export { useStore, itemKey, indexResults } from './store'
export {
  useLiveStore,
  useLiveState,
  useLiveDegraded,
  useLiveCertainInput,
  detectState,
} from './livestate'
export type { DetectResult } from './livestate'
export { sendAct } from './sendkeys'
export type { SendBody, SendResult } from './sendkeys'
export { liveTermHandle } from './liveconn'
export type { LiveTermHandle } from './liveconn'

// ── 底层 client(api / ttyd / idb):高级消费方直接调 ──
export * from './api'
export { connect } from './ttyd'
export type { TtydConn, TtydHandlers } from './ttyd'
export { idbGetTx, idbPutTx, purgeLegacyTx } from './idb'
export type { TxCache } from './idb'
export { highlighter, LANG_SET } from './highlight'

// ── 类型 ──
export type {
  Item,
  Block,
  BlockType,
  PatchHunk,
  TranscriptResp,
  SubMeta,
  SubtranscriptResp,
  WorkflowAgent,
  WorkflowDetail,
  SessionMeta,
  Info,
} from './types'
