// RichConfirmSelect.tsx —— 富确认卡(纯客户端分类,无需改解析)。
//
// 定位:把里世界那种「是/否」「是/审阅/否」「是否按计划继续」的确认菜单,从通用 .cbtn.opt 列表
// 升格成一张「居中提问 + 大号色彩按钮」的确认卡。所有判定都是对 st.title/st.options/st.actions
// 的客户端正则分类,点击仍回退到与通用菜单完全一致的 moveTo+Enter / act(keys) 键序——零解析改动。
//
// 三种子形态(同一组件内分支):
//   1) 普通确认:绿色主按钮(是/继续)+ 红色否定按钮(否/取消),❔ 字形。
//   2) 破坏性确认:title 命中 delete/clear/remove/destroy/reset → 切红色危险主题
//      (红框 + ⚠ 字形 + 肯定键改用「动作动词」如「删除/清空」)。
//   3) 计划批准:title 含 proceed + plan → 🧭 字形(沿用 PlanCard 的导航隐喻)。
//
// 仅处理单选(integrator 已用 selectKind 把多选与 model/permission 排除在外)。
import type { ReactNode } from 'react'
import type { CtrlState } from '../api'

// 单个选项形状(与 CtrlState.options 逐字段对齐,这里收窄成本组件用到的部分)。
type CtrlOption = NonNullable<CtrlState['options']>[number]
// 动作形状(与 CtrlState.actions 对齐:label + 可选 keys/text)。
type CtrlAction = NonNullable<CtrlState['actions']>[number]

// ── RichSelectProps:富 select 组件的统一 props 形状(integrator 注入同形对象)。 ──
// 组件是纯展示 + 回调:收 st 与从 ControlBar 透传的 helpers,点击只调 helpers,不碰 ControlBar 内部。
export interface RichSelectProps {
  // 当前控件状态(kind 必为 'select';本卡只读 title/options/actions)。
  st: CtrlState
  helpers: {
    // 把里世界菜单光标移到目标项(发方向键序列,不提交);与通用菜单的 moveTo 完全一致。
    moveTo: (num: string) => void
    // 多选切换(本确认卡用不到,但保持 props 同形以便 integrator 统一注入)。
    toggleAt: (num: string) => void
    // 统一发键层:body 注入 tmux 键 / 文本,toast 为可选可见反馈文案。
    act: (body: { text?: string; keys?: string[]; enter?: boolean }, toast?: string) => void
    // 计算「移到目标项」的方向键序列(供需要「移动+回车」一次性发送时复用)。
    navTo: (num: string) => string[]
  }
}

// ── 选项意图分类(客户端正则;label 优先,落到位序兜底)── ────────────────────────
type Intent = 'yes' | 'no' | 'review'

