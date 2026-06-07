// engine/states/list.tsx — 通用「长单选清单」兜底态(落在新核心上):检测 + 命令 + 限高滚动卡,一处内聚。
// 定位:model/resume/agent/topic … 这类「编号一长串、单选」的菜单,凡没被更具体的态接住的,统统归这里。
// weight=90(最高 → 最后跑):它是兜底,只在前面所有更具体的 resolve 都落空时才赢。
import { useState } from 'react'
import { State, current, type Ctx, type StateInfo } from '../engine'
import { useEngineState } from '../react'

const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

export interface ListInfo extends StateInfo {
  kind: 'list'
  title: string
  options: { num: number | null; label: string; cur: boolean }[]
}

class ListState extends State<ListInfo> {
  readonly kind = 'list'
  readonly weight = 90 // 兜底:最高 weight，排在最后；只有更具体的态全没命中才轮到它

  resolve(ctx: Ctx): ListInfo | null {
    const opts = ctx.pre.options
    // 决定性信号(因为是兜底，故放宽)：≥2 项 且 没有任何一项带复选框态(checked!==undefined)= 纯单选。
    // checked 一旦出现说明是多选菜单，不归本态;weight=90 又保证只在别的态都落空时才赢。
    if (opts.length < 2) return null
    if (opts.some((o) => o.checked !== undefined)) return null
    return {
      kind: 'list',
      title: ctx.pre.title,
      options: opts.map((o) => ({ num: o.num, label: o.label, cur: o.cur })),
    }
  }

  // 闭环导航:把里世界光标移到 label 匹配的那一项(按目标相对当前的方向选 Up/Down，逐步走),
  // 每步等光标真的动了(cur 标签变化),没动就停(bail)；到达目标(BY VALUE)才回车提交。
  async pick(label: string): Promise<void> {
    const want = new RegExp('^' + escapeRe(label) + '$', 'i')
    let info = this.now()
    if (!info) return
    for (let s = info.options.length + 2; s > 0; s--) {
      const curIdx = info.options.findIndex((o) => o.cur)
      const tgtIdx = info.options.findIndex((o) => want.test(o.label))
      if (tgtIdx < 0) return // 目标不在屏上(滚动了/没解析到):不瞎按
      if (curIdx === tgtIdx) {
        await this.send(['Enter']) // 已在目标上 → 提交;下一帧不再是 list，卡片自动卸载(=关闭)
        return
      }
      const before = info.options[curIdx]?.label
      await this.send([tgtIdx > curIdx ? 'Down' : 'Up'])
      const next = await this.waitFor((x) => x.options.find((o) => o.cur)?.label !== before)
      if (!next) return // 光标没动:到头了/失焦,停手
      info = next
    }
  }

  cancel() {
    return this.send(['Escape'])
  }

  private now(): ListInfo | null {
    const m = current()
    return m && m.kind === 'list' ? (m.info as ListInfo) : null
  }
}

export const list = new ListState() // 单例,构造即自注册

// ── 视图:计数徽标 + 限高滚动清单 ──
export function ListCard() {
  const m = useEngineState()
  const [busy, setBusy] = useState(false)
  if (!m || m.kind !== 'list') return null
  const info = m.info as ListInfo

  const pick = async (label: string) => {
    setBusy(true)
    await list.pick(label)
    setBusy(false)
  }

  return (
    <div className="cbar col">
      <div className="cbar-title">
        {info.title || '请选择'}
        <span className="pill"> 共 {info.options.length} 项</span>
      </div>

      {/* 限高滚动:长清单超出本区域纵向滚动(不撑爆控件层、移动端无横向溢出)。
          当前项 ❯ 前导 + " cur" 高亮;点击 = 闭环导航到该项并提交。 */}
      <div style={{ maxHeight: 240, overflowY: 'auto' }}>
        {info.options.map((o, i) => (
          <button
            key={i}
            className={'cbtn opt' + (o.cur ? ' cur' : '')}
            aria-current={o.cur ? 'true' : undefined}
            disabled={busy}
            onClick={() => pick(o.label)}
          >
            <span aria-hidden="true">{o.cur ? '❯ ' : '  '}</span>
            {o.num != null && <span>{o.num}. </span>}
            {o.label}
          </button>
        ))}
      </div>

      <div className="cbar-row">
        <button className="cbtn" disabled={busy} onClick={() => list.cancel()}>
          取消
        </button>
      </div>
    </div>
  )
}
