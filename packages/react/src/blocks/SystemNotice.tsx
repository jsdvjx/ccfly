// 「伪用户」识别与弱化展示。前缀 sn-。
// 会话 jsonl 里 role==='user' 的条目里,很多不是用户主动输入:任务通知 / 斜杠命令及其本地输出 /
// system-reminder / 中断标记 / 纯图片附件元信息。它们历史上被渲成「你」的气泡,不合理。
// 本模块:① 纯函数分类器 classifyUserItem;② 弱化的 SystemNotice 控件(dim、紧凑、可折叠)。
// 真正的用户输入(含「真话 + 内嵌系统片段」)仍走 UserBubble,只把系统片段剥离/折叠。
import { useState } from 'react'
import type { Item } from '../types'
import { MD } from '../components'
import { Collapsible } from './shell'
import { AnsiText, stripAnsi } from './Ansi'

// ── 分类结果 ──
// user        :真正的用户主动输入(可能带内嵌系统片段,需剥离后入气泡;含图片也走这里,文字+图片同条)
// notification:子代理/动态工作流任务通知 <task-notification>
// command     :斜杠命令调用 <command-name>… 或其本地输出 <local-command-stdout> / caveat
// system      :system-reminder 等系统提示(独立成条时)
// interrupt   :用户中断标记 [Request interrupted…]
// other       :空 / 无法归类
export type UserItemKind = 'user' | 'notification' | 'command' | 'system' | 'interrupt' | 'other'

export interface UserClassification {
  kind: UserItemKind
  // 真正给用户气泡看的「用户真话」(已剥离内嵌系统片段);非 user 类型时为空。
  userText: string
  // 非 user 类型的展示载荷:标题(命令名/标签)+ 折叠正文。
  label?: string
  body?: string
  // body 是否带 TUI 原始 ANSI(命令输出):true → 用 AnsiText 渲染保留原色;否则走 MD。
  ansi?: boolean
}

