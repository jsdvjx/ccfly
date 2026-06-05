import { Pill } from './shared'
import type { CardModule } from './types'

// /hooks 卡(只读):把 Claude Code Hooks 面板抓屏解析成结构化的 hook 类型清单,重渲成原生控件。
// 抓屏每行带前导空格,夹杂标题「Hooks」、「N hooks configured」、「ℹ … Learn more」只读说明、
// 底部「Enter to confirm · Esc to cancel」等提示;编号行行首可能有光标符 ❯ 或滚动符 ↓/↑,要剥掉。
// 面板本身只读(改 hook 要直接编辑 settings.json),故本卡不加任何交互。

export interface HookType {
  name: string
  desc: string
}

export interface Hooks {
  configured: number
  note: string
  types: HookType[]
}

export function parseHooks(text: string): Hooks | null {
  let sawTitle = false
  let configured = 0
  let note = ''
  const types: HookType[] = []

  for (const raw of text.split('\n')) {
    // 剥行首光标符 ❯ / 滚动符 ↓ ↑ 与空格,便于后续匹配。
    const line = raw.replace(/^[\s❯↓↑]+/, '').trimEnd()
    if (!line) continue

    // 标题行。
    if (line === 'Hooks') {
      sawTitle = true
      continue
    }

    // configured:「N hooks configured」。
    const mc = line.match(/^(\d+)\s+hooks?\s+configured$/i)
    if (mc) {
      configured = Number(mc[1])
      continue
    }

    // note:「ℹ … Learn more」,剥掉开头 ℹ 与结尾 Learn more。
    if (line.startsWith('ℹ')) {
      note = line
        .replace(/^ℹ\s*/, '')
        .replace(/\s*Learn more\s*$/i, '')
        .trim()
      continue
    }

    // 底部提示,忽略。
    if (/^(Enter|Esc)\s+to\b/i.test(line)) continue

    // 编号行:「N.  HookType   描述」,name 与 desc 间至少 2 空格。
    const mt = line.match(/^\d+\.\s+(\S+)\s{2,}(.+)$/)
    if (mt) {
      types.push({ name: mt[1], desc: mt[2].trim() })
    }
  }

  // 防御:既无标题又无任何类型行,大概率不是 Hooks 面板。
  if (!sawTitle && types.length === 0) return null

  return { configured, note, types }
}

export function HooksCard({ data }: { data: Hooks }) {
  return (
    <div className="info">
      <Pill tone={data.configured > 0 ? 'on' : 'off'}>{data.configured} 个已配置</Pill>
      {data.note && <div className="note">{data.note}(只读,改 settings.json)</div>}
      {data.types.length > 0 ? (
        <div className="set-list">
          {data.types.map((t, i) => (
            <div className="set-row" key={i}>
              <span className="set-k">
                <span className="set-kn">{t.name}</span>
                <span className="set-d">{t.desc}</span>
              </span>
            </div>
          ))}
        </div>
      ) : (
        <div className="empty">无 hook 类型</div>
      )}
    </div>
  )
}

export const hooks: CardModule<Hooks> = { parse: parseHooks, Card: HooksCard }
