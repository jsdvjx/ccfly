// engine/states/confirm.tsx — 是/否(二元/三元)确认对话(落在新读屏核心上):检测 + 闭环命令 + 卡片,一处内聚。
// 旧 RichConfirmSelect 只取语义(意图词、破坏性配色、字形);新设计读 pre、闭环驱动(移光标→等真动→只在按值到位时才 Enter)。
import { State, current, type Ctx, type StateInfo } from '../engine'
import { useEngineState } from '../react'

type Intent = 'yes' | 'no' | 'review'
const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

// 确认词典:每个选项 label 必须命中之一,才算「确认菜单」而非普通二项 picker。
const RE_CONFIRMISH =
  /^(yes|no|review|proceed|cancel|confirm|delete|clear|continue|don'?t|是|否|取消|确认|继续|审阅|清空|删除|放弃)/i
// 否定词:no/cancel/don't/否/取消/放弃 等 → intent='no'。
const RE_NO = /^(no\b|n\b|cancel|don'?t|dismiss|abort|reject|deny|stop|否\b|取消|拒绝|不\b|放弃|算了)/i
// 审阅词:review/审阅(中性第三态)→ intent='review'。
const RE_REVIEW = /^(review|审阅)/i
// 破坏性:title 命中删除/清空/移除/重置/销毁 → 危险主题(肯定按钮转红)。
const RE_DESTRUCTIVE = /delete|clear|remove|reset|destroy|删除|清空|移除|重置|销毁/i
// 选「choose/select/pick/选择/挑选」菜单的反误判守卫:它不是是/否确认,让位 list。
const RE_PICKER = /\bchoose\b|\bselect\b|\bpick\b|选择|挑选/i

// 把一个 label 归类为 yes/no/review(否/审阅优先识别,其余皆作 yes)。
function intentOf(label: string): Intent {
  if (RE_REVIEW.test(label)) return 'review'
  if (RE_NO.test(label)) return 'no'
  return 'yes'
}

export interface ConfirmInfo extends StateInfo {
  kind: 'confirm'
  title: string
  destructive: boolean
  options: { label: string; cur: boolean; intent: Intent }[]
}

class ConfirmState extends State<ConfirmInfo> {
  readonly kind = 'confirm'
  readonly weight = 40

  resolve(ctx: Ctx): ConfirmInfo | null {
    const opts = ctx.pre.options
    // 决定性信号(保守:非确认的二项 picker 必须落空,交给 list)——
    // 1) 单选:任一带复选框态(checked!==undefined)即非确认,落空。
    if (opts.some((o) => o.checked !== undefined)) return null
    // 2) 选项数在 1~3 之间(典型二元/三元确认)。
    if (opts.length < 1 || opts.length > 3) return null
    // 标题归一:trim + 全角问号/叹号折成 ASCII,让「清空缓存？」也能被句末 ? 命中。
    const title = ctx.pre.title.trim().replace(/？/g, '?').replace(/！/g, '!')
    // 3) 标题像问句:do/would 起头、确认/是否 起头,或 ? 句末。
    const looksQuestion = /\?\s*$/.test(title) || /^(do you|would you|确认|是否)/i.test(title)
    if (!looksQuestion) return null
    // 4) 每个选项 label 都命中确认词典(否则是普通菜单)。
    if (!opts.every((o) => RE_CONFIRMISH.test(o.label))) return null
    // 5) 反误判:标题是 choose/select/pick 选择菜单 → 不是确认,落空交给 list。
    if (RE_PICKER.test(title)) return null
    return {
      kind: 'confirm',
      title,
      destructive: RE_DESTRUCTIVE.test(title),
      options: opts.map((o) => ({ label: o.label, cur: o.cur, intent: intentOf(o.label) })),
    }
  }

  // 闭环导航:把里世界光标移到 label 匹配的选项(每步一个 Down,等它真动了,没动就停),到位后 Enter 提交。
  async choose(label: string): Promise<boolean> {
    const want = new RegExp('^' + escapeRe(label) + '$', 'i')
    let info = this.now()
    if (!info) return false
    for (let s = info.options.length + 2; s > 0; s--) {
      const cur = info.options.find((o) => o.cur)
      if (cur && want.test(cur.label)) {
        await this.send(['Enter']) // 仅在按值到位时才提交
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
  } // Escape 取消 → 下一帧不再是 confirm → 卡片自动卸载

  private now(): ConfirmInfo | null {
    const m = current()
    return m && m.kind === 'confirm' ? (m.info as ConfirmInfo) : null
  }
}

export const confirm = new ConfirmState() // 单例,构造即自注册

// ── 视图:居中提问 + 每选项一颗按钮(yes 主色/破坏性转红,no/review 中性,当前项加 cur)──
export function ConfirmCard() {
  const m = useEngineState()
  if (!m || m.kind !== 'confirm') return null
  const info = m.info as ConfirmInfo
  return (
    <div className={'cbar col' + (info.destructive ? ' danger' : '')}>
      <div className={'cbar-title' + (info.destructive ? ' danger' : '')}>{info.title}</div>
      <div className="cbar-row">
        {info.options.map((o, i) => (
          <button
            key={i}
            className={
              'cbtn' +
              (o.intent === 'yes' ? (info.destructive ? ' danger' : ' primary') : '') +
              (o.cur ? ' cur' : '')
            }
            aria-current={o.cur ? 'true' : undefined}
            onClick={() => confirm.choose(o.label)}
          >
            {o.label}
          </button>
        ))}
      </div>
      <div className="cbar-row">
        <button className="cbtn" onClick={() => confirm.cancel()}>
          取消
        </button>
      </div>
    </div>
  )
}
