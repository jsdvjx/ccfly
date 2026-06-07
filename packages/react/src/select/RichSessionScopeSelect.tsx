// RichSessionScopeSelect.tsx —— 「全局保存 vs 仅本次会话」作用域富组件。
//
// 命中场景(reSessOnly 页脚态):里世界弹出形如「Save globally / 's' to use this session only」的
// 提示时,ctrlstate 会在 st.actions 里追加一条 { label:'本次', text:'s' } 动作(见
// livestate.ts:327 / ctrlstate.go:221)。此时通用 select 兜底会把它渲成一排小按钮,语义不直观。
// 本组件把它升格为两张大「作用域卡」:🌐 全局保存(Enter) vs 💾 仅本次会话(s),外加取消。
//
// 设计要点:
//   1) 纯展示 + 自包含 —— 状态与回调全经 props(RichSelectProps)注入,绝不 import ControlBar 内部。
//   2) 不臆造按键 —— 三张卡各自从 st.actions 里「按字面找」对应动作(Enter/text:'s'/Escape),
//      点击时把那条动作原样回灌给 helpers.act;故无需改解析层,后端/客户端探测零改动。
//   3) 移动端安全 —— 卡片用 flex 自适应换行,无横向溢出;全部样式走 className(见导出的 cssText)。
import type { ReactNode } from 'react'
import type { CtrlState } from '../api'
import type { SendBody } from '../sendkeys'

// ── 富 select 组件统一 props 契约(本目录首个文件,在此权威定义;同族富 select 共用此形状)──
// helpers 即 ControlBar 内部那套发键助手的「对外切面」:富组件只消费,不关心其实现。
//   act(body, toast?) —— 统一发键(语义键/文本/提交),提交类操作传 toast 给可见反馈(见 ControlBar.act)。
//   navTo / moveTo / toggleAt —— 菜单光标导航助手(本组件不用,但同族组件需要,故纳入契约)。
export interface RichSelectHelpers {
  act: (body: SendBody, toast?: string) => void
  navTo: (num: string) => string[]
  moveTo: (num: string) => void
  toggleAt: (num: string) => void
}
export interface RichSelectProps {
  st: CtrlState
  helpers: RichSelectHelpers
}

// st.actions 单条形状(与 CtrlState.actions[] 对齐):{ label, keys?, text? }。
type CtrlAction = NonNullable<CtrlState['actions']>[number]

// ── 探测:是否「作用域(本次/全局)」菜单 —— 供集成方在 confirm/generic 之前优先分流 ──
// 判定:actions 含 { text:'s' }(全局 vs 本次的关键标记),或含 { label:'本次' }(中文标签兜底)。
// 与 spec.detectSignature 一致:selectKind(st)==='sessionScope'。
export function isSessionScope(st: CtrlState): boolean {
  const acts = st.actions || []
  return acts.some((a) => a.text === 's') || acts.some((a) => a.label === '本次')
}

// 在 actions 里「按字面」找一条动作:keys 完全匹配,或 text 完全匹配。找不到返回 undefined(对应卡不渲染)。
function findByKeys(acts: CtrlAction[], keys: string[]): CtrlAction | undefined {
  return acts.find((a) => {
    const k = a.keys || []
    return k.length === keys.length && k.every((x, i) => x === keys[i])
  })
}
function findByText(acts: CtrlAction[], text: string): CtrlAction | undefined {
  return acts.find((a) => a.text === text)
}

// 单张作用域卡:大图标 + 标签 + 副标题(键位提示);点击把传入的 action 原样回灌 helpers.act。
function ScopeCard({
  icon,
  label,
  sub,
  action,
  toast,
  helpers,
  variant,
}: {
  icon: string
  label: string
  sub: string
  action: CtrlAction
  toast?: string
  helpers: RichSelectHelpers
  variant?: 'primary' | 'danger'
}): ReactNode {
  return (
    <button
      className={'rss-card cbtn' + (variant ? ' ' + variant : '')}
      // 原样回灌:keys 优先(若该动作带 keys),否则用 text;两者皆取自 st.actions,不臆造。
      onClick={() => helpers.act({ keys: action.keys, text: action.text }, toast)}
    >
      <span className="rss-icon" aria-hidden>
        {icon}
      </span>
      <span className="rss-label">{label}</span>
      <span className="rss-sub">{sub}</span>
    </button>
  )
}

// ── 主组件:作用域富 select ──
// 三张卡按 st.actions 字面解析:
//   保存(全局) ← keys:['Enter']  → act({keys:['Enter']}, '已保存')
//   仅本次会话   ← text:'s'        → act({text:'s'}, '仅本次')
//   取消         ← keys:['Escape'] → act({keys:['Escape']})(无 toast)
// 任一动作缺失则对应卡不渲染(例如里世界本帧没给 Enter 动作时不臆造保存卡)。
export function RichSessionScopeSelect({ st, helpers }: RichSelectProps): ReactNode {
  const acts = st.actions || []
  const save = findByKeys(acts, ['Enter']) // 全局保存
  const sess = findByText(acts, 's') // 仅本次
  const cancel = findByKeys(acts, ['Escape']) // 取消

  return (
    <div className="cbar col rss">
      {st.title && <div className="cbar-title">{st.title}</div>}
      <div className="cbar-row rss-cards">
        {save && (
          <ScopeCard
            icon="🌐"
            label="全局保存"
            sub="对所有会话生效 · Enter"
            action={save}
            toast="已保存"
            helpers={helpers}
            variant="primary"
          />
        )}
        {sess && (
          <ScopeCard
            icon="💾"
            label="仅本次会话"
            sub="只对当前会话生效 · s"
            action={sess}
            toast="仅本次"
            helpers={helpers}
          />
        )}
      </div>
      {/* 键盘提示行(灰态):呼应里世界页脚的「Enter / s / Esc」语义。 */}
      <div className="rss-hint">Enter 全局保存 · s 仅本次 · Esc 取消</div>
      {cancel && (
        <div className="cbar-row">
          <button
            className="rss-cancel cbtn danger"
            onClick={() => helpers.act({ keys: cancel.keys, text: cancel.text })}
          >
            取消
          </button>
        </div>
      )}
    </div>
  )
}

export default RichSessionScopeSelect
