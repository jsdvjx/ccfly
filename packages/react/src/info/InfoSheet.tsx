import { useEffect, useState } from 'react'
import { AnsiText, stripAnsi } from '../blocks/Ansi'
import { MD } from '../components'
import { cardFor, groupOf, type CmdCard } from './registry'
import { useCapture, useSweep, relTime, type Capture, type SweepState } from './useCapture'

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
  // jsonl 路径:out 是干净 markdown。「原始」看源码;否则优先用本卡的 Md(结构化卡,如 /context),
  // 缺省朴素 <MD> 渲染。
  if (cap.md) {
    if (cap.raw) return <pre className="cmd-out">{cap.out || '(空)'}</pre>
    const Md = card.mod.Md
    return Md ? <Md md={cap.out} /> : <MD text={cap.out} />
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

// 扫页卡身:渲染某 tab 的已扫结果(结构卡);没扫到则按 phase 给 loading/offline/busy/未抓到。
function SweepBody({ card, hit, phase }: { card: CmdCard; hit?: { data: unknown; raw: string }; phase: SweepState['phase'] }) {
  if (hit) {
    const Card = card.mod.Card // CardModule<any> → 吃任意 data
    return <Card data={hit.data} />
  }
  if (phase === 'loading') return <div className="empty">加载中…</div>
  if (phase === 'offline') return <div className="empty">会话未在运行</div>
  if (phase === 'busy') return <div className="empty">⏳ 会话生成中,结束后点「刷新」重试</div>
  return <div className="empty">未抓到「{card.label}」页 —— 点「刷新」重扫一遍。</div>
}

// 多卡(tabs):一次开 /cost 面板、后台 ← → 扫遍所有页(useSweep),全部抓好后切 tab 即时(读已抓结果、
// 不再发命令)。= 重写旧 TabsSheet 的「切 tab 才抓」(覆盖网下不可靠 → 用户反馈「用量可以但没法切换」)。
// 关闭无需再发 Esc:扫完已自行关掉面板、回到干净输入态。
function SweepSheet({
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
  const { found, phase, run, ts } = useSweep(host, sid, tabs)
  const card = tabs[cur]
  const done = Object.keys(found).length
  return (
    <div className="sheet">
      <div className="sheet-box" onClick={(e) => e.stopPropagation()}>
        <div className="sheet-h">
          <span>{tabs[0].group}</span>
          <div className="sheet-hbtns">
            <button className="cbtn" onClick={() => run()}>刷新</button>
            <button className="cbtn" onClick={onClose}>关闭</button>
          </div>
        </div>
        <div className="ctabs">
          {tabs.map((t, i) => (
            <button
              key={t.cmd}
              className={'ctab' + (i === cur ? ' on' : '') + (found[t.cmd] ? '' : ' ctab-pending')}
              onClick={() => setCur(i)}
            >
              {t.label}
              {!found[t.cmd] && phase === 'loading' ? ' …' : ''}
            </button>
          ))}
        </div>
        {phase === 'loading' ? (
          <div className="info-status loading">
            <span className="info-spin" aria-hidden />
            <span className="info-status-txt">正在浏览各页… {done}/{tabs.length}</span>
          </div>
        ) : ts ? (
          <div className="info-status">
            <span className="info-status-txt">刷新于 {relTime(ts)}</span>
            <button className="info-status-refresh" onClick={() => run()}>刷新</button>
          </div>
        ) : null}
        <div className="sheet-list">
          <SweepBody card={card} hit={found[card.cmd]} phase={phase} />
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
  // 多卡组(/cost 的会话信息:用量/状态/统计/设置)→ 扫页卡(一次开面板、← → 扫遍所有页、切 tab 即时)。
  if (grp.length > 1) {
    return <SweepSheet key={card.cmd} host={host} sid={sid} tabs={grp} start={Math.max(0, grp.indexOf(card))} onClose={onClose} />
  }
  return <SingleSheet key={card.cmd} host={host} sid={sid} card={card} onClose={onClose} />
}
