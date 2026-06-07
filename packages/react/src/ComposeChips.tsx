// ComposeChips —— 空闲 input 态的次级快捷 chip 组(并入 ControlBar 的 .compose-tools 工具行)。
//
// 形态(按用户反馈):
//   · 模型 chip(「opus 按钮」):显示「模型 + 版本 + 力度」一体(如 ◈ Opus 4.8 · high)。点 → /model
//     (模型选择页里同时改模型与力度,故不另设力度按钮;这里的力度只是只读展示)。可拉宽、整行换行,
//     但高度与其余 chip 一致。
//   · 压缩(/compact)、清空(/clear):做成 compact 图标按钮(只图标、定宽),与 ControlBar 的 / 命令、
//     ✨ 建议 同款,四枚风格统一。
//   组件返回 fragment(无自带行容器),由 ControlBar 平铺进 .compose-tools。
//
// 设计约束:纯展示 + 自包含;模型 chip 只是替用户敲 /model 的快捷入口(真正菜单由里世界回报后经 selectKind
// 派发到 Rich 组件);所有提交走 ControlBar.submit 唯一漏斗;/clear 走自绘 confirm(不用原生 confirm)。
import { useState, type ReactNode } from 'react'

// 模型短名:claude-opus-4-8 → Opus 4.8;识别不出家族则原样返回(截断由 CSS 省略)。
function shortModelLabel(m?: string): string {
  if (!m) return ''
  const fam = /opus/i.test(m) ? 'Opus' : /sonnet/i.test(m) ? 'Sonnet' : /haiku/i.test(m) ? 'Haiku' : ''
  const ver = m.match(/(\d+)[-.](\d+)/)
  const v = ver ? ver[1] + '.' + ver[2] : ''
  return fam ? (v ? fam + ' ' + v : fam) : m
}

// 力度 → 规范短档 + tone(优先更强档匹配,杜绝 "ultrathink" 里的 "think" 抢先;与 .eff-* 调色板同源)。
const EFFORT_KEYS: Array<{ key: string; tone: string }> = [
  { key: 'ultrathink', tone: 'max' }, { key: 'ultra', tone: 'max' }, { key: 'max', tone: 'max' },
  { key: 'high', tone: 'high' }, { key: 'medium', tone: 'med' }, { key: 'low', tone: 'low' }, { key: 'minimal', tone: 'min' },
]
function effortInfo(effort?: string): { label: string; tone: string } | null {
  if (!effort) return null
  const low = effort.toLowerCase()
  for (const e of EFFORT_KEYS) {
    if (low.includes(e.key)) return { label: e.key === 'ultrathink' || e.key === 'ultra' ? 'max' : e.key, tone: e.tone }
  }
  return { label: effort, tone: 'med' }
}

export interface ComposeChipsProps {
  // 最近已知模型 / 力度(按 host+sid 缓存喂入;input 态屏不报,故只能显示「最近已知」)。
  lastModel?: string
  lastEffort?: string
  // 唯一提交漏斗(对应 ControlBar.submit):非信息类命令(/model、/compact、/clear)走它(certain 闸 + 原子清空)。
  submit: (payload: string) => void
  // 信息类命令运行入口(对应 ControlBar.onRunCmd):被判信息类则走它抓屏展示而非提交。
  onRunCmd?: (cmd: string) => void
  isInfoCmd?: (cmd: string) => boolean
}

function runCmd(
  cmd: string,
  submit: ComposeChipsProps['submit'],
  onRunCmd: ComposeChipsProps['onRunCmd'],
  isInfoCmd: ComposeChipsProps['isInfoCmd'],
) {
  if (isInfoCmd && isInfoCmd(cmd) && onRunCmd) onRunCmd(cmd)
  else submit(cmd)
}

export function ComposeChips({ lastModel, lastEffort, submit, onRunCmd, isInfoCmd }: ComposeChipsProps): ReactNode {
  const [clearCfm, setClearCfm] = useState(false)
  const modelLbl = shortModelLabel(lastModel)
  const eff = effortInfo(lastEffort)
  const title = (modelLbl || '模型') + (eff ? ' · 力度 ' + eff.label : '') + '(点开切换模型/力度)'

  return (
    <>
      {/* 模型 chip(opus 按钮):模型 + 版本 + 力度一体;点 → /model(力度也在该页内)。可拉宽/换行,高度同其余 chip。 */}
      <button type="button" className="cchip cchip--model" title={title} onClick={() => runCmd('/model', submit, onRunCmd, isInfoCmd)}>
        <span className="cchip-ico" aria-hidden>◈</span>
        <span className="cchip-txt">
          {modelLbl || '模型'}
          {eff && <span className={'cchip-eff eff-' + eff.tone}> · {eff.label}</span>}
        </span>
      </button>

      {/* 压缩(/compact):compact 图标按钮。 */}
      <button type="button" className="cchip cchip--icon" title="压缩上下文(/compact)" aria-label="压缩上下文" onClick={() => runCmd('/compact', submit, onRunCmd, isInfoCmd)}>
        <span className="cchip-ico" aria-hidden>🗜</span>
      </button>

      {/* 清空(/clear):compact 图标按钮,破坏性 → 先弹自绘 confirm。 */}
      <button type="button" className="cchip cchip--icon cchip--danger" title="清空对话(/clear)" aria-label="清空对话" onClick={() => setClearCfm(true)}>
        <span className="cchip-ico" aria-hidden>🧹</span>
      </button>

      {clearCfm && (
        <div className="cfm">
          <div className="cfm-box">
            <div className="cfm-msg">清空当前对话?</div>
            <div className="cfm-prev">将向里世界发送 /clear,清掉本会话上下文(不可撤销)。</div>
            <div className="cfm-btns">
              <button className="cbtn" onClick={(e) => { e.preventDefault(); setClearCfm(false) }}>取消</button>
              <button
                className="cbtn danger"
                onClick={(e) => {
                  e.preventDefault()
                  runCmd('/clear', submit, onRunCmd, isInfoCmd)
                  setClearCfm(false)
                }}
              >
                清空
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}

export default ComposeChips
