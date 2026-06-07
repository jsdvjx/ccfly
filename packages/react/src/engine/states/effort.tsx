// engine/states/effort.tsx — 独立「力度/思考强度」菜单(落在新核心上):检测 + 闭环命令 + 视图,一处内聚。
// 与 modelSelect 的区别:那个是「模型+力度」二合一选择器,本态只接管【单独弹出】的力度菜单。
// 两种里世界形态同收一态:① 编号选项菜单(options 非空,逐项移动光标 + Enter 提交);
//                          ② 纯横向力度条(options 为空,仅 ←/→ 微调 + Enter 保存)。
// 反 F7 bug:不硬编码 minimal..max 五档梯子,也不「默认居中 medium」——一律读屏文案/值,值即 pre.effort。
import { State, current, type Ctx, type StateInfo } from '../engine'
import { useEngineState } from '../react'

// 把 name 当字面量做大小写无关匹配(标签里可能含别的字符,转义防注入)。
const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

export interface EffortInfo extends StateInfo {
  kind: 'effort'
  title: string
  value?: string // 当前力度短语(= pre.effort || undefined);纯滑轨形态靠它显示
  options: { label: string; cur: boolean }[]
}

class EffortState extends State<EffortInfo> {
  readonly kind = 'effort'
  readonly weight = 30

  resolve(ctx: Ctx): EffortInfo | null {
    const title = ctx.pre.title
    // 决定性信号:① 不是模型菜单(让 modelSelect 先吃);
    if (/model|模型/i.test(title)) return null
    // ② 任一选项标签命中 opus/sonnet/haiku → 那是 modelSelect 的菜单,本态让位;
    if (ctx.pre.options.some((o) => /opus|sonnet|haiku/i.test(o.label))) return null
    // ③ 必须「确实是力度」:有 effort 值,或标题点名 effort/thinking/力度/思考。否则不接管。
    if (!ctx.pre.effort && !/effort|thinking|力度|思考/i.test(title)) return null
    return {
      kind: 'effort',
      title,
      value: ctx.pre.effort ?? undefined,
      options: ctx.pre.options.map((o) => ({ label: o.label, cur: o.cur })),
    }
  }

  // 编号菜单:把里世界光标移到 label 匹配的那一档(逐步 Down),闭环——每步等光标真的动了,没动就停。
  // 命中目标(按值)后才提交 Enter;绝不盲发连击。
  async pick(label: string): Promise<boolean> {
    const want = new RegExp(escapeRe(label), 'i')
    let info = this.now()
    if (!info) return false
    for (let s = info.options.length + 2; s > 0; s--) {
      const cur = info.options.find((o) => o.cur)
      if (cur && want.test(cur.label)) {
        await this.send(['Enter']) // 已在目标档 → 提交
        return true
      }
      const before = cur?.label
      await this.send(['Down'])
      const next = await this.waitFor((x) => x.options.find((o) => o.cur)?.label !== before)
      if (!next) return false // 光标没动 → 到头/失帧,停手
      info = next
    }
    return false
  }

  // 纯滑轨:左右键微调,等显示值变化(超时也无妨——说明到头了)。
  async nudge(dir: 'Left' | 'Right'): Promise<void> {
    const before = this.now()?.value
    await this.send([dir])
    await this.waitFor((x) => x.value !== before, 800)
  }

  save() {
    return this.send(['Enter']) // 提交力度 → 下一帧不再是 effort → 卡片自动卸载(= 关闭)
  }
  cancel() {
    return this.send(['Escape'])
  }

  private now(): EffortInfo | null {
    const m = current()
    return m && m.kind === 'effort' ? (m.info as EffortInfo) : null
  }
}

export const effort = new EffortState() // 单例,构造即自注册

// ── 视图:有编号选项 → 列表(点击逐项定位 + Enter);否则 → ◀ 值 ▶ 横向微调 + 保存。 ──
export function EffortCard() {
  const m = useEngineState()
  if (!m || m.kind !== 'effort') return null
  const info = m.info as EffortInfo
  const hasOptions = info.options.length > 0

  return (
    <div className="cbar col">
      <div className="cbar-title">{info.title || '思考力度'}</div>

      {hasOptions
        ? info.options.map((o, i) => (
            <button
              key={i}
              className={'cbtn opt' + (o.cur ? ' cur' : '')}
              aria-current={o.cur ? 'true' : undefined}
              onClick={() => effort.pick(o.label)}
            >
              {o.label}
              {o.cur && <span className="pill">当前</span>}
            </button>
          ))
        : (
          <>
            <div className="cbar-row">
              <button className="cbtn adj" onClick={() => effort.nudge('Left')}>
                ◀
              </button>
              <span className="pill">{info.value}</span>
              <button className="cbtn adj" onClick={() => effort.nudge('Right')}>
                ▶
              </button>
            </div>
            <div className="cbar-row">
              <button className="cbtn primary" onClick={() => effort.save()}>
                保存
              </button>
            </div>
          </>
        )}

      <div className="cbar-row">
        <button className="cbtn" onClick={() => effort.cancel()}>
          取消
        </button>
      </div>
    </div>
  )
}
