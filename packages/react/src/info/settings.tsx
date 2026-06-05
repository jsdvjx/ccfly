import { Pill } from './shared'
import type { CardModule } from './types'

// /config 卡:把 Claude Code Settings 面板抓屏解析成结构化键值,重渲成原生控件(只读展示)。
// 抓屏每行带前导空格,夹杂 tab 栏「Settings Status …」、搜索框边框(╭│╰、⌕ Search settings…)、
// 空行、底部提示(←/→/tab to switch · …)等噪声,parser 只盯「名称  <2+空格>  值」标记行。
// 注:交互式开关(点 Pill 切 true/false)为后续工作,本卡纯展示。

export interface SettingItem {
  k: string
  v: string
}

export interface SettingsData {
  items: SettingItem[]
  more: number
}

// 英文设置键 → 中文说明(一览)。键以 /config 面板原文为准。
const DESC: Record<string, string> = {
  'Auto-compact': '上下文将满时自动压缩',
  'Show tips': '显示使用提示',
  'Reduce motion': '减弱动画效果',
  'Thinking mode': '显示模型思考过程',
  'Prompt suggestions': '输入时给出建议',
  'Session recap': '会话回顾摘要',
  'Rewind code (checkpoints)': '代码检查点(可回退)',
  'Dynamic workflows': '动态工作流',
  'Ultracode keyword trigger': 'Ultracode 关键词触发多 agent',
  'Verbose output': '详细输出',
  'Terminal progress bar': '终端进度条',
  'Show turn duration': '显示每轮耗时',
  'Default permission mode': '默认权限模式',
  'Worktree base ref': '工作树基准引用',
  'Use auto mode during plan': '计划模式下也用自动模式',
  'Respect .gitignore in file picker': '文件选择器遵守 .gitignore',
  'Skip the /copy picker': '跳过 /copy 选择器',
  'Copy on select': '选中即复制',
  'Auto-scroll': '自动滚动到底部',
  'Open agents view by default': '默认打开子代理视图',
  '← opens agents': '左方向键打开子代理',
  'Auto-update channel': '自动更新通道',
  Theme: '配色主题',
  'Local notifications': '本地通知',
  'Push when actions required': '需要操作时推送通知',
  'Push when Claude decides': 'Claude 认为合适时推送',
  'Output style': '输出风格',
  Language: '界面语言',
  'Editor mode': '编辑器模式(normal/vim)',
  'Show last response in external editor': '在外部编辑器查看上次回复',
  'Show PR status footer': '显示 PR 状态栏',
  Model: '当前模型',
  'Auto-connect to IDE (external terminal)': '外部终端自动连接 IDE',
  'Claude in Chrome enabled by default': '默认启用 Claude in Chrome',
  'Enable Remote Control for all sessions': '对所有会话启用远程控制',
}

export function parseSettings(text: string): SettingsData | null {
  const items: SettingItem[] = []
  let more = 0

  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line) continue

    // 跳过 tab 栏「Settings Status Config Usage Stats」——它也是「字母+2空格+值」,会被误当成一条设置。
    if (/Settings\s+Status\s+Config\s+Usage\s+Stats/.test(line)) continue

    // 底部「↓ 16 more below」取数。
    const mb = line.match(/↓\s*(\d+)\s+more\s+below/i)
    if (mb) {
      more = Number(mb[1])
      continue
    }

    // 名称必须以字母开头;名称与值之间至少 2 个空格;值取最后一段。
    const m = line.match(/^([A-Za-z][^]*?)\s{2,}(\S.*?)$/)
    if (!m) continue
    const k = m[1].trim()
    const v = m[2].trim()
    if (!k || !v) continue
    items.push({ k, v })
  }

  // 防御:像样的设置项不足 3 条,大概率不是 Settings 面板。
  if (items.length < 3) return null

  return { items, more }
}

export function SettingsCard({ data }: { data: SettingsData }) {
  return (
    <div className="info">
      <div className="set-list">
        {data.items.map((it, i) => (
          <div className="set-row" key={i}>
            <span className="set-k">
              <span className="set-kn">{it.k}</span>
              {DESC[it.k] && <span className="set-d">{DESC[it.k]}</span>}
            </span>
            {it.v === 'true' ? (
              <Pill tone="on">开</Pill>
            ) : it.v === 'false' ? (
              <Pill tone="off">关</Pill>
            ) : (
              <Pill>{it.v}</Pill>
            )}
          </div>
        ))}
      </div>
      {data.more > 0 && <div className="set-more">还有 {data.more} 项(在终端展开)</div>}
    </div>
  )
}

export const settings: CardModule<SettingsData> = { parse: parseSettings, Card: SettingsCard }
