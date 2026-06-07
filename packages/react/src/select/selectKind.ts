// selectKind.ts —— 富 select 客户端分类器(集成方的「脊柱」,纯函数、零解析改动)。
//
// 作用:ControlBar 的 `st.kind === 'select'` 分支不再只渲一种通用 UI。本模块对已解析的
// st(title/options/effort/actions)做「客户端分类」,返回一个判别符,集成方据此把控件态
// 派发到对应的富组件;命中不了任何已知菜单则返回 'generic',回落到 ControlBar 既有的通用渲染。
//
// 设计要点:
//   1) 只读已解析字段(api.ts CtrlState),不碰 livestate.ts / ctrlstate.go,故 parser 零改动。
//   2) 顺序即优先级,且只在此一处定义——六个富组件不各自重判,避免「同一菜单被两个组件抢」。
//      优先级(高→低):model > permission > sessionScope > effort > confirm > multi > list > generic。
//        · model / permission 是「更具体」的菜单,必须排在 confirm(宽泛的是/否兜底)之前;
//        · sessionScope(全局 vs 本次)靠 st.actions 的 {text:'s'} 标记,排在 confirm/generic 前;
//        · effort 让位 model/permission(其谓词内部已排除),再让 confirm 兜单选是/否;
//        · multi 为带复选框态的「多选」兜底(model/permission 已先行接管各自的多选场景);
//        · list 为「≥4 项的长单选清单」兜底(/resume、agent/主题/命令选择器 …),排在 generic 之前,
//          给计数徽标 + 限高滚动 + 当前项 ❯ 指示;1~3 项的单选已由 confirm 接管,故 list 只吃长清单。
//   3) 谓词保守:宁可返回 'generic' 走「久经考验的既有分支」,也不冒进把通用菜单错认成富 UI。
//
// 统一 props 契约 RichSelectProps:六个富组件共用同一形状({ st, helpers }),便于集成方
// 一处构造 helpers、一处分发。helpers 即 ControlBar 内部那套发键助手的对外切面。
import type { CtrlState } from '../api'

// ── 富 select 组件统一 props 契约(整库 select/*.tsx 共用) ──
//   navTo:把里世界菜单光标移到目标项的方向键序列(不含提交);无需移动返回 []。
//   moveTo:仅移动光标(单选用,不提交)。
//   toggleAt:移动光标 + Space 勾/取消(多选用)。
//   act:统一发键出口(语义键 keys / 文本 text+enter);第二参为可选 toast 文案。
export interface RichSelectHelpers {
  navTo: (num: string) => string[]
  moveTo: (num: string) => void
  toggleAt: (num: string) => void
  act: (body: { text?: string; keys?: string[]; enter?: boolean }, toast?: string) => void
}

export interface RichSelectProps {
  st: CtrlState
  helpers: RichSelectHelpers
}

// 富 select 判别符:依次对应各富组件,'generic' = 回落 ControlBar 通用渲染。
export type SelectKind =
  | 'model'
  | 'permission'
  | 'sessionScope'
  | 'effort'
  | 'confirm'
  | 'multi'
  | 'list'
  | 'generic'

// ── 各类谓词(与各富组件文件内导出的 isXxx 逐一对齐;此处内联以集中优先级,避免循环依赖)──

// model:标题含 "model" 且 ≥2 选项 且 非多选(无任何 checked 态)且至少一项命中模型家族名。
function isModel(st: CtrlState): boolean {
  const title = (st.title || '').toLowerCase()
  const opts = st.options || []
  return (
    title.includes('model') &&
    opts.length >= 2 &&
    !opts.some((o) => o.checked !== undefined) &&
    opts.some((o) => /opus|sonnet|haiku|claude/i.test(o.label))
  )
}

// permission:标题含权限/信任语义,且 ≥2 选项(确为可选菜单,而非单按钮提示)。
// 健壮性(Round-2 C):
//   · "allow" 加词边界 \ballow\b,避免命中 "allowance"/"hallowed" 等噪声;
//   · "claude needs" 收紧为「needs (your )?permission」常见提示语,避免把任意含 "needs" 的标题误判;
//   · 选项侧加佐证:典型权限菜单的选项里几乎总有 allow/deny/yes/no/always/允许/拒绝 之一。
//     标题命中「强权限词」(permission/trust/folder/workspace/directory/权限/信任/目录/工作区)即可单独成立;
//     仅靠泛词(allow)命中时,要求选项也带授权语义,杜绝把「Allow this?(是/否)」这类二元确认抢走。
const RE_PERM_STRONG = /permission|trust|folder|workspace|directory|needs?\s+(your\s+)?permission|权限|信任|目录|工作区/i
const RE_PERM_WEAK = /\ballow\b|允许/i
const RE_PERM_OPT = /\b(allow|deny|yes|no|always|approve|reject|grant)\b|允许|拒绝|同意|总是|批准/i
function isPermission(st: CtrlState): boolean {
  const title = st.title || ''
  const opts = st.options || []
  if (opts.length < 2) return false
  if (RE_PERM_STRONG.test(title)) return true
  // 泛词兜底:标题只含 allow/允许 时,需选项也呈授权语义(否则让位 confirm/generic)。
  return RE_PERM_WEAK.test(title) && opts.some((o) => RE_PERM_OPT.test(o.label || ''))
}

