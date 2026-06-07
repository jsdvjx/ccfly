// engine/states/permission.tsx — 权限/信任提示控件(落在新核心上):检测 + 命令 + 视图,一处内聚。
// 镜像 modelSelect:resolve 读 pre.options 认「授权菜单」,命令侧闭环导航(每步等光标真动了,
// 没动就停),提交只在到位后发 Enter。视图按意图给按钮上色(允许/总是=主色、拒绝=危险)。
import { State, current, type Ctx, type StateInfo } from '../engine'
import { useEngineState } from '../react'

const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

// 决定性信号:选项里是否带「授权语义」—— 这是把真权限菜单和普通是/否确认分开的关键 tell。
// 用屏上 label 原文判定,不写死任何阶梯/枚举。
const RE_GRANT = /don'?t ask again|always|allow|grant|approve|允许|总是|批准|同意/i

// 单个 label → 意图。判定优先级:先「总是/不再询问」(常同时含 allow 词,如
// "Yes, and don't ask again" 应归 always 而非 allow),再 allow,再 deny,否则 other。
type Intent = 'allow' | 'always' | 'deny' | 'other'
function intentOf(label: string): Intent {
  const s = label || ''
  if (/always|don'?t\s+ask|总是|不再(询问|提示)/i.test(s)) return 'always'
  if (/\b(allow|grant|approve|accept|proceed|continue)\b|允许|批准|同意|继续/i.test(s)) return 'allow'
  if (/\b(deny|no|reject|decline|cancel|skip|never|stop)\b|拒绝|否决|取消|不允许/i.test(s)) return 'deny'
  return 'other'
}

export interface PermissionInfo extends StateInfo {
  kind: 'permission'
  title: string
  options: { label: string; cur: boolean; intent: Intent }[]
}

class PermissionState extends State<PermissionInfo> {
  readonly kind = 'permission'
  readonly weight = 25

  resolve(ctx: Ctx): PermissionInfo | null {
    const opts = ctx.pre.options
    // 决定性信号:≥2 选项 且 至少一项带授权 tell(allow/always/grant…)。
    // 标题里的 permission/trust/folder/工作区 只是佐证,选项 tell 才是主判别 —— 故只认选项。
    if (opts.length < 2 || !opts.some((o) => RE_GRANT.test(o.label))) return null
    return {
      kind: 'permission',
      title: ctx.pre.title,
      options: opts.map((o) => ({ label: o.label, cur: o.cur, intent: intentOf(o.label) })),
    }
  }

  // 选定某项:把里世界光标移到目标 label(下键,闭环每步等光标真动了,没动就停),到位才回车提交。
  async choose(label: string): Promise<boolean> {
    const want = new RegExp(escapeRe(label), 'i')
    let info = this.now()
    if (!info) return false
    for (let s = info.options.length + 2; s > 0; s--) {
      const cur = info.options.find((o) => o.cur)
      if (cur && want.test(cur.label)) {
        await this.send(['Enter']) // 到位才提交 → 下一帧不再是 permission → 卡片自动卸载(= 关闭)
        return true
      }
      const before = cur?.label
      await this.send(['Down'])
      const next = await this.waitFor((x) => x.options.find((o) => o.cur)?.label !== before)
      if (!next) return false
      info = next
    }
    return false
  }

  cancel() {
    return this.send(['Escape'])
  }

  private now(): PermissionInfo | null {
    const m = current()
    return m && m.kind === 'permission' ? (m.info as PermissionInfo) : null
  }
}

export const permission = new PermissionState() // 单例,构造即自注册

// ── 视图:标题 + 每个选项一枚按钮(允许/总是=主色、拒绝=危险) + 取消 ──
export function PermissionCard() {
  const m = useEngineState()
  if (!m || m.kind !== 'permission') return null
  const info = m.info as PermissionInfo

  const cls = (intent: Intent) =>
    'cbtn' + (intent === 'allow' || intent === 'always' ? ' primary' : intent === 'deny' ? ' danger' : '')

  return (
    <div className="cbar col">
      <div className="cbar-title">{info.title || '需要你的授权'}</div>

      {info.options.map((o, i) => (
        <button
          key={i}
          className={cls(o.intent) + (o.cur ? ' cur' : '')}
          aria-current={o.cur ? 'true' : undefined}
          onClick={() => permission.choose(o.label)}
        >
          {o.label}
          {o.intent === 'always' && <span className="pill">记住</span>}
        </button>
      ))}

      <div className="cbar-row">
        <button className="cbtn" onClick={() => permission.cancel()}>
          取消
        </button>
      </div>
    </div>
  )
}
