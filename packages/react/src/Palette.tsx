import { useMemo, useState } from 'react'

// 「信息类命令」判定(/cost /status /mcp …):由调用方注入 isInfoCmd,缺省「永不是信息命令」
// —— 即所有命令都走 onPick 透传进里世界。info/* 子树已抽进本包(info/registry 派生 isInfoCmd),
// ControlBar 缺省即用它接线、SessionView 用 onRunCmd 开 InfoSheet;此处保留可注入,方便消费方
// 自组控件层时关闭信息卡(传 `() => false`)或自定义命令表。

// 斜杠命令清单:tier='common' 为常用(默认显示);其余按 g 分组收进「更多」。
// 单一数据源,增删一行即可。来源:Claude Code 内置命令 + 本环境内置技能。
interface Slash {
  cmd: string
  label: string
  tier?: 'common'
  g?: string
}
const SLASH: Slash[] = [
  { cmd: '/clear', label: '清空对话', tier: 'common' },
  { cmd: '/compact', label: '压缩上下文', tier: 'common' },
  { cmd: '/context', label: '上下文用量', tier: 'common' },
  { cmd: '/model', label: '切换模型', tier: 'common' },
  { cmd: '/effort', label: '调整力度', tier: 'common' },
  { cmd: '/resume', label: '继续会话', tier: 'common' },
  { cmd: '/cost', label: '用量与额度', tier: 'common' },
  { cmd: '/agents', label: '子代理', tier: 'common' },

  { cmd: '/rewind', label: '回退检查点', g: '会话' },
  { cmd: '/memory', label: '编辑记忆 / CLAUDE.md', g: '会话' },
  { cmd: '/export', label: '导出对话', g: '会话' },
  { cmd: '/add-dir', label: '添加工作目录', g: '会话' },

  { cmd: '/mcp', label: 'MCP 服务', g: '工具' },
  { cmd: '/skills', label: '技能列表', g: '工具' },
  { cmd: '/hooks', label: '钩子', g: '工具' },
  { cmd: '/bashes', label: '后台 shell', g: '工具' },

  { cmd: '/config', label: '设置', g: '配置' },
  { cmd: '/permissions', label: '权限规则', g: '配置' },
  { cmd: '/status', label: '状态', g: '配置' },
  { cmd: '/doctor', label: '体检', g: '配置' },
  { cmd: '/vim', label: 'Vim 模式', g: '配置' },
  { cmd: '/terminal-setup', label: '终端按键设置', g: '配置' },
  { cmd: '/release-notes', label: '更新日志', g: '配置' },
  { cmd: '/init', label: '生成 CLAUDE.md', g: '配置' },
  { cmd: '/login', label: '登录', g: '配置' },
  { cmd: '/logout', label: '登出', g: '配置' },
  { cmd: '/help', label: '帮助', g: '配置' },

  { cmd: '/code-review', label: '代码审查', g: '技能' },
  { cmd: '/security-review', label: '安全审查', g: '技能' },
  { cmd: '/deep-research', label: '深度研究', g: '技能' },
  { cmd: '/verify', label: '验证改动', g: '技能' },
  { cmd: '/simplify', label: '简化代码', g: '技能' },
  { cmd: '/run', label: '运行应用', g: '技能' },
  { cmd: '/schedule', label: '定时任务', g: '技能' },
  { cmd: '/loop', label: '循环执行', g: '技能' },
]

const GROUPS = ['会话', '工具', '配置', '技能']

// 信息类命令:输出是 claude 本地渲染(不进 jsonl)、跑完即回输入态 → 里世界没有可映射的控件。
// 故这些不透传进里世界,而是直接打开表世界原生卡统一展示。哪些算信息命令 → 由 info/registry 单一真相派生。

export function SlashPalette({
  onPick,
  onClose,
  onRun,
  isInfoCmd = () => false,
}: {
  onPick: (cmd: string) => void
  onClose: () => void
  onRun: (cmd: string) => void
  // 命令是否为「信息类」(走 onRun 抓屏展示)。缺省「永不是」→ 所有命令透传进里世界。
  isInfoCmd?: (cmd: string) => boolean
}) {
  const [q, setQ] = useState('')
  const [more, setMore] = useState(false)
  const [pending, setPending] = useState<Slash | null>(null)
  const ql = q.trim().toLowerCase()

  const filtered = useMemo(
    () => (ql ? SLASH.filter((a) => a.label.toLowerCase().includes(ql) || a.cmd.toLowerCase().includes(ql)) : null),
    [ql],
  )

  const row = (a: Slash) => {
    const info = isInfoCmd(a.cmd)
    return (
      <button
        key={a.cmd}
        className="act"
        onClick={(e) => {
          e.preventDefault()
          if (info) { onRun(a.cmd); onClose() } // 信息类 → 跑命令抓屏展示真实输出
          else setPending(a)
        }}
      >
        <span className="act-l">{a.label}</span>
        <span className="act-r">{info ? 'ⓘ 信息' : a.cmd}</span>
      </button>
    )
  }

  return (
    <div className="sheet">
      <div className="sheet-box">
        <div className="sheet-h">
          <span>命令</span>
          <button className="cbtn" onClick={(e) => { e.preventDefault(); onClose() }}>✕</button>
        </div>
        <input
          className="cinput"
          placeholder="筛选动作…"
          value={q}
          autoCapitalize="off"
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => setQ(e.target.value)}
        />
        <div className="sheet-list">
          {filtered ? (
            filtered.length ? filtered.map(row) : <div className="empty">无匹配</div>
          ) : (
            <>
              <div className="actg">常用</div>
              {SLASH.filter((a) => a.tier === 'common').map(row)}
              <button className="act more" onClick={(e) => { e.preventDefault(); setMore(!more) }}>
                <span className="act-l">{more ? '收起' : '更多命令'}</span>
                <span className="act-r">{more ? '▾' : '▸'}</span>
              </button>
              {more &&
                GROUPS.map((g) => (
                  <div key={g}>
                    <div className="actg">{g}</div>
                    {SLASH.filter((a) => a.g === g).map(row)}
                  </div>
                ))}
            </>
          )}
        </div>
      </div>

      {pending && (
        <div className="cfm">
          <div className="cfm-box">
            <div className="cfm-msg">确认「{pending.label}」?  将发送 {pending.cmd}</div>
            <div className="cfm-btns">
              <button className="cbtn" onClick={(e) => { e.preventDefault(); setPending(null) }}>取消</button>
              <button
                className="cbtn primary"
                onClick={(e) => { e.preventDefault(); onPick(pending.cmd); setPending(null); onClose() }}
              >
                确认
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