// sessionScope:actions 含 {text:'s'}(全局 vs 本次的关键标记),或含 {label:'本次'}(中文兜底)。
function isSessionScope(st: CtrlState): boolean {
  const acts = st.actions || []
  return acts.some((a) => a.text === 's') || acts.some((a) => a.label === '本次')
}

// effort:有 st.effort,或标题含 effort/thinking/力度/思考;且非 model/permission(那两类已先行接管)。
function isEffort(st: CtrlState): boolean {
  const title = st.title || ''
  if (/\bmodel\b|模型/i.test(title)) return false
  if (/permission|权限|allow|deny/i.test(title)) return false
  return !!st.effort || /effort|thinking|力度|思考/i.test(title)
}

// confirm:单选、≤3 选项,且标题像个问句(do/would 起头 或 ? 结尾,含 CJK 全角 ？),
// 且选项要么全是是/否/审阅类词,要么本就 ≤2 项(典型二元确认)。
// 确认词典:全/否分别用于「全确认」判定 与「带否定项」识别(后者放宽 choose/select 守卫)。
const RE_CONFIRMISH =
  /^(yes|no|review|proceed|cancel|confirm|delete|clear|continue|don'?t|是|否|取消|确认|继续|审阅|清空|删除|放弃)/i
function isConfirm(st: CtrlState): boolean {
  const opts = st.options || []
  const isMulti = opts.some((o) => o.checked !== undefined)
  if (isMulti) return false
  if (opts.length === 0 || opts.length > 3) return false
  // 归一化标题:trim + 把 CJK 全角问号/叹号(U+FF1F ？/ U+FF01 !)折成 ASCII,
  // 让「清空缓存？」「Proceed？」也能被下面的 /\?\s*$/ 句末判定命中。
  const title = (st.title || '')
    .trim()
    .replace(/？/g, '?')
    .replace(/！/g, '!')
  // 问句判定:do/would 起头、确认/是否 起头(保留头锚分支),或 ? 句末(含已归一的全角)。
  // 句末 ? 分支同时接住「裸祈使 + 问号」:Proceed?/Clear?/Continue? 等无主语短问句。
  const looksQuestion = /^(do you|would you|确认|是否)/i.test(title) || /\?\s*$/.test(title)
  if (!looksQuestion) return false
  const allConfirmish = opts.every((o) => RE_CONFIRMISH.test(o.label || ''))
  // 反误判守卫:标题含纯「选择」线索(choose/select/pick/选择/挑选)且选项并非全是确认词,
  // 说明这是个二元「选 X」菜单而非是/否确认 —— 让位 generic 分支,别套确认卡。
  if (/\bchoose\b|\bselect\b|\bpick\b|选择|挑选/i.test(title) && !allConfirmish) return false
  return allConfirmish || opts.length <= 2
}

// multi:任一选项带复选框三态(checked!==undefined)。
function isMulti(st: CtrlState): boolean {
  return (st.options || []).some((o) => o.checked !== undefined)
}

// list:≥4 项的单选清单(无复选框)。最低优先兜底——上面的具体菜单都没命中、又不是多选时,
// 长单选清单走富清单卡(计数 + 限高滚动);1~3 项的单选已由 confirm 接管,故此处只吃长清单。
function isList(st: CtrlState): boolean {
  const opts = st.options || []
  if (opts.some((o) => o.checked !== undefined)) return false
  return opts.length >= 4
}

// ── 分类入口:固定优先级链,首个命中即返回;全不命中 → 'generic'。 ──
export function selectKind(st: CtrlState): SelectKind {
  if (st.kind !== 'select') return 'generic'
  if (isModel(st)) return 'model'
  if (isPermission(st)) return 'permission'
  if (isSessionScope(st)) return 'sessionScope'
  if (isEffort(st)) return 'effort'
  if (isConfirm(st)) return 'confirm'
  if (isMulti(st)) return 'multi'
  if (isList(st)) return 'list'
  return 'generic'
}
