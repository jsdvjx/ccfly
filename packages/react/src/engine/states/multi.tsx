// engine/states/multi.tsx — 多选(复选框)菜单控件(落在新读屏核心上):检测 + 闭环命令 + 视图,一处内聚。
// 决定性信号:任一选项带复选框三态(pre 已把 ☑/☐/[x]/[ ] 解析成 checked: boolean)。
// 不做乐观本地态:勾选权威来自下一帧回报,toggle 后 waitFor 那项 checked 翻转(modelSelect 同款)。
import { State, current, type Ctx, type StateInfo } from '../engine'
import { useEngineState } from '../react'

const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

export interface MultiInfo extends StateInfo {
  kind: 'multi'
  title: string
  options: { label: string; cur: boolean; checked: boolean }[]
}

class MultiState extends State<MultiInfo> {
  readonly kind = 'multi'
  readonly weight = 50

  resolve(ctx: Ctx): MultiInfo | null {
    const opts = ctx.pre.options
    // 决定性信号:至少一项带复选框态(checked!==undefined)。没有 → 不是多选菜单,放行给别的态。
    if (!opts.some((o) => o.checked !== undefined)) return null
    return {
      kind: 'multi',
      title: ctx.pre.title,
      options: opts.map((o) => ({ label: o.label, cur: o.cur, checked: !!o.checked })), // 三态强制成 boolean
    }
  }

  // 切换某项:先把里世界光标闭环移到目标项(上/下键,每步等光标真的动了,没动就停),
  // 再按 Space 勾/取消勾选,最后等那项的 checked 翻转(超时即停,不盲发)。
  async toggle(label: string): Promise<boolean> {
    const want = new RegExp(escapeRe(label), 'i')
    let info = this.now()
    if (!info) return false
    for (let s = info.options.length + 2; s > 0; s--) {
      const cur = info.options.find((o) => o.cur)
      if (cur && want.test(cur.label)) break // 光标已在目标项
      // 目标在光标之上 → Up,之下 → Down;同一方向逐格逼近。
      const curIdx = info.options.findIndex((o) => o.cur)
      const wantIdx = info.options.findIndex((o) => want.test(o.label))
      if (wantIdx < 0) return false
      const dir = wantIdx < curIdx ? 'Up' : 'Down'
      const before = cur?.label
      await this.send([dir])
      const next = await this.waitFor((x) => x.options.find((o) => o.cur)?.label !== before)
      if (!next) return false // 没动 = 到头/竞态,停
      info = next
    }
    // 已在目标项:记下当前勾选态,按 Space,等下一帧那项翻转。
    const target = this.now()?.options.find((o) => want.test(o.label))
    if (!target) return false
    const wasChecked = target.checked
    await this.send(['Space'])
    const flipped = await this.waitFor(
      (x) => !!x.options.find((o) => want.test(o.label) && o.checked !== wasChecked),
      800,
    )
    return !!flipped
  }

  confirm() {
    return this.send(['Enter'])
  } // 提交里世界多选 → 下一帧不再是 multi → 卡片自动卸载(= 关闭)
  cancel() {
    return this.send(['Escape'])
  }

  private now(): MultiInfo | null {
    const m = current()
    return m && m.kind === 'multi' ? (m.info as MultiInfo) : null
  }
}

export const multi = new MultiState() // 单例,构造即自注册

// ── 视图:每项一行 ☑/☐ + 标签,底栏「已选 N / 共 M」+ 确认 / 取消 ──
export function MultiCard() {
  const m = useEngineState()
  if (!m || m.kind !== 'multi') return null
  const info = m.info as MultiInfo
  const selected = info.options.filter((o) => o.checked).length
  const total = info.options.length

  return (
    <div className="cbar col">
      {info.title && <div className="cbar-title">{info.title}</div>}

      {info.options.map((o, i) => (
        <button
          key={i}
          className={'cbtn opt' + (o.cur ? ' cur' : '')}
          aria-current={o.cur ? 'true' : undefined}
          onClick={() => multi.toggle(o.label)}
        >
          {/* 放大的复选框字形:勾选 ☑、未勾选 ☐。 */}
          <span className="pill">{o.checked ? '☑' : '☐'}</span> {o.label}
        </button>
      ))}

      <div className="cbar-row">
        {/* 实时计数:直接由 info.options[].checked 现算,随每帧回报刷新(无本地副本)。 */}
        <span aria-live="polite">
          已选 {selected} / 共 {total}
        </span>
        <button className="cbtn primary" onClick={() => multi.confirm()}>
          确认
        </button>
        <button className="cbtn danger" onClick={() => multi.cancel()}>
          取消
        </button>
      </div>
    </div>
  )
}
