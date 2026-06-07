// RichListSelect.tsx —— 「长单选清单」的富组件:把里世界那种「编号一长串、单选」的菜单
// (/resume 历史会话、agent 选择器、主题/命令选择器 …)从一摞朴素 .cbtn.opt 升格成
// 一张「带计数徽标 + 限高滚动 + 当前项 ❯ 清晰指示」的清单卡。
//
// 定位(优先级最低的单选兜底,排在 generic 之前):
//   selectKind 链里 model/permission/sessionScope/effort/confirm/multi 都没接住、且这是个
//   「≥4 项的单选菜单(无任何复选框态)」时,交给本组件;否则仍回落 ControlBar 既有通用渲染。
//   为何要 ≥4 项:1~3 项的二元/三元确认归 RichConfirmSelect 更合适;长清单才需要「计数 + 限高滚动」。
//
// 纯客户端分类、零解析改动:只读 st.title/st.options/st.actions(均已解析),点击映射回与通用菜单
// 完全一致的 moveTo(num)(仅移动光标,不提交)+ action 行「确认」回车。高亮 .cur 跟随里世界真实
// 光标,由下一帧 detectState 回报 —— 不做乐观本地高亮(与上游 moveTo 约定一致)。
import type { ReactNode } from 'react'
import type { CtrlState } from '../api'
import type { RichSelectProps } from './selectKind'

// ── 检测谓词(集成方据此 dispatch;与 selectKind 链里的判定对齐)──
// 命中:单选(无任何 checked 态)、≥4 项、从 1 起编号且含当前项(由解析层保证)。
// 注:model/permission/sessionScope/effort/confirm 已在更高优先级先行接管,故本谓词不必再排除它们
//   —— selectKind 的顺序即优先级,本组件只在它们都没命中时才被询问。但为可独立复用,这里仍做最小自检。
export function isListSelect(st: CtrlState): boolean {
  if (st.kind !== 'select') return false
  const opts = st.options || []
  if (opts.some((o) => o.checked !== undefined)) return false // 多选不归本组件
  return opts.length >= 4
}

export function RichListSelect({ st, helpers }: RichSelectProps): ReactNode {
  const { moveTo, act } = helpers
  const options = st.options || []
  const curNum = options.find((o) => o.cur)?.num

  return (
    // 复用 .cbar.col 纵向骨架,叠加 .rls 作用域钩子(样式见 styles.css)。
    <div className="cbar col rls">
      {/* 头部:标题 + 计数徽标(让「这是个长清单」一目了然)。 */}
      <div className="cbar-title rls-head">
        <span className="rls-title">{st.title || '请选择'}</span>
        <span className="pill rls-cnt">{options.length} 项</span>
      </div>

      {/* 限高滚动清单:超出本区域纵向滚动(不撑爆控件层、移动端无横向溢出)。
          当前项以 ❯ 前导 + .cur 高亮;点击 = moveTo(仅移动光标),按「确认」才提交。 */}
      <div className="rls-scroll" role="listbox" aria-label={st.title || '选项'}>
        {options.map((o) => (
          <button
            key={o.num}
            className={'cbtn opt rls-opt' + (o.cur ? ' cur' : '')}
            onClick={() => moveTo(o.num)}
            role="option"
            aria-selected={o.cur ? 'true' : 'false'}
          >
            <span className="rls-mark" aria-hidden="true">
              {o.cur ? '❯' : ' '}
            </span>
            <span className="rls-num">{o.num}.</span>
            <span className="rls-label">{o.label}</span>
          </button>
        ))}
      </div>

      {/* 力度行(长清单一般无 effort,但 CtrlState 通用,带了就渲染 ◀/▶,行为同 ControlBar)。 */}
      {st.effort && (
        <div className="cbar-row">
          <button className="cbtn adj" onClick={() => act({ keys: ['Left'] })}>◀</button>
          <span className="cbar-eff">{st.effort}</span>
          <button className="cbtn adj" onClick={() => act({ keys: ['Right'] })}>▶</button>
        </div>
      )}

      {/* 动作行:沿用 st.actions(确认=Enter、取消=Escape …),发键与 ControlBar 完全一致。
          当前项已选中时给「确认」更强的存在感(primary);未选中(curNum 为空,极少见)仍可点。 */}
      <div className="cbar-row">
        {(st.actions || []).map((a, i) => (
          <button
            key={i}
            className={'cbtn' + (a.label === '取消' ? ' danger' : a.label === '确认' ? ' primary' : '')}
            onClick={() => act({ keys: a.keys, text: a.text }, a.label === '取消' ? undefined : '已选择')}
          >
            {a.label}
            {/* 「确认」按钮带上当前项编号提示,让用户清楚提交的是哪一项。 */}
            {a.label === '确认' && curNum && <span className="rls-pick"> · {curNum}</span>}
          </button>
        ))}
      </div>
    </div>
  )
}

export default RichListSelect
