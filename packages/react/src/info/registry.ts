import type { CardModule } from './types'
import { usage } from './usage'
import { status } from './status'
import { stats } from './stats'
import { settings } from './settings'
import { mcp } from './mcp'
import { doctor } from './doctor'
import { hooks } from './hooks'
import { skills } from './skills'
import { context } from './context'

// 一条命令的完整描述符 = 路由键 + 展示 + 分组 + 到达方式 + 模态性 + 卡模块。
// 这张表是「斜杠命令 → 原生卡」的唯一真相:Palette(isInfoCmd)、App(cardFor)、InfoSheet(groupOf)全从它派生。
// 增删一个可前端化命令 = 改这一行。
export interface CmdCard {
  cmd: string // 路由键(单卡时也作标题)
  label: string // tab 名
  group?: string // 同 group → 渲成 tabs(顺序 = 表内顺序);省略 → 独占单卡
  tabOnly?: boolean // 只作为组内 tab,不是 Palette 入口(/stats 无直达命令;/config 仍走透传以便改设置)
  reach: { cmd: string; rights?: number; esc?: number } // 到达:发哪条斜杠;落地后定向 N×Right(默认 0,单次不累积);esc=该卡面板需几次 Esc 清场/收场(默认 1;/config 搜索框需 2;/context 打印进流、无挡路面板 → 0,绝不在干净输入态乱按 Esc)
  modal: boolean // true=停留面板(抓完发 Esc + 延时更久 + 多轮询);false=打印进对话(/context)
  viaJsonl?: boolean // true=结果走 jsonl 的 isMeta markdown(摆脱抓屏):发命令前取游标、提交、轮询 /cmdresult,直接 MD 渲染那段 markdown。实测仅 /context 写 isMeta。
  mod: CardModule<any> // 统一契约 { parse, Card };<any> 让异构卡共存一表,类型安全在各模块内闭合
}

export const CARDS: CmdCard[] = [
  // ── /cost 组:四条同 group → 自动 4 tab(= 旧 INFO_TABS)。usage/status 是 Palette 入口;stats/config 仅组内 tab ──
  { cmd: '/cost', label: '用量', group: '会话信息', reach: { cmd: '/cost' }, modal: true, mod: usage },
  { cmd: '/status', label: '状态', group: '会话信息', reach: { cmd: '/status' }, modal: true, mod: status },
  { cmd: '/stats', label: '统计', group: '会话信息', tabOnly: true, reach: { cmd: '/cost', rights: 1 }, modal: true, mod: stats },
  { cmd: '/config', label: '设置', group: '会话信息', tabOnly: true, reach: { cmd: '/config', esc: 2 }, modal: true, mod: settings }, // 搜索框:首次 Esc 仅清过滤,第二次才关面板
  // ── 独占单卡 ──
  // /context:走 jsonl 通知路径(viaJsonl)——「发送命令后会收到通知」:发 /context 前取 transcript 游标,
  // 提交命令,轮询 /cmdresult 拿那条 isMeta `## Context Usage` 干净 markdown,经 context.Md 渲成结构卡
  // (parseContextMd → ContextCard,失败回退 <MD>)。esc:0=不停留面板、打印进流,故不乱按 Esc;modal:false。
  // (它也仍会内联进消息流,但用户要的是点 /context 即弹的那张富卡,故重新接回信息体系。)
  { cmd: '/context', label: '上下文', reach: { cmd: '/context', esc: 0 }, modal: false, viaJsonl: true, mod: context },
  { cmd: '/mcp', label: 'MCP', reach: { cmd: '/mcp' }, modal: true, mod: mcp },
  { cmd: '/doctor', label: '体检', reach: { cmd: '/doctor' }, modal: true, mod: doctor },
  { cmd: '/hooks', label: '钩子', reach: { cmd: '/hooks' }, modal: true, mod: hooks },
  { cmd: '/skills', label: '技能', reach: { cmd: '/skills' }, modal: true, mod: skills },
]

// ── 派生索引(唯一真相的三个投影)──
const byCmd = new Map(CARDS.map((c) => [c.cmd, c]))
export const cardFor = (cmd: string): CmdCard | undefined => byCmd.get(cmd) // App:命中即开 InfoSheet
// Palette 入口 = 命中且非 tabOnly(/cost /status /context /mcp /doctor /hooks /skills)。
export const isInfoCmd = (cmd: string): boolean => {
  const c = byCmd.get(cmd)
  return !!c && !c.tabOnly
}
// 同组(tabs);独占卡返回单元素组。single-vs-tabs 是 group 字段的自然投影,不是代码分叉。
export const groupOf = (c: CmdCard): CmdCard[] => (c.group ? CARDS.filter((x) => x.group === c.group) : [c])
