// RichEffortSelect.tsx —— 力度/思考强度的「滑轨」富组件:把里世界的 /effort 菜单(或纯横向力度条)
// 渲染成一条 minimal·low·medium·high·max 的水平刻度轨,当前档高亮上色,◀/▶ 作为真实的微调控件,
// 点击某一档则按需发出若干 Left/Right 把里世界光标推到那一档。
//
// 设计原则(与 ControlBar 既有架构一致,见 ControlBar.tsx select 分支):
//   - 纯展示 + 自包含:状态与回调皆由 props(RichSelectProps)注入,组件本身不 import ControlBar 内部。
//   - 客户端分类:仅凭 st.effort / st.options / st.title 判定「这是力度菜单」,点击映射回与原版相同的
//     Left/Right(微调)或 moveTo(num)(编号菜单跳档)+ Enter(确认),无需改解析器(options 已解析)。
//   - 无乐观位移:点击后不本地预移高亮,位置一律等下一帧 st 回报(WS 在线 ~150ms / 降级轮询),
//     与 ControlBar 的 moveTo 行为对齐。
//
// 两种工作形态:
//   1) 编号力度菜单(st.options 非空,形如 "1. minimal / 2. low / …"):点档用 helpers.moveTo(num)。
//   2) 纯滑轨(solo-slider:st.effort 有值、st.options 为空):点档用若干 helpers.act({keys:['Left'|'Right']})
//      把光标从「当前档」推到「目标档」。
import { useMemo, type ReactNode } from 'react'
import type { CtrlState } from '../api'

// ── 共享 props 契约(与其它 rich-select 组件同形,由集成方统一收口)──
// helpers 暴露 ControlBar 内已有的发键能力:act(注入 tmux 键)、moveTo(编号菜单跳到第 num 项的方向键批)。
// 注:本组件只用到 act / moveTo;其它 rich-select 组件可能用到更多 helpers,集成方按并集定义即可。
export interface RichSelectHelpers {
  // 注入 tmux 键 / 文本(对应 ControlBar 的 act):微调走 act({keys:['Left'|'Right']}),确认走 act({keys:['Enter']})。
  act: (body: { text?: string; keys?: string[]; enter?: boolean }, toast?: string) => void
  // 编号菜单跳档:把里世界菜单光标移到第 num 项的方向键批(对应 ControlBar 的 moveTo)。
  moveTo: (num: string) => void
  // 多选切换(本组件不用,留全集以对齐其它组件的 props 形状)。
  toggleAt?: (num: string) => void
}

export interface RichSelectProps {
  st: CtrlState
  helpers: RichSelectHelpers
}

// ── 力度档位:从弱到强的规范顺序(minimal..max);轨道从左到右即此序 ──
// tone 用于上色,复用设计系统 .eff-low/.eff-med/.eff-high/.eff-max 调色板;minimal 走中性灰(无专属类)。
type EffortTone = 'min' | 'low' | 'med' | 'high' | 'max'
interface EffortLevel {
  key: string // 规范键(用于匹配 st.effort 文本 / 选项标签)
  label: string // 轨道上显示的短标签
  tone: EffortTone // 上色档(对应 .eff-* 调色板)
  // 该键的别名(里世界文案可能写 ultra/ultrathink 等);用于关键词扫描命中同一档。
  aliases: string[]
}
const EFFORT_LEVELS: readonly EffortLevel[] = [
  { key: 'minimal', label: 'minimal', tone: 'min', aliases: ['minimal', 'none', 'off'] },
  { key: 'low', label: 'low', tone: 'low', aliases: ['low'] },
  { key: 'medium', label: 'medium', tone: 'med', aliases: ['medium', 'med', 'default', 'think'] },
  { key: 'high', label: 'high', tone: 'high', aliases: ['high', 'megathink'] },
  { key: 'max', label: 'max', tone: 'max', aliases: ['max', 'maximum', 'ultra', 'ultrathink'] },
]

// ── 本地关键词解析(不 import 共享 EFFORT_TONE,避免耦合)──
// 在一段力度文本里扫出它落在哪一档:逐档逐别名做大小写无关的子串匹配,命中即返回该档索引。
// 优先「更强档」匹配:从 max 往 minimal 扫,杜绝 "ultrathink" 里的 "think"(medium 别名)抢先命中。
function levelIndexOf(text: string | undefined): number {
  if (!text) return -1
  const low = text.toLowerCase()
  for (let i = EFFORT_LEVELS.length - 1; i >= 0; i--) {
    const lvl = EFFORT_LEVELS[i]
    for (const a of lvl.aliases) {
      if (low.includes(a)) return i
    }
  }
  return -1
}

// 从编号选项里找「当前档(cur)」对应的规范档索引;找不到返回 -1。
function curIndexFromOptions(options: CtrlState['options']): number {
  if (!options || options.length === 0) return -1
  const cur = options.find((o) => o.cur)
  if (!cur) return -1
  return levelIndexOf(cur.label)
}

// ── 检测谓词:集成方据此把 select 态派发到本组件 ──
// 命中条件:有 st.effort,或 title 含 effort/thinking;且「尚未」被识别为 model/permission 菜单
//(那两类由各自的 rich 组件优先接管,本组件让位)。solo-slider 子例:st.effort 且 options 为空。
export function isEffortSelect(st: CtrlState): boolean {
  if (st.kind !== 'select') return false
  const title = st.title || ''
  // 让位:model/permission 菜单不归本组件(由对应 rich 组件处理)。
  if (/\bmodel\b|模型/i.test(title)) return false
  if (/permission|权限|allow|deny/i.test(title)) return false
  return !!st.effort || /effort|thinking|力度|思考/i.test(title)
}

