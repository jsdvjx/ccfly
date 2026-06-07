// engine/states/modelSelect.tsx — 模型切换控件(落在新核心上):检测 + 命令 + 两步向导视图,一处内聚。
// 视图改为向导:step1 选模型 → step2 调力度 → 保存(提交即自动关闭)。比旧的「模型+力度挤一张卡」清爽。
import { useState } from 'react'
import { State, current, type Ctx, type StateInfo } from '../engine'
import { useEngineState } from '../react'

type Family = 'opus' | 'sonnet' | 'haiku'
const familyOf = (s: string): Family | null =>
  /opus/i.test(s) ? 'opus' : /sonnet/i.test(s) ? 'sonnet' : /haiku/i.test(s) ? 'haiku' : null
const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

export interface ModelSelectInfo extends StateInfo {
  kind: 'modelSelect'
  title: string
  options: { label: string; cur: boolean; family: Family | null }[]
  effort?: string // 力度短语(如 "medium effort");无则只有一步
}

class ModelSelectState extends State<ModelSelectInfo> {
  readonly kind = 'modelSelect'
  readonly weight = 20

  resolve(ctx: Ctx): ModelSelectInfo | null {
    const opts = ctx.pre.options
    if (opts.length < 2 || !opts.some((o) => familyOf(o.label))) return null
    const title = titleAbove(ctx)
    if (!/model|模型/i.test(title)) return null
    return {
      kind: 'modelSelect',
      title,
      options: opts.map((o) => ({ label: o.label, cur: o.cur, family: familyOf(o.label) })),
      effort: ctx.pre.effort ?? undefined,
    }
  }

  // step1:把里世界光标移到目标模型(上下键,不提交)。闭环:每步等光标真的动了,没动就停。
  async gotoModel(name: string): Promise<boolean> {
    const want = new RegExp(escapeRe(name), 'i')
    let info = this.now()
    if (!info) return false
    for (let s = info.options.length + 2; s > 0; s--) {
      const cur = info.options.find((o) => o.cur)
      if (cur && want.test(cur.label)) return true
      const before = cur?.label
      await this.send(['Down'])
      const next = await this.waitFor((x) => x.options.find((o) => o.cur)?.label !== before)
      if (!next) return false
      info = next
    }
    return false
  }

  // step2:左右键调力度,等显示值变化(超时也无妨 —— 说明到头了)。
  async nudgeEffort(dir: 'Left' | 'Right'): Promise<void> {
    const before = this.now()?.effort
    await this.send([dir])
    await this.waitFor((x) => x.effort !== before, 800)
  }

  save() {
    return this.send(['Enter'])
  } // 提交里世界菜单 → 下一帧不再是 modelSelect → 卡片自动卸载(= 关闭)
  cancel() {
    return this.send(['Escape'])
  }

  private now(): ModelSelectInfo | null {
    const m = current()
    return m && m.kind === 'modelSelect' ? (m.info as ModelSelectInfo) : null
  }
}

function titleAbove(ctx: Ctx): string {
  const rows = ctx.pre.options.flatMap((o) => o.rows)
  const top = rows.length ? Math.min(...rows) : ctx.frame.rows
  for (let y = top - 1; y >= 0 && y >= top - 6; y--) {
    const t = ctx.frame.text(y).trim()
    if (t) return t
  }
  return ''
}

export const modelSelect = new ModelSelectState() // 单例,构造即自注册

// ── 视图:两步向导 ──
export function RichModelSelect() {
  const m = useEngineState()
  const [step, setStep] = useState<1 | 2>(1)
  const [busy, setBusy] = useState(false)
  if (!m || m.kind !== 'modelSelect') return null
  const info = m.info as ModelSelectInfo
  const hasEffort = !!info.effort
  const totalSteps = hasEffort ? 2 : 1

  const pickModel = async (label: string) => {
    setBusy(true)
    const ok = await modelSelect.gotoModel(label)
    if (ok && hasEffort) setStep(2)
    else if (ok) await modelSelect.save() // 无力度:选完即存
    setBusy(false)
  }
  const save = async () => {
    setBusy(true)
    await modelSelect.save()
  }

  return (
    <div className="cbar col rms">
      <div className="cbar-title">
        {step === 1 ? '选择模型' : '思考力度'}
        {totalSteps > 1 && <span className="rms-step"> · 第 {step}/{totalSteps} 步</span>}
      </div>

      {step === 1 &&
        info.options.map((o, i) => (
          <button
            key={i}
            className={'cbtn opt rms-opt' + (o.cur ? ' cur' : '')}
            aria-current={o.cur ? 'true' : undefined}
            disabled={busy}
            onClick={() => pickModel(o.label)}
          >
            <span className="rms-opt-top">
              {o.family && <span className={'pill rms-badge rms-badge--' + o.family}>{o.family}</span>}
              <span className="rms-opt-label">{o.label}</span>
              {o.cur && <span className="rms-cur-tag">当前</span>}
            </span>
          </button>
        ))}

      {step === 2 && (
        <>
          <div className="cbar-row">
            <button className="cbtn adj" disabled={busy} onClick={() => modelSelect.nudgeEffort('Left')}>
              ◀
            </button>
            <span className="cbar-eff">{info.effort}</span>
            <button className="cbtn adj" disabled={busy} onClick={() => modelSelect.nudgeEffort('Right')}>
              ▶
            </button>
          </div>
          <div className="cbar-row">
            <button className="cbtn" disabled={busy} onClick={() => setStep(1)}>
              ← 上一步
            </button>
            <button className="cbtn primary" disabled={busy} onClick={save}>
              保存
            </button>
          </div>
        </>
      )}

      <div className="cbar-row">
        <button className="cbtn" disabled={busy} onClick={() => modelSelect.cancel()}>
          取消
        </button>
      </div>
    </div>
  )
}
