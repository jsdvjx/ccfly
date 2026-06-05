import { useEffect, useState } from 'react'
import { sendKeys, tmuxName } from '../api'
import { AnsiText, stripAnsi } from '../blocks/Ansi'
import { MD } from '../components'
import { cardFor, groupOf, type CmdCard } from './registry'
import { useCapture, relTime, type Capture } from './useCapture'

// 唯一面板 —— 取代旧 CostPanel + CmdSheet。Head + CardBody 被 single/tabs 共用;入口按 groupOf 长度分流。

// 按结局渲染:loading/busy/offline/notfound 各给明确状态;ok 才解析渲卡(解析不出回退原文)。
function CardBody({ card, cap }: { card: CmdCard; cap: Capture }) {
  if (cap.phase === 'loading') return <div className="empty">加载中…</div>
  if (cap.phase === 'offline') return <div className="empty">会话未在运行</div>
  if (cap.phase === 'busy') return <div className="empty">⏳ 会话生成中,结束后点「刷新」重试</div>
  if (cap.phase === 'notfound') {
    // cap.out 带色:原始回退用 AnsiText 渲染 TUI 原色,而非纯灰 pre。
    return cap.raw ? (
      <pre className="cmd-out">{cap.out ? <AnsiText text={cap.out} /> : '(空)'}</pre>
    ) : (
      <div className="empty">未能打开「{card.label}」—— 可能被其他界面挡住或里世界未响应。点「刷新」重试,或「原始」看屏幕。</div>
    )
  }
  // jsonl 路径:out 是干净 markdown。「原始」看源码;否则朴素 <MD> 渲染。
  if (cap.md) {
    if (cap.raw) return <pre className="cmd-out">{cap.out || '(空)'}</pre>
    return <MD text={cap.out} />
  }
  // 抓屏卡:结构化卡吃剥色文本(用自己的色);「原始」/解析失败回退用带色 AnsiText 渲染。
  const data = cap.raw ? null : card.mod.parse(stripAnsi(cap.out))
  const Card = card.mod.Card
  return data ? <Card data={data} /> : <pre className="cmd-out">{cap.out ? <AnsiText text={cap.out} /> : '(空)'}</pre>
}

// 头部:单行三按钮(原始/控件 + 刷新 + 关闭),保持现有 .sheet-hbtns 布局。single/tabs 共用。
function Head({ title, cap, onClose }: { title: string; cap: Capture; onClose: () => void }) {
  return (
    <div className="sheet-h">
      <span>{title}</span>
      <div className="sheet-hbtns">
        {(cap.phase === 'ok' || cap.phase === 'notfound') && (
          <button className="cbtn" onClick={() => cap.setRaw(!cap.raw)}>
            {cap.raw ? '控件' : '原始'}
          </button>
        )}
        <button className="cbtn" onClick={() => cap.run()}>
          刷新
        </button>
        <button className="cbtn" onClick={onClose}>
          关闭
        </button>
      </div>
    </div>
  )
}

// 刷新状态条(SWR 提示):紧贴 Head 下方,深色紧凑、与现风格一致。
//  · 正在抓新数据(revalidating)→ 脉动点 +「上次刷新于 X · 正在加载新数据…」(有旧 ts);无旧数据则「正在加载…」
//  · 抓完(有 ts、非 revalidating)→「刷新于 X」+「刷新」轻按钮(等价 Head 的刷新,就近可点)
//  · 完全无数据(ts=0 且不在抓)→ 不显条(CardBody 自有 loading/notfound/offline 文案)
function StatusBar({ cap }: { cap: Capture }) {
  // 让相对时间随面板停留自动走字(每 30s 重渲一次即可)。
  const [, tick] = useState(0)
  useEffect(() => {
    const id = window.setInterval(() => tick((n) => n + 1), 30000)
    return () => clearInterval(id)
  }, [])
  if (cap.revalidating) {
    return (
      <div className="info-status loading">
        <span className="info-spin" aria-hidden />
        <span className="info-status-txt">{cap.ts ? `上次刷新于 ${relTime(cap.ts)} · 正在加载新数据…` : '正在加载…'}</span>
      </div>
    )
  }
  if (cap.ts && (cap.phase === 'ok' || cap.phase === 'notfound')) {
    return (
      <div className="info-status">
        <span className="info-status-txt">刷新于 {relTime(cap.ts)}</span>
        <button className="info-status-refresh" onClick={() => cap.run()}>刷新</button>
      </div>
    )
  }
  return null
}

// 单卡:清场/收场的 Esc 次数由 card.reach.esc 决定(modal 默认 1)。= 旧 CmdSheet。
function SingleSheet({ host, sid, card, onClose }: { host: string; sid: string; card: CmdCard; onClose: () => void }) {
  const cap = useCapture(host, sid, card, false)
  return (
    <div className="sheet">
      <div className="sheet-box" onClick={(e) => e.stopPropagation()}>
        <Head title={card.cmd} cap={cap} onClose={onClose} />
        <StatusBar cap={cap} />
        <div className="sheet-list">
          <CardBody card={card} cap={cap} />
        </div>
      </div>
    </div>
  )
}

// 多卡(tabs):切 tab 换 card → useCapture 以 card.cmd 为键重跑 / 命中缓存;清场 Esc 次数由各卡 reach.esc 定。
// 初始落在被点中的那张卡(/cost→用量、/status→状态);close 统一发 Esc 关里世界停留的面板。= 旧 CostPanel。
function TabsSheet({
  host,
  sid,
  tabs,
  start,
  onClose,
}: {
  host: string
  sid: string
  tabs: CmdCard[]
  start: number
  onClose: () => void
}) {
  const [cur, setCur] = useState(start)
  const card = tabs[cur]
  const cap = useCapture(host, sid, card, true)
  const close = () => {
    const n = card.reach.esc ?? 1 // 关里世界面板:Esc 次数取当前 tab 的 reach.esc(/config 搜索框=2,其余=1)
    if (n > 0) sendKeys(host, tmuxName(sid), { keys: Array(n).fill('Escape') })
    onClose()
  }
  return (
    <div className="sheet">
      <div className="sheet-box" onClick={(e) => e.stopPropagation()}>
        <Head title={card.group!} cap={cap} onClose={close} />
        <div className="ctabs">
          {tabs.map((t, i) => (
            <button key={t.cmd} className={'ctab' + (i === cur ? ' on' : '')} onClick={() => setCur(i)}>
              {t.label}
            </button>
          ))}
        </div>
        <StatusBar cap={cap} />
        <div className="sheet-list">
          <CardBody card={card} cap={cap} />
        </div>
      </div>
    </div>
  )
}

// 唯一入口:App 只渲这个。groupOf 长度自然分流 single / tabs;tabs 落在被点中的卡。
export function InfoSheet({ host, sid, cmd, onClose }: { host: string; sid: string; cmd: string; onClose: () => void }) {
  const card = cardFor(cmd)
  if (!card) return null
  const grp = groupOf(card)
  // key=cmd:换命令必重挂载,避免复用残留的 cur/缓存(即便当前总经 null 切换,也防未来「面板开着切命令」)。
  if (grp.length > 1) {
    return <TabsSheet key={card.cmd} host={host} sid={sid} tabs={grp} start={Math.max(0, grp.indexOf(card))} onClose={onClose} />
  }
  return <SingleSheet key={card.cmd} host={host} sid={sid} card={card} onClose={onClose} />
}
