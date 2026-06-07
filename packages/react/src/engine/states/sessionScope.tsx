// engine/states/sessionScope.tsx — 「全局保存 vs 仅本次会话」作用域提示(落在新读屏核心上):检测 + 命令 + 视图,一处内聚。
//
// 命中场景:里世界弹出形如「Save globally · 's' to use this session only」的提示。
// 与通用「是/否」确认的根本区别在【页脚】:这条提示的页脚带「s = 用于本次会话」的字面 tell ——
// 正是这个页脚标记把它从泛确认里分出来(旧实现靠 actions{text:'s'},新设计直接读 pre.footer)。
//
// 形态:无菜单选项可导航(纯页脚提示),三个命令各为一次原子发键(Enter / s / Escape),
// 故不需要 modelSelect 那种「移动光标到目标项」的闭环;按目标值提交 = 直接发对应键。
import { State, type Ctx, type StateInfo } from '../engine'
import { useEngineState } from '../react'

// 决定性信号:页脚出现「s …to use this session」(英文 tell),或中文「本次/仅此/this session」兜底。
// 只认页脚 —— resolve 不命中此信号即返回 null,与泛 confirm 互斥(weight 仅在真正并列时破平)。
const RE_SESS_FOOTER = /\bs\s+to\s+use\s+this\s+session/i
const RE_SESS_CJK = /本次|仅此|this session/i

export interface SessionScopeInfo extends StateInfo {
  kind: 'sessionScope'
  title: string
}

class SessionScopeState extends State<SessionScopeInfo> {
  readonly kind = 'sessionScope'
  readonly weight = 28

  resolve(ctx: Ctx): SessionScopeInfo | null {
    const footer = ctx.pre.footer
    if (!footer) return null
    if (!RE_SESS_FOOTER.test(footer) && !RE_SESS_CJK.test(footer)) return null
    return { kind: 'sessionScope', title: ctx.pre.title }
  }

  // 全局保存:Enter(对所有会话生效)。提交后下一帧不再是 sessionScope → 卡片自动卸载(= 关闭)。
  saveGlobal() {
    return this.send(['Enter'])
  }
  // 仅本次:字面字符 "s"(只对当前会话生效)。同样提交即关闭。
  thisSession() {
    return this.send(['s'])
  }
  cancel() {
    return this.send(['Escape'])
  }
}

export const sessionScope = new SessionScopeState() // 单例,构造即自注册

// ── 视图:两枚作用域按钮 + 取消 ──
export function ScopeCard() {
  const m = useEngineState()
  if (!m || m.kind !== 'sessionScope') return null
  const info = m.info as SessionScopeInfo

  return (
    <div className="cbar col">
      {info.title && <div className="cbar-title">{info.title}</div>}
      <div className="cbar-row">
        <button className="cbtn primary" onClick={() => sessionScope.saveGlobal()}>
          全局保存
        </button>
        <button className="cbtn" onClick={() => sessionScope.thisSession()}>
          仅本次
        </button>
      </div>
      <div className="cbar-row">
        <button className="cbtn" onClick={() => sessionScope.cancel()}>
          取消
        </button>
      </div>
    </div>
  )
}