// ── 判别正则(以实测 jsonl 为准)──
const RE_TASK_NOTIFY = /<task-notification>/
const RE_CMD_NAME = /<command-name>/
const RE_CMD_STDOUT = /<local-command-stdout>/
const RE_CMD_CAVEAT = /<local-command-caveat>/
const RE_SYS_REMINDER = /<system-reminder>/
// 中断:整条仅为 [Request interrupted …](允许首尾空白)。
const RE_INTERRUPT_ONLY = /^\s*\[Request interrupted[^\]]*\]\s*$/
// 内嵌于用户真话里的图片占位 [Image #N] —— 这是真用户消息的附件标记,只清除占位、不改判类型。
const RE_IMAGE_TOKEN = /\[Image #\d+\]/g
// 剥离独立的 <system-reminder>…</system-reminder> 段(用户真话末尾常被附带系统提示)。
const RE_SYS_REMINDER_BLOCK = /<system-reminder>[\s\S]*?<\/system-reminder>/g

// 取首尾配对标签里的内容(贪婪到最后一个闭合,容忍正文里出现转义的 < )。
function tagInner(text: string, tag: string): string {
  const m = text.match(new RegExp('<' + tag + '>([\\s\\S]*)</' + tag + '>'))
  return m ? m[1].trim() : ''
}

// ── 分类器(纯函数)──
// 优先识别「整条即一个包裹标签」的伪用户类型;其余按真用户处理,顺手剥离内嵌系统片段。
export function classifyUserItem(item: Item): UserClassification {
  // 图片去重(实测数据规律):用户贴图时 jsonl 存两份——
  //   · 真消息(isMeta 无):含 base64 image 块 → 后端转出 {type:image, mediaType, imgIdx}(无 path),要渲染;
  //   · isMeta:true 副本:含路径式 text 块 [Image: source: …] → 后端转出 {type:image, path, imgIdx}(有 path),是重复副本。
  // 故仅 base64 图块(无 path)算「可渲染真图」;纯路径图块条目(无 base64、无实义文字)= isMeta 副本,判为 other 不渲染。
  // (若将来出现「纯路径无 base64」的真图,这里会误抑制——届时需回退渲染 path 形。)
  const hasImage = (item.blocks || []).some((b) => b.type === 'image' && !b.path)
  // 取这条 user 消息的全部 text block 拼成判别文本(伪用户类型在本会话实测均为单 text block)。
  const raw = (item.blocks || [])
    .filter((b) => b.type === 'text' && b.text)
    .map((b) => b.text as string)
    .join('\n')
    .trim()
  // 无实义文字:有 base64 真图 → 真用户消息;否则(纯路径副本 / 全空)→ other 不渲染。
  if (!raw) return hasImage ? { kind: 'user', userText: '' } : { kind: 'other', userText: '' }

  // 1) 任务通知:动态工作流 / 子代理回包。
  if (RE_TASK_NOTIFY.test(raw)) {
    const summary = tagInner(raw, 'summary')
    return { kind: 'notification', userText: '', label: '任务通知', body: summary ? summary + '\n\n' + raw : raw }
  }

  // 2) 斜杠命令调用:<command-name>/x</command-name> + message/args。
  if (RE_CMD_NAME.test(raw)) {
    const name = tagInner(raw, 'command-name')
    const args = tagInner(raw, 'command-args')
    return { kind: 'command', userText: '', label: name || '命令', body: args }
  }

  // 3) 斜杠命令的本地输出 / caveat(与 command-name 不同条,单独成立)。
  //    判别仍吃剥色;但命令输出正文保留原始 ANSI(ansi:true)→ 由 AnsiText 还原 TUI 原色。
  if (RE_CMD_STDOUT.test(raw) || RE_CMD_CAVEAT.test(raw)) {
    const out = tagInner(raw, 'local-command-stdout')
    if (out) return { kind: 'command', userText: '', label: '命令输出', body: out, ansi: true }
    const caveat = tagInner(raw, 'local-command-caveat')
    return { kind: 'command', userText: '', label: '命令输出', body: caveat || stripAnsi(raw) }
  }

  // 4) 用户中断标记(整条即标记)。
  if (RE_INTERRUPT_ONLY.test(raw)) {
    return { kind: 'interrupt', userText: '', label: '已中断', body: raw.trim() }
  }

  // 5) 整条即 system-reminder(独立成条;真用户消息内嵌的 reminder 在下方剥离而非改判)。
  if (RE_SYS_REMINDER.test(raw) && !hasUserProseBesidesReminder(raw)) {
    return { kind: 'system', userText: '', label: '系统提示', body: tagInner(raw, 'system-reminder') || raw }
  }

  // 6) 真正的用户输入:剥离内嵌系统片段 + 清掉图片占位,留下用户真话。
  const userText = stripSystemFragments(raw)
  // 文字被剥空但有图片 → 仍是真用户消息(纯图片),保留 user 类型让气泡渲染图片块。
  if (!userText) return hasImage ? { kind: 'user', userText: '' } : { kind: 'other', userText: '' }
  return { kind: 'user', userText }
}

// 剥掉内嵌 system-reminder 后是否仍有用户实质正文(用于区分「整条系统提示」vs「用户真话+附带提示」)。
function hasUserProseBesidesReminder(raw: string): boolean {
  return raw.replace(RE_SYS_REMINDER_BLOCK, '').trim().length > 0
}

// 从用户真话里剥掉内嵌系统片段(system-reminder 段)与图片占位,返回纯用户文本。
function stripSystemFragments(raw: string): string {
  return raw
    .replace(RE_SYS_REMINDER_BLOCK, '')
    .replace(RE_IMAGE_TOKEN, '')
    .trim()
}

// ── 各伪用户类型的图标(小、低存在感)──
const ICONS: Record<Exclude<UserItemKind, 'user' | 'other'>, string> = {
  notification: '🔔',
  command: '⌘',
  system: '⚙',
  interrupt: '⛔',
}

// ── 弱化系统/通知/命令控件 ──
// 一行头部(小图标 + 类型标签 + 可选 brief)+ 可折叠正文。整体 dim、不占「你」的角色头。
export interface SystemNoticeProps {
  cls: UserClassification
}
export function SystemNotice({ cls }: SystemNoticeProps) {
  const [open, setOpen] = useState(false)
  if (cls.kind === 'user' || cls.kind === 'other') return null
  const icon = ICONS[cls.kind]
  const body = (cls.body || '').trim()
  const interrupt = cls.kind === 'interrupt'
  // 中断:无可展开正文,单行徽标即止;其余:头部可点展开折叠正文。
  const foldable = !interrupt && !!body
  const lineCount = body ? body.split('\n').length : 0

  return (
    <div className={'sn sn--' + cls.kind}>
      <div
        className={'sn-head' + (foldable ? ' sn-clk' : '')}
        onClick={foldable ? () => setOpen((o) => !o) : undefined}
        role={foldable ? 'button' : undefined}
      >
        <span className="sn-icon">{icon}</span>
        <span className="sn-label">{cls.label}</span>
        {/* 命令名 / 输出首行作 brief 预览(剥色,避免转义码漏进单行徽标)*/}
        {!interrupt && body && (
          <span className="sn-brief">{stripAnsi(body.split('\n')[0])}</span>
        )}
        {foldable && <span className="sn-chev">{open ? '▾' : '▸'}</span>}
      </div>
      {foldable && open && (
        <div className="sn-body">
          {cls.kind === 'notification' ? (
            <Collapsible lines={10} count={lineCount} fade>
              <pre className="sn-pre">{body}</pre>
            </Collapsible>
          ) : cls.ansi ? (
            // 命令输出带 TUI 原始 ANSI:AnsiText 还原原色(/model 的 bold、/context 彩色格子图)。
            <Collapsible lines={10} count={lineCount} fade>
              <pre className="cmd-out">
                <AnsiText text={body} />
              </pre>
            </Collapsible>
          ) : (
            <Collapsible lines={10} count={lineCount} fade>
              <MD text={body} />
            </Collapsible>
          )}
        </div>
      )}
    </div>
  )
}
