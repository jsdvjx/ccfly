import { Component, Fragment, type ReactNode } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { Item } from './types'
import { CodeBlock as MdCodeBlock } from './blocks/CodeBlock'
import { UserBubble, AssistantBody } from './blocks/TextShell'
import { SystemNotice, classifyUserItem } from './blocks/SystemNotice'
import { renderBlock } from './blocks/router'
import { ImageChip, ImageUuidProvider } from './blocks/ImageBlock'
import { itemKey } from './store'
import { storageKey } from './config'

// ── assistant 轮注脚(仿 TUI 的「✻ Cooked… 1m 5s · 64k tokens」)───────────────
// 一轮 = 一条真·用户提问后,到下一条真·用户提问之前的全部 assistant/工具项。
// 注脚渲在该轮「最后一条 assistant item」之后,锚点 uuid = 那条 assistant 的 uuid。

// 俏皮动词池(自带几枚 + 复用 ControlBar 的一组风格)。首次随机选定、按锚点 uuid 持久化。
const FOOT_VERBS = [
  'Cooked', 'Baked', 'Sautéed', 'Simmered', 'Tinkering', 'Brewing', 'Mulling',
  'Conjured', 'Percolated', 'Noodled', 'Ruminated', 'Pondered', 'Marinated',
  'Forged', 'Hatched', 'Wrangled', 'Crunched', 'Cogitated', 'Finagled',
]

// 动词持久化:localStorage 键 `<prefix>verb:<anchorUuid>` —— 同一轮重开取回同一个词。
// 无 uuid 不持久化(每次随机,可接受);localStorage 不可用时静默回退到纯随机。
function verbFor(uuid?: string): string {
  if (!uuid) return FOOT_VERBS[Math.floor(Math.random() * FOOT_VERBS.length)]
  const key = storageKey('verb:' + uuid)
  try {
    const got = localStorage.getItem(key)
    if (got) return got
    const v = FOOT_VERBS[Math.floor(Math.random() * FOOT_VERBS.length)]
    localStorage.setItem(key, v)
    return v
  } catch {
    return FOOT_VERBS[Math.floor(Math.random() * FOOT_VERBS.length)]
  }
}

// 时长 h/m/s:3725→"1h 2m 5s"、125→"2m 5s"、42→"42s"。
function fmtFootDur(s: number): string {
  if (!Number.isFinite(s) || s < 0) s = 0
  s = Math.floor(s)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (h > 0) return h + 'h ' + m + 'm ' + sec + 's'
  if (m > 0) return m + 'm ' + sec + 's'
  return sec + 's'
}

// token 紧凑格式:1234→"1.2k"、64000→"64k"、2.1e6→"2.1M";<1000 原样;0/空→""。
function fmtFootTok(n: number): string {
  if (!n || n <= 0) return ''
  if (n >= 1e6) return +(n / 1e6).toFixed(1) + 'M'
  if (n >= 1e3) return +(n / 1e3).toFixed(1) + 'k'
  return '' + n
}

// 解析 RFC3339 时间戳为毫秒;失败返回 NaN。
function tsMs(ts?: string): number {
  if (!ts) return NaN
  return Date.parse(ts)
}

// 一条轮注脚:dim 灰小字脚注。verb 由锚点 uuid 持久化;dur=该轮起止时长;tok=该轮 assistant 输出 token 累加(为 0 省略)。
function TurnFootnote({ uuid, durSec, tokens }: { uuid?: string; durSec: number; tokens: number }) {
  const verb = verbFor(uuid)
  const tok = fmtFootTok(tokens)
  return (
    <div className="turn-foot">
      ✻ {verb} 用了 {fmtFootDur(durSec)}
      {tok ? ' · ' + tok + ' tokens' : ''}
    </div>
  )
}

