// 富模型选择器(RichModelSelect):把里世界「/model 切换模型」菜单从一排朴素按钮,
// 升级成带「家族徽章 + 能力提示」的卡片行。纯客户端分类、零后端依赖 ——
// 只读 st.title / st.options / st.actions(均为已解析字段),命中模型菜单时渲染富 UI;
// 点击映射回与今天完全一致的 navTo + Enter / moveTo 发键路径,无需任何 parser/state 改动。
//
// 单选语义(与 ControlBar select 单选分支逐一对齐):点模型卡 = moveTo(o.num) 仅移动里世界
// 菜单光标(不提交);用户按 action 行的「确认」才回车提交。高亮跟随真实光标(st.options[].cur),
// 由下一帧 detectState 回报 —— 不做乐观本地高亮(与上游 moveTo 注释同款约定)。
import type { ReactNode } from 'react'
import type { CtrlState } from '../api'

// ── Props 契约(与同目录其余 select-rich 组件共用 RichSelectProps 形状)──
// helpers 由 ControlBar 注入:navTo(方向键序列)/ moveTo(导航不提交)/ toggleAt(导航+Space,
// 本单选组件用不到)/ act(统一发键,提交类操作传 toast 文案)。
export interface RichSelectProps {
  st: CtrlState
  helpers: {
    navTo: (num: string) => string[]
    moveTo: (num: string) => void
    toggleAt: (num: string) => void
    act: (body: { text?: string; keys?: string[]; enter?: boolean }, toast?: string) => void
  }
}

// ── 模型家族注册表(硬编码,无后端字段、无新依赖)──
// 能力提示来自固定的「家族级」事实(非逐模型——版本号会变,但家族档位稳定):
//   - Opus:旗舰档,1M 上下文窗口,支持自适应思考(可调思考力度至 max)。
//   - Sonnet:均衡档,1M 上下文窗口,支持自适应思考(力度至 high)。
//   - Haiku:极速档,200K 上下文窗口,主打快与省,不走扩展思考。
// tone 复用 .pill 族色味(on=绿/med 蓝/warn 琥珀),按档位给徽章上色。
type Family = 'opus' | 'sonnet' | 'haiku'
interface FamilyInfo {
  /** 徽章短名(Opus / Sonnet / Haiku)。 */
  badge: string
  /** 徽章色味:复用 .pill 的 on(绿)/ warn(琥珀);蓝走 .rms-badge--acc。 */
  tone: 'opus' | 'sonnet' | 'haiku'
  /** 上下文窗口能力提示(展示用短语)。 */
  ctx: string
  /** 思考能力提示(扩展/自适应思考是否支持)。 */
  thinking: string
}
const MODEL_FAMILY: Record<Family, FamilyInfo> = {
  // 旗舰:最强、最自主,1M 上下文,自适应思考可至 max 力度。
  opus: { badge: 'Opus', tone: 'opus', ctx: '1M 上下文', thinking: '自适应思考 · 力度至 max' },
  // 均衡:速度与智能的最佳平衡,1M 上下文,自适应思考至 high。
  sonnet: { badge: 'Sonnet', tone: 'sonnet', ctx: '1M 上下文', thinking: '自适应思考 · 力度至 high' },
  // 极速:最快最省,200K 上下文,主打快速响应(不走扩展思考)。
  haiku: { badge: 'Haiku', tone: 'haiku', ctx: '200K 上下文', thinking: '极速 · 不走扩展思考' },
}

// 从选项文案识别模型家族:大小写不敏感,opus/sonnet/haiku 任一命中即归档;
// 都不命中(如 "Default (recommended)" 这类不带家族名的项)返回 null —— 该行降级为无徽章的纯文本卡。
function detectFamily(label: string): Family | null {
  const s = (label || '').toLowerCase()
  if (s.includes('opus')) return 'opus'
  if (s.includes('sonnet')) return 'sonnet'
  if (s.includes('haiku')) return 'haiku'
  return null
}

// ── 判定谓词(集成方据此 dispatch 到本组件)──
// selectKind(st)==='model':标题含 "model" 且 ≥2 选项 且 非多选(无任何 checked 态)
// 且至少一项文案命中 opus/sonnet/haiku/claude。命中即用富模型卡渲染。
export function isModelSelect(st: CtrlState): boolean {
  const title = (st.title || '').toLowerCase()
  const opts = st.options || []
  return (
    title.includes('model') &&
    opts.length >= 2 &&
    !opts.some((o) => o.checked !== undefined) &&
    opts.some((o) => /opus|sonnet|haiku|claude/i.test(o.label))
  )
}

// 家族徽章药丸:复用 .pill 视觉骨架,按档位上 .rms-badge--<tone> 色味。
function FamilyBadge({ info }: { info: FamilyInfo }): ReactNode {
  return <span className={'pill rms-badge rms-badge--' + info.tone}>{info.badge}</span>
}

// ── 主组件 ──
export function RichModelSelect({ st, helpers }: RichSelectProps) {
  const { moveTo, act } = helpers
  const options = st.options || []

  return (
    // 复用 .cbar.col 纵向布局骨架,叠加 .rms 作本组件作用域钩子(全部样式见下方 cssText)。
    <div className="cbar col rms">
      {st.title && <div className="cbar-title">{st.title}</div>}

      {options.map((o) => {
        const fam = detectFamily(o.label)
        const info = fam ? MODEL_FAMILY[fam] : null
        // 单选项点击 = 仅移动里世界菜单光标(moveTo,不提交);确认走下方 action 行的回车。
        return (
          <button
            key={o.num}
            className={'cbtn opt rms-opt' + (o.cur ? ' cur' : '')}
            onClick={() => moveTo(o.num)}
            aria-current={o.cur ? 'true' : undefined}
          >
            <span className="rms-opt-top">
              {info && <FamilyBadge info={info} />}
              <span className="rms-opt-label">
                {o.num}. {o.label}
              </span>
              {/* 当前模型(里世界菜单光标所在项)挂一枚「当前」尾标,与 .cur 高亮呼应。 */}
              {o.cur && <span className="rms-cur-tag">当前</span>}
            </span>
            {/* 能力提示:仅识别出家族的行才有(上下文窗口 · 思考支持),弱存在感的 mut 小字。 */}
            {info && (
              <span className="rms-meta">
                {info.ctx} · {info.thinking}
              </span>
            )}
          </button>
        )
      })}

      {/* 思考力度 ◀/▶ 行:仅当本菜单带 effort 时出现(与 ControlBar select 分支同款)。 */}
      {st.effort && (
        <div className="cbar-row">
          <button className="cbtn adj" onClick={() => act({ keys: ['Left'] })}>
            ◀
          </button>
          <span className="cbar-eff">{st.effort}</span>
          <button className="cbtn adj" onClick={() => act({ keys: ['Right'] })}>
            ▶
          </button>
        </div>
      )}

      {/* 动作行:逐条渲染 st.actions(确认=primary 蓝、取消=danger 红、其余中性)。
          点击映射回与今天一致的发键:{keys|text};提交类(非「取消」)给 toast 反馈。
          单选确认即「按回车提交里世界菜单」—— 与 ControlBar 单选 action 行逻辑逐一对齐。 */}
      <div className="cbar-row">
        {(st.actions || []).map((a, i) => (
          <button
            key={i}
            className={'cbtn' + (a.label === '取消' ? ' danger' : a.label === '确认' ? ' primary' : '')}
            onClick={() => act({ keys: a.keys, text: a.text }, a.label === '取消' ? undefined : '已选择')}
          >
            {a.label}
          </button>
        ))}
      </div>
    </div>
  )
}

export default RichModelSelect
