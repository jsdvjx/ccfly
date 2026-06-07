import type { ReactNode } from 'react'
import type { CtrlState } from '../api'

// ── RichPermissionSelect:权限/信任提示的「富卡」渲染 ─────────────────────────────
//
// 设计要点(纯客户端分类,零解析改动):
//   ControlBar 的 select 分支已把里世界菜单解析成 st.title / st.options / st.actions
//   (见 api.ts CtrlState)。本组件不碰解析层,只对 st 做「客户端分类」:命中
//   权限/信任语义(/permission|claude needs|allow|trust|folder|workspace|directory/)
//   就以富卡呈现;点击仍映射回 ControlBar 原生的 navTo+Enter / toggleAt 发键。
//   因此它是 PRESENTATIONAL(展示型)+ 自包含:只收 state + 回调(props),不引 ControlBar 内部。
//
// 覆盖两类菜单:
//   1) 单选权限提示(Claude 请求执行某操作:Allow / Allow always / Deny)——
//      点击 = helpers.moveTo(num) 仅移动光标;按「确认」action 才回车提交。
//   2) 多选权限清单(某选项 o.checked!==undefined)——点击 = helpers.toggleAt(num) 勾/取消;
//      Enter 确认。两类统一在底部 action 行落地(action 来自 st.actions,行为与 ControlBar 一致)。
//   同时覆盖「信任文件夹/工作区/目录」提示(title 含 trust/folder/workspace/directory)。

// helpers:ControlBar 暴露的发键原语(逐一对齐 ControlBar.tsx 内部同名函数)。
//   - act:统一发键出口(语义键 keys / 文本 text+enter);第二参为可选 toast 文案。
//   - moveTo:仅把里世界菜单光标移到目标项(单选用,不提交)。
//   - toggleAt:把光标移到目标项并按 Space 勾/取消(多选用)。
export interface RichSelectHelpers {
  act: (body: { text?: string; keys?: string[]; enter?: boolean }, toast?: string) => void
  moveTo: (num: string) => void
  toggleAt: (num: string) => void
}

// 富 select 组件统一 props 形状(整库 select/*.tsx 复用同一契约,便于 integrator 一处分发)。
export interface RichSelectProps {
  st: CtrlState
  helpers: RichSelectHelpers
}

// ── 选项「意图」分类(纯本地 label 正则,不依赖后端额外字段)──────────────────────
type OptIntent = 'allow' | 'deny' | 'dontask' | 'neutral'