export function RichEffortSelect({ st, helpers }: RichSelectProps): ReactNode {
  const options = st.options || []
  const isSolo = options.length === 0 // 纯滑轨:无编号选项,仅 st.effort 文本

  // 当前档索引:编号菜单优先读 cur 选项,其次兜底读 st.effort 关键词;都没有则取 medium 居中。
  const curIdx = useMemo(() => {
    const fromOpt = curIndexFromOptions(st.options)
    if (fromOpt >= 0) return fromOpt
    const fromEffort = levelIndexOf(st.effort)
    if (fromEffort >= 0) return fromEffort
    return EFFORT_LEVELS.findIndex((l) => l.tone === 'med') // 兜底居中
  }, [st.options, st.effort])

  // 编号菜单:把规范档索引映射回里世界选项编号(o.num),供 moveTo 跳档。
  // 映射方式:用该选项标签解析出的规范档索引去匹配;一档可能对应一个选项(力度菜单通常一一对应)。
  const numForLevel = useMemo(() => {
    const map = new Map<number, string>()
    for (const o of options) {
      const li = levelIndexOf(o.label)
      if (li >= 0 && !map.has(li)) map.set(li, o.num)
    }
    return map
  }, [options])

  // 是否有「确认」动作(编号菜单一般有 Enter 确认;纯滑轨多为即时生效,无确认)。
  const confirmAction = (st.actions || []).find((a) => a.label === '确认')

  // 点击某一档:
  //   - 编号菜单且该档有对应选项编号 → helpers.moveTo(num)(方向键批跳到该项,不提交;确认按钮另发 Enter)。
  //   - 否则(纯滑轨,或编号菜单缺映射)→ 按当前档与目标档之差,发出相应数量的 Left/Right。
  const jumpTo = (targetIdx: number) => {
    if (targetIdx === curIdx) return
    const num = numForLevel.get(targetIdx)
    if (!isSolo && num) {
      helpers.moveTo(num)
      return
    }
    // 纯滑轨 / 无编号映射:逐档微调。目标在右 → 连发 Right;在左 → 连发 Left。
    const delta = targetIdx - curIdx
    const key = delta > 0 ? 'Right' : 'Left'
    const keys = Array(Math.abs(delta)).fill(key)
    helpers.act({ keys })
  }

  // ◀/▶ 单步微调(始终走 Left/Right,与原版 effort 行一致)。
  const stepLeft = () => {
    if (curIdx > 0) helpers.act({ keys: ['Left'] })
  }
  const stepRight = () => {
    if (curIdx < EFFORT_LEVELS.length - 1) helpers.act({ keys: ['Right'] })
  }

  const atMin = curIdx <= 0
  const atMax = curIdx >= EFFORT_LEVELS.length - 1
  const curLevel = EFFORT_LEVELS[curIdx] || EFFORT_LEVELS[2]

  return (
    <div className="res">
      {/* 标题:有 st.title 用原文,纯滑轨兜底「调整力度」。 */}
      <div className="res-head">
        <span className="res-title">{st.title || '调整思考力度'}</span>
        <span className={'res-cur eff-' + curLevel.tone}>⚡{curLevel.label}</span>
      </div>

      {/* 微调 + 滑轨一体:◀ / 刻度轨 / ▶。轨道每一档是一个可点 segment;当前档 .on 上色。 */}
      <div className="res-bar">
        <button className="cbtn adj res-adj" onClick={stepLeft} disabled={atMin} title="弱一档(←)">
          ◀
        </button>
        <div className="res-track" role="group" aria-label="思考力度">
          {EFFORT_LEVELS.map((lvl, i) => {
            const on = i === curIdx
            const reached = i <= curIdx // 已达到(含当前):用于轨道「填充」观感
            const cls =
              'res-seg eff-' + lvl.tone + (on ? ' on' : '') + (reached ? ' reached' : '')
            return (
              <button
                key={lvl.key}
                className={cls}
                onClick={() => jumpTo(i)}
                aria-pressed={on}
                title={lvl.label}
              >
                <span className="res-dot" />
                <span className="res-label">{lvl.label}</span>
              </button>
            )
          })}
        </div>
        <button className="cbtn adj res-adj" onClick={stepRight} disabled={atMax} title="强一档(→)">
          ▶
        </button>
      </div>

      {/* 动作行:有「确认」动作时给一枚确认按钮(编号菜单);纯滑轨即时生效则隐去。
          取消始终给(对齐原版 actions 里的 Escape),让用户可退出面板。 */}
      <div className="res-actions">
        {confirmAction && (
          <button
            className="cbtn primary"
            onClick={() => helpers.act({ keys: ['Enter'] }, '已提交')}
          >
            确认
          </button>
        )}
        {(st.actions || [])
          .filter((a) => a.label === '取消')
          .map((a, i) => (
            <button
              key={i}
              className="cbtn danger"
              onClick={() => helpers.act({ keys: a.keys, text: a.text })}
            >
              {a.label}
            </button>
          ))}
      </div>
    </div>
  )
}

export default RichEffortSelect
