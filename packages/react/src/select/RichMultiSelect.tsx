import type { ReactNode } from 'react'
import type { CtrlState } from '../api'

// ── RichMultiSelect:多选菜单的「兜底加强版」(catch-all richer multi-select)──────────────
//
// 适用面:任何「多选型」select 控件(选项带复选框三态 checked!==undefined),且未被更专门的
// 富组件(model / permission)接管。它在通用复选菜单基础上做了三件事:
//   1) 更大更清晰的复选框字形(☑/☐ 放大,行内左对齐,触屏好点);
//   2) 底栏一条实时「已选 N / 共 M」计数(直接由 st.options[].checked 现算,无本地副本——
//      勾选态权威来自下一帧 detectState 回报,计数随之刷新,不做乐观本地态以免与里世界打架);
//   3) 逐项点击 = toggleAt(o.num)(= navTo + Space,单批发键),与 ControlBar 复选行为完全一致。
//
// 纯展示 + 自包含:状态(st)与操作回调(helpers)全经 props 注入,不 import ControlBar 内部。
// 点击如何映射回里世界发键,全在 helpers 里(由集成方按 ControlBar 同款 navTo/toggleAt/act 注入)。
//
// 与 ControlBar 行为对齐的关键两点:
//   - 行点击走 helpers.toggleAt(num):导航 + Space 同批,避免「移动后未切换」的竞态;
//   - 底栏 actions 过滤掉那枚全局「切换」按钮(因为已逐项渲染复选框 + Space 切换,留着会重复;
//     规则与 ControlBar 第 351–352 行一致),仅保留「确认 / 取消」等提交类入口。

// helpers:集成方注入的发键回调族(与 ControlBar 内同名同义),本组件只调用、不实现。
export interface RichSelectHelpers {
  // 多选切换:把里世界菜单光标移到第 num 项后按 Space 勾/取消勾选(navTo + Space 单批发键)。
  toggleAt: (num: string) => void
  // 通用发键出口:keys=语义键序列(如 ['Enter']/['Escape']),text=直接上屏文本;
  // toast 为提交类操作的可见反馈文案(取消类不传)。
  act: (body: { text?: string; keys?: string[]; enter?: boolean }, toast?: string) => void
}

// props 契约:与同族富 select 组件统一为 RichSelectProps(state + helpers)。
export interface RichSelectProps {
  st: CtrlState
  helpers: RichSelectHelpers
}

// 是否为「本组件可接管的多选态」:至少一项带复选框三态(checked!==undefined)。
// 集成方据此 + 优先级(model/permission 先匹配,本组件为最低优先兜底)派发到这里。
export function isMultiSelect(st: CtrlState): boolean {
  return st.kind === 'select' && (st.options || []).some((o) => o.checked !== undefined)
}

export function RichMultiSelect({ st, helpers }: RichSelectProps): ReactNode {
  const { toggleAt, act } = helpers
  const options = st.options || []
  // 实时计数:M=可勾选项(带复选框态)的总数,N=其中已勾选数。直接从 st 现算,
  // 随每帧 detectState 回报自动刷新(不缓存本地副本,杜绝与里世界勾选态不一致)。
  const total = options.filter((o) => o.checked !== undefined).length
  const selected = options.filter((o) => o.checked === true).length

  return (
    <div className="cbar col rmsel">
      {st.title && <div className="cbar-title">{st.title}</div>}

      {options.map((o) => {
        const hasBox = o.checked !== undefined
        return (
          <button
            key={o.num}
            // 复用 .cbtn.opt(基础选项样式)与 .cbtn.opt.on(已勾选绿条)/.cur(当前光标项),
            // 叠加 .rmsel-opt 放大触点;非复选项(理论上不出现于多选态)退化为仅移动光标语义由集成方兜。
            className={'cbtn opt rmsel-opt' + (o.cur ? ' cur' : '') + (o.checked ? ' on' : '')}
            onClick={() => toggleAt(o.num)}
          >
            {/* 放大的复选框字形:勾选 ☑、未勾选 ☐(与 ControlBar / AskUserQuestionCard 同族字形)。 */}
            {hasBox && <span className="rmsel-box">{o.checked ? '☑' : '☐'}</span>}
            <span className="rmsel-lbl">
              {o.num}. {o.label}
            </span>
          </button>
        )
      })}

      {/* 力度 ◀/▶ 行(若该多选态附带 effort,与 ControlBar 同款),保持一致体验。 */}
      {st.effort && (
        <div className="cbar-row">
          <button className="cbtn adj" onClick={() => act({ keys: ['Left'] })} title="降低力度">
            ◀
          </button>
          <span className="cbar-eff">{st.effort}</span>
          <button className="cbtn adj" onClick={() => act({ keys: ['Right'] })} title="提高力度">
            ▶
          </button>
        </div>
      )}

      {/* 底栏:实时「已选 N / 共 M」计数 + 提交/取消按钮。
          actions 过滤掉全局「切换」按钮(已逐项复选框切换,留着会重复;同 ControlBar 第 351–352 行规则)。 */}
      <div className="cbar-row rmsel-foot">
        <span className="rmsel-count" aria-live="polite">
          已选 {selected} / 共 {total}
        </span>
        {(st.actions || [])
          .filter((a) => a.label !== '切换')
          .map((a, i) => (
            <button
              key={i}
              className={'cbtn' + (a.label === '取消' ? ' danger' : a.label === '确认' ? ' primary' : '')}
              onClick={() => act({ keys: a.keys, text: a.text }, a.label === '取消' ? undefined : '已提交')}
            >
              {a.label}
            </button>
          ))}
      </div>
    </div>
  )
}

// ── 本组件全部 CSS(className 基,供集成方并入中央样式表;本文件不建/不引 .css)──────────
// 复用设计系统:.cbar/.cbar.col/.cbar-row/.cbar-title/.cbar-eff/.cbtn(.opt/.cur/.on/.adj/.primary/.danger)。
// CSS 变量:--mut --green --acc 等。移动安全:选项内文 white-space:normal + overflow-wrap,绝不横向溢出。
export const richMultiSelectCss = `
/* RichMultiSelect:多选菜单加强版 —— 容器仅作命名锚点(布局沿用 .cbar.col)。 */
.rmsel {}
/* 加强选项行:复选框 + 标签横排,文本可换行(长标签不撑破容器,移动安全)。 */
.rmsel-opt { display: flex; align-items: flex-start; gap: 10px; }
/* 放大的 ☑/☐ 字形:固定不缩,顶对齐首行;已勾选项配绿(与 .opt.on 绿条同族)。 */
.rmsel-box { flex: none; font-size: 20px; line-height: 1.2; color: var(--mut); }
.rmsel-opt.on .rmsel-box { color: var(--green); }
/* 标签:占满余宽并允许换行,长 token/路径不横向溢出。 */
.rmsel-lbl { flex: 1; min-width: 0; overflow-wrap: anywhere; }
/* 底栏:计数靠左、按钮靠右;按钮仍按 .cbar.col .cbar-row .cbtn 平分剩余宽。 */
.rmsel-foot { align-items: center; }
/* 实时计数:弱化的 mut 文字,占左侧、不抢按钮宽(flex:none 防被 .cbtn 的 flex:1 挤没)。 */
.rmsel-count { flex: none; font: 600 13px system-ui; color: var(--mut); white-space: nowrap; }
`