// 肯定词:yes/proceed/confirm/continue/ok/approve/允许/继续/确认/是 等(含破坏性动词,破坏性主题再覆写文案)。
const RE_YES = /^(yes\b|y\b|proceed|continue|confirm|ok\b|okay|approve|accept|allow|do it|go ahead|sure|是\b|好\b|继续|确认|允许|同意|删除|清空|移除|重置)/i
// 否定词:no/cancel/don't/dismiss/否/取消/拒绝/不 等。
const RE_NO = /^(no\b|n\b|cancel|don'?t|dismiss|abort|reject|deny|stop|否\b|取消|拒绝|不\b|算了)/i
// 审阅词:review/edit/modify/审阅/修改/编辑(中性第三态)。
const RE_REVIEW = /(review|edit\b|modify|revise|审阅|修改|编辑|查看)/i

// 把一个选项归类为 yes/no/review;label 命不中时按「首项=yes、末项=no」的常规兜底。
function classifyOption(o: CtrlOption, idx: number, total: number): Intent {
  const lab = o.label || ''
  if (RE_REVIEW.test(lab)) return 'review'
  if (RE_NO.test(lab)) return 'no'
  if (RE_YES.test(lab)) return 'yes'
  // 兜底:两/三项确认里,首项通常是肯定、末项通常是否定,中间项作 review。
  if (idx === 0) return 'yes'
  if (idx === total - 1) return 'no'
  return 'review'
}

// ── 破坏性判定:title 命中删除/清空/移除/销毁/重置 → 危险主题。 ──
const RE_DESTRUCTIVE = /delete|clear|remove|destroy|reset|删除|清空|移除|销毁|重置/i
// ── 计划批准判定:title 同时含 proceed 与 plan(或中文「按计划继续」)→ 🧭 主题。 ──
const RE_PLAN = /(proceed[\s\S]*plan|plan[\s\S]*proceed|按.*计划|计划.*继续|continue[\s\S]*plan)/i

// 从破坏性 title 抽一个「动作动词」放到肯定按钮上(删除/清空/…),抽不到回退「确认」。
function destructiveVerb(title: string): string {
  if (/delete|删除/i.test(title)) return '删除'
  if (/clear|清空/i.test(title)) return '清空'
  if (/remove|移除/i.test(title)) return '移除'
  if (/destroy|销毁/i.test(title)) return '销毁'
  if (/reset|重置/i.test(title)) return '重置'
  return '确认'
}

// 顶部字形:破坏性 ⚠、计划批准 🧭、普通 ❔。
function headIcon(destructive: boolean, plan: boolean): string {
  if (destructive) return '⚠'
  if (plan) return '🧭'
  return '❔'
}

// 在 actions 里找一个「肯定」动作(label 命中肯定词,或带 enter/Enter 键)。找不到返回 null。
function findAffirmAction(actions: CtrlAction[]): CtrlAction | null {
  for (const a of actions) {
    if (RE_YES.test(a.label || '') || a.label === '确认') return a
    if ((a.keys || []).some((k) => k.toLowerCase() === 'enter')) return a
  }
  return null
}
// 在 actions 里找一个「否定/取消」动作(label 命中否定词,或带 Escape 键)。找不到返回 null。
function findNegateAction(actions: CtrlAction[]): CtrlAction | null {
  for (const a of actions) {
    if (RE_NO.test(a.label || '') || a.label === '取消') return a
    if ((a.keys || []).some((k) => k.toLowerCase() === 'escape' || k.toLowerCase() === 'esc')) return a
  }
  return null
}

// ── 富确认卡。 ── ───────────────────────────────────────────────────────────────
export function RichConfirmSelect({ st, helpers }: RichSelectProps): ReactNode {
  const title = (st.title || '').trim()
  const options = st.options || []
  const actions = st.actions || []

  const destructive = RE_DESTRUCTIVE.test(title)
  const plan = RE_PLAN.test(title)
  const icon = headIcon(destructive, plan)

  // 逐项归类:把选项分到 yes/no/review 三桶(保留各自 num,点选走 moveTo)。
  const total = options.length
  const tagged = options.map((o, i) => ({ opt: o, intent: classifyOption(o, i, total) }))
  const yesOpt = tagged.find((t) => t.intent === 'yes')?.opt
  const noOpt = tagged.find((t) => t.intent === 'no')?.opt
  const reviewOpt = tagged.find((t) => t.intent === 'review')?.opt

  // ── 提交一个意图 ── ───────────────────────────────────────────────────────────
  // 肯定:先把光标移到 yes 项(若有),再发 Enter 提交;并优先复用 st.actions 里的「确认」动作。
  //   破坏性主题下 toast 用动作动词;否则用「已确认」。
  const confirmYes = () => {
    const affirm = findAffirmAction(actions)
    const toast = destructive ? '已' + destructiveVerb(title) : '已确认'
    if (yesOpt) {
      // 移到 yes 项 + Enter 一次发送(与通用 moveTo+确认 等价,避免移动/回车竞态)。
      helpers.act({ keys: [...helpers.navTo(yesOpt.num), 'Enter'] }, toast)
      return
    }
    if (affirm) {
      helpers.act({ keys: affirm.keys, text: affirm.text, enter: affirm.text ? true : undefined }, toast)
      return
    }
    // 既无 yes 项也无肯定动作:退回纯 Enter(里世界默认高亮项即肯定项)。
    helpers.act({ keys: ['Enter'] }, toast)
  }

  // 否定:优先 st.actions 的「取消」动作;否则若有 no 项就移过去 + Enter;再否则发 Escape 兜底。
  const confirmNo = () => {
    const negate = findNegateAction(actions)
    if (negate) {
      helpers.act({ keys: negate.keys, text: negate.text, enter: negate.text ? true : undefined })
      return
    }
    if (noOpt) {
      helpers.act({ keys: [...helpers.navTo(noOpt.num), 'Enter'] })
      return
    }
    helpers.act({ keys: ['Escape'] })
  }

  // 审阅(第三态):无独立语义,等同「移到该项 + Enter」,toast 用「已选择」。
  const confirmReview = () => {
    if (reviewOpt) helpers.act({ keys: [...helpers.navTo(reviewOpt.num), 'Enter'] }, '已选择')
  }

  // 肯定按钮文案:破坏性 → 动作动词;计划批准 → 「按计划继续」;否则取 yes 项 label 或「确认」。
  const yesLabel = destructive ? destructiveVerb(title) : plan ? '按计划继续' : yesOpt?.label || '确认'
  // 否定按钮文案:取 no 项 label 或「取消」。
  const noLabel = noOpt?.label || '取消'

  return (
    <div className={'cbar col rcs' + (destructive ? ' rcs--danger' : '')}>
      {/* 居中提问:字形 + 标题原文(无标题时省略字形那行,只留按钮)。 */}
      {title && (
        <div className="rcs-q">
          <span className="rcs-icon" aria-hidden="true">{icon}</span>
          <span className="rcs-qt">{title}</span>
        </div>
      )}
      {/* 大号色彩按钮行:窄屏自动竖排(.cbar-row 在样式里 wrap)。 */}
      <div className="cbar-row rcs-btns">
        <button
          className={'cbtn rcs-yes' + (destructive ? ' danger' : ' primary')}
          onClick={confirmYes}
        >
          {destructive ? '⚠ ' : plan ? '🧭 ' : '✓ '}
          {yesLabel}
        </button>
        {/* 审阅按钮:仅在确实存在中性第三态时出现(中性配色)。 */}
        {reviewOpt && (
          <button className="cbtn rcs-review" onClick={confirmReview}>
            ✎ {reviewOpt.label}
          </button>
        )}
        <button className="cbtn danger rcs-no" onClick={confirmNo}>
          ✕ {noLabel}
        </button>
      </div>
    </div>
  )
}

export default RichConfirmSelect