// 模型短名:claude-opus-4-8 → Opus 4.8。只在「模型变化处」显示,避免每条 assistant 都刷「claude opus」字样。
export function shortModel(m?: string): string {
  if (!m) return ''
  const fam = /opus/i.test(m) ? 'Opus' : /sonnet/i.test(m) ? 'Sonnet' : /haiku/i.test(m) ? 'Haiku' : ''
  const ver = m.match(/(\d+)[-.](\d+)/)
  const v = ver ? ver[1] + '.' + ver[2] : ''
  return fam ? (v ? fam + ' ' + v : fam) : m
}

// ── Markdown 正文 ──
// 围栏代码块走批次1 的 CodeBlock(无行号散文代码块,自带复制 + 全屏阅读);行内代码仍 .inline。
export function MD({ text }: { text: string }) {
  return (
    <div className="md">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          code({ className, children }: { className?: string; children?: ReactNode }) {
            const txt = String(children ?? '').replace(/\n$/, '')
            const m = /language-(\w+)/.exec(className || '')
            if (m) return <MdCodeBlock code={txt} lang={m[1]} />
            if (txt.includes('\n')) return <MdCodeBlock code={txt} lang="" />
            return <code className="inline">{children}</code>
          },
        }}
      >
        {text}
      </ReactMarkdown>
    </div>
  )
}

// ── 可复用 transcript 渲染器(主会话与子代理共用)──
// 纯按条目顺序渲染,不做角色分组/标签去重:对话永远只有「用户↔Claude」两方,纯用颜色区分。
// 主会话(App.Session)与子代理视图(SubagentView)都用它 —— 子代理 jsonl 与主会话同构,直接复用。
// 注意:本组件不自带 SessionContext.Provider(由外层提供 host/sid),以便其中嵌套的工具卡正常工作。
// 单条消息渲染容错:任一条 block 抛错只内联降级成一行提示,绝不让整树卸载黑屏。
// (此前无任何 ErrorBoundary,某张卡渲染抛错就黑全屏——回到顶部把更老/特殊条目带进视口时尤甚。)
class MsgBoundary extends Component<{ children: ReactNode }, { err: Error | null }> {
  state: { err: Error | null } = { err: null }
  static getDerivedStateFromError(err: Error) {
    return { err }
  }
  componentDidCatch(err: Error) {
    // 留个控制台痕迹便于定位具体抛错块;不影响其余消息渲染。
    console.error('[MsgBoundary] message render failed:', err)
  }
  render() {
    if (this.state.err) {
      return <div className="msg-err">⚠️ 此条消息渲染出错(已跳过):{this.state.err.message}</div>
    }
    return this.props.children
  }
}

export function TranscriptView({ items }: { items: Item[] }) {
  const foot = computeTurnFootnotes(items)
  return (
    <>
      {items.map((it, i) => (
        // 稳定 key:必须用 itemKey(it) 而非下标 i —— 上滑前插更老消息会让下标整体偏移,
        // 用下标 key 会让同一 key 复用到不同条目(text↔tool_use,hooks 数不同)→ React hooks 崩 → 黑屏。
        // store 已按 itemKey 去重,保证当前 items 内 itemKey 唯一。
        <Fragment key={itemKey(it)}>
          <MsgBoundary>
            <Message item={it} />
          </MsgBoundary>
          {foot[i] && <TurnFootnote uuid={foot[i]!.uuid} durSec={foot[i]!.durSec} tokens={foot[i]!.tokens} />}
        </Fragment>
      ))}
    </>
  )
}

// 一个真用户提问的判定:role==='user' 且分类器判为真正用户输入(排除任务通知/命令/系统提示/中断/空)。
function isRealUser(it: Item): boolean {
  return it.role === 'user' && classifyUserItem(it).kind === 'user'
}