// 「不再询问/总是允许」类:绿底里更进一步的「记住此决定」,以柔和 amber 标识 + 角标提醒。
//   命中:don't ask again / always allow / always / yes, and don't ask / 不再询问 / 总是允许 …
const reDontAsk = /don['’]?t\s+ask|always|不再(询问|提示)|总是(允许|允许)?|记住/i
// 允许/同意类(绿):allow / yes / approve / proceed / accept / 允许 / 同意 / 确认 / 是 …
const reAllow = /\b(allow|yes|approve|approved|accept|proceed|trust|grant|continue)\b|允许|同意|信任|继续|批准/i
// 拒绝/取消类(红):deny / no / reject / cancel / don't / skip / 拒绝 / 否 / 取消 / 不 …
const reDeny = /\b(deny|denied|no|reject|decline|cancel|skip|never|stop|abort)\b|拒绝|否决|取消|不允许|拒绝/i

// 对单个选项 label 归类。判定优先级:先看「不再询问」(它常同时含 allow 词,如
// "Yes, and don't ask again"——应归 dontask 而非 allow),再 allow,再 deny,否则中性。
function classifyOption(label: string): OptIntent {
  const s = label || ''
  if (reDontAsk.test(s)) return 'dontask'
  if (reAllow.test(s)) return 'allow'
  if (reDeny.test(s)) return 'deny'
  return 'neutral'
}

// 从 st.title 抽出「被请求的目标」(命令/路径/工具名),以等宽行突出展示。
//   常见形态:
//     "Claude needs your permission to run `npm install`"  → npm install
//     "Allow Bash(npm test)?"                              → Bash(npm test)
//     "Do you trust the files in /path/to/repo?"           → /path/to/repo
//   抽取顺序:反引号内 > 括号内 > 类路径片段;都抓不到则返回空(只渲染标题)。
function extractTarget(title: string): string {
  const t = title || ''
  const bt = /`([^`]+)`/.exec(t)
  if (bt) return bt[1].trim()
  const paren = /\(([^)]+)\)/.exec(t)
  if (paren) return paren[1].trim()
  const path = /(\/[^\s?]+|~\/[^\s?]+|[A-Za-z]:\\[^\s?]+)/.exec(t)
  if (path) return path[1].trim()
  return ''
}

// 是否为「信任文件夹/工作区/目录」提示(决定卡头图标:🗂 信任 vs 🔒 权限)。
const reTrust = /trust|folder|workspace|directory|文件夹|目录|工作区|信任/i

// intent → 选项额外 className(左色条 + 字色),与 .cbtn.opt 叠加。
const INTENT_CLASS: Record<OptIntent, string> = {
  allow: ' rps-opt--allow',
  deny: ' rps-opt--deny',
  dontask: ' rps-opt--soft',
  neutral: '',
}

export function RichPermissionSelect({ st, helpers }: RichSelectProps): ReactNode {
  const title = st.title || ''
  const options = st.options || []
  const actions = st.actions || []
  // 多选判定:任一选项带复选框态(checked!==undefined)。与 ControlBar select 分支同口径。
  const isMulti = options.some((o) => o.checked !== undefined)
  const isTrust = reTrust.test(title)
  const target = extractTarget(title)

  return (
    <div className="cbar col rps">
      {/* 卡头:锁/作用域图标 + 标题。信任类用 🗂,纯权限类用 🔒。 */}
      <div className="cbar-title rps-head">
        <span className="rps-ico" aria-hidden>
          {isTrust ? '🗂' : '🔒'}
        </span>
        <span className="rps-title">{title || (isTrust ? '信任此目录?' : '需要你的授权')}</span>
      </div>

      {/* 被请求目标(等宽、可换行不溢出):命令/路径/工具名抓得到才渲染。 */}
      {target && <div className="rps-target" title={target}>{target}</div>}

      {/* 选项行:意图上色(绿=允许、红=拒绝、amber=不再询问)。
          多选(hasBox)点击 → toggleAt 勾/取消;单选点击 → moveTo 仅移动光标(确认才提交)。
          高亮(.cur)/勾选(.on)跟随里世界真实光标——由下一帧 detectState 回报,故不做乐观本地态。 */}
      {options.map((o) => {
        const hasBox = o.checked !== undefined
        const intent = classifyOption(o.label)
        const isDontAsk = intent === 'dontask'
        return (
          <button
            key={o.num}
            className={
              'cbtn opt rps-opt' +
              INTENT_CLASS[intent] +
              (o.cur ? ' cur' : '') +
              (o.checked ? ' on' : '')
            }
            onClick={() => (hasBox ? helpers.toggleAt(o.num) : helpers.moveTo(o.num))}
          >
            {hasBox ? (o.checked ? '☑' : '☐') + ' ' : ''}
            {o.num}. {o.label}
            {/* 「不再询问」角标:提醒这是会被记住的决定。 */}
            {isDontAsk && <span className="rps-dontask">记住</span>}
          </button>
        )
      })}

      {/* 力度行(权限菜单一般无 effort,但 CtrlState 通用,带了就渲染 ◀/▶,行为同 ControlBar)。 */}
      {st.effort && (
        <div className="cbar-row">
          <button className="cbtn adj" onClick={() => helpers.act({ keys: ['Left'] })}>◀</button>
          <span className="cbar-eff">{st.effort}</span>
          <button className="cbtn adj" onClick={() => helpers.act({ keys: ['Right'] })}>▶</button>
        </div>
      )}

      {/* 动作行:沿用 st.actions(确认=Enter、取消=Escape、本次=s …),发键与 ControlBar 完全一致。
          多选时已逐项渲染复选框+Space 切换,故隐去 action 行里那枚全局「切换」按钮(避免重复)。
          取消/拒绝类(label 取消、含 deny/no)染红;确认类染绿主色;其余中性。 */}
      <div className="cbar-row">
        {actions
          .filter((a) => !(isMulti && a.label === '切换'))
          .map((a, i) => {
            const danger = a.label === '取消' || reDeny.test(a.label)
            const primary = a.label === '确认' || /allow|continue|确认|继续|允许/i.test(a.label)
            return (
              <button
                key={i}
                className={'cbtn' + (danger ? ' danger' : primary ? ' primary' : '')}
                onClick={() => helpers.act({ keys: a.keys, text: a.text }, danger ? undefined : '已提交')}
              >
                {a.label}
              </button>
            )
          })}
      </div>
    </div>
  )
}

// ── 客户端分类谓词:integrator 用它把权限/信任 select 分发到本组件 ───────────────
//   命中条件(与 selectKind.ts 的 isPermission 同口径,Round-2 C 收紧):
//   · 强权限词(permission/trust/folder/workspace/directory/needs your permission/权限/信任/目录/工作区)
//     单独成立;
//   · 仅泛词(\ballow\b/允许)命中时,要求选项也带授权语义(allow/deny/yes/no/always/允许/拒绝 …),
//     避免把「Allow this?(是/否)」这类二元确认误判为权限菜单。
//   两路都要求至少 2 个选项(确为可选菜单,而非单按钮提示)。
const RE_PERM_STRONG_X = /permission|trust|folder|workspace|directory|needs?\s+(your\s+)?permission|权限|信任|目录|工作区/i
const RE_PERM_WEAK_X = /\ballow\b|允许/i
const RE_PERM_OPT_X = /\b(allow|deny|yes|no|always|approve|reject|grant)\b|允许|拒绝|同意|总是|批准/i
export function isPermissionSelect(st: CtrlState): boolean {
  if (st.kind !== 'select') return false
  const title = st.title || ''
  const opts = st.options || []
  if (opts.length < 2) return false
  if (RE_PERM_STRONG_X.test(title)) return true
  return RE_PERM_WEAK_X.test(title) && opts.some((o) => RE_PERM_OPT_X.test(o.label || ''))
}
