import type { CardModule } from './types'

// /skills 卡:把 Claude Code「Skills」面板抓屏解析成结构化技能清单,重渲成原生控件。
// 抓屏每行带前导空格,夹杂顶部信任提示、底部「Esc to close」等噪声,
// 行首可能有光标符「❯」或滚动符「↓」「↑」,要先剥掉。parser 必须看到标题行「Skills」,
// 缺这个关键标记就返回 null(上层回退原文)。
// 兼容两种样本:空态(「No skills found」)与有技能(若干「<名>  <2+空格>  <描述>」行)。

export interface Skill {
  name: string
  desc: string
}

export interface Skills {
  skills: Skill[]
  hint: string
}

// 剥掉行首光标/滚动符与首尾空白:抓屏每行带缩进,选中行还会冠上「❯」,滚动到边界会冠上「↓」「↑」。
const strip = (raw: string) => raw.replace(/^\s*[❯↓↑]?\s*/, '').replace(/\s+$/, '')

// 分组标题词:有技能时可能出现「User skills」「Project skills」等分组标题。简单起见不分组,直接跳过。
const isGroup = (line: string) => /^(?:User|Project|Built-in|Personal|Global|Local)\s+skills$/i.test(line)

export function parseSkills(text: string): Skills | null {
  let seenTitle = false
  let empty = false
  let hint = ''
  const skills: Skill[] = []

  for (const raw of text.split('\n')) {
    const line = strip(raw)
    if (!line) continue

    // 标题行「Skills」:关键标记。出现即记下,本行不当技能。
    if (/^Skills$/i.test(line)) {
      seenTitle = true
      continue
    }

    // 空态标记。
    if (/^No skills found$/i.test(line)) {
      empty = true
      continue
    }

    // 空态下「Create skills in …」提示行,作为 hint。
    if (/^Create skills in\b/i.test(line)) {
      hint = line
      continue
    }

    // 底部「Esc to close / Esc to go back …」等提示行,忽略。
    if (/^Esc to\b/i.test(line)) continue

    // 标题出现前的内容(信任提示等噪声)一律忽略。
    if (!seenTitle) continue

    // 分组标题(User skills / Project skills …):简单起见跳过,不建分组。
    if (isGroup(line)) continue

    // 技能行:「名称  <2+空格>  描述」。名称取首段(到首个 2+空格止),描述取其余。
    const m = line.match(/^(\S[^]*?)\s{2,}(\S.*?)$/)
    if (m) {
      skills.push({ name: m[1].trim(), desc: m[2].trim() })
      continue
    }

    // 只有名没描述的行也收(desc 留空)。排除带内部空格的多词噪声句子。
    if (/^\S+$/.test(line)) {
      skills.push({ name: line, desc: '' })
    }
  }

  // 防御:连标题都没出现,大概率不是 /skills 面板。
  if (!seenTitle) return null
  // 防御:既非空态、又没解析到任何技能,信息不足,回退原文。
  if (!empty && skills.length === 0) return null

  return { skills, hint }
}

export function SkillsCard({ data }: { data: Skills }) {
  if (data.skills.length === 0) {
    return (
      <div className="info">
        <div className="empty">未找到技能</div>
        {data.hint && <div className="note">在 .claude/skills/ 下创建技能</div>}
      </div>
    )
  }

  return (
    <div className="info">
      <div className="grp">{data.skills.length} 个技能</div>
      <div className="set-list">
        {data.skills.map((s, i) => (
          <div className="set-row" key={i}>
            <span className="set-k">
              <span className="set-kn">{s.name}</span>
              {s.desc && <span className="set-d">{s.desc}</span>}
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}

export const skills: CardModule<Skills> = { parse: parseSkills, Card: SkillsCard }