// 计算每条 item 后是否需挂轮注脚。轮边界:一条真用户提问起、到下一条真用户提问前的全部 assistant/工具项为一轮;
// 注脚挂在该轮「最后一条 assistant item」之后。返回 index→注脚数据(uuid 锚点 / 时长 / token 累加)的稀疏映射。
//   - 时长 = 真用户提问 ts 到该轮最后一条事件 ts 之差(秒);任一端 ts 缺失则视作 0。
//   - token = 该轮各 assistant 的 outTokens 累加。
//   - 锚点 uuid = 该轮最后一条 assistant item 的 uuid(动词持久化用)。
type FootData = { uuid?: string; durSec: number; tokens: number }
function computeTurnFootnotes(items: Item[]): Array<FootData | null> {
  const out: Array<FootData | null> = new Array(items.length).fill(null)
  let userIdx = -1 // 当前轮的真用户提问下标(-1 = 尚未进入任何用户轮)
  let lastAsstIdx = -1 // 当前轮已见的最后一条 assistant 下标
  let tokens = 0 // 当前轮 assistant 输出 token 累加
  let lastEventIdx = -1 // 当前轮已见的最后一条事件下标(算时长用,含工具回传)

  const flush = () => {
    if (userIdx < 0 || lastAsstIdx < 0) return // 没有用户起点或本轮无 assistant → 不挂注脚
    const startMs = tsMs(items[userIdx].ts)
    const endMs = tsMs(items[lastEventIdx >= 0 ? lastEventIdx : lastAsstIdx].ts)
    const durSec = Number.isFinite(startMs) && Number.isFinite(endMs) ? Math.max(0, (endMs - startMs) / 1000) : 0
    out[lastAsstIdx] = { uuid: items[lastAsstIdx].uuid, durSec, tokens }
  }

  for (let i = 0; i < items.length; i++) {
    const it = items[i]
    if (isRealUser(it)) {
      flush() // 收尾上一轮
      userIdx = i
      lastAsstIdx = -1
      lastEventIdx = i
      tokens = 0
      continue
    }
    if (userIdx < 0) continue // 首个真用户之前的项(系统前导等)不计入任何轮
    lastEventIdx = i
    if (it.role === 'assistant') {
      lastAsstIdx = i
      tokens += it.outTokens || 0
    }
  }
  flush() // 收尾最后一轮
  return out
}

// ── 一条消息(薄壳)──
// 不再渲染角色文字标签:用户=蓝(.msg.user 蓝底+蓝左条)、Claude=中性(.msg.asst 灰左条),纯靠颜色区分。
// 各 block 分发:user 文本 → UserBubble、assistant 文本 → AssistantBody、其余(thinking/tool_use)→ renderBlock。
export function Message({ item }: { item: Item }) {
  const blocks = item.blocks || []
  if (item.role === 'user') {
    const hasText = blocks.some((b) => b.type === 'text' && b.text && b.text.trim())
    // 只渲染 base64 真图(无 path);路径式图块是 isMeta 副本(见 classifyUserItem),一律跳过。
    const imgs = blocks.filter((b) => b.type === 'image' && !b.path)
    if (!hasText && imgs.length === 0) return null // 纯 tool_result 载体 / 纯路径图副本:不单独渲染
    // 「伪用户」识别:任务通知 / 斜杠命令 / 系统提示 / 中断 → 弱化通知控件。
    // 真正用户输入(含图片)→ 走蓝气泡,文字 + 图片缩略 chip 同条,内嵌系统片段已被剥离。
    const cls = classifyUserItem(item)
    if (cls.kind === 'other') return null
    if (cls.kind !== 'user') {
      return (
        <div className="msg sys">
          <SystemNotice cls={cls} />
        </div>
      )
    }
    return (
      <div className="msg user">
        <div className="bubble">
          {cls.userText.trim() && <UserBubble text={cls.userText} />}
          {imgs.length > 0 && (
            <ImageUuidProvider value={item.uuid || ''}>
              <div className="ic-row">
                {imgs.map((b, i) => (
                  <ImageChip key={i} block={b} />
                ))}
              </div>
            </ImageUuidProvider>
          )}
        </div>
      </div>
    )
  }
  // assistant
  return (
    <div className="msg asst">
      <div className="bubble">
        {blocks.map((b, i) => {
          if (b.type === 'text') return <AssistantBody key={i} text={b.text || ''} />
          return renderBlock(b, i)
        })}
      </div>
    </div>
  )
}
