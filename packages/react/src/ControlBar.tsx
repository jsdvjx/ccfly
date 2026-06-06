import { useEffect, useRef, useState, type ReactNode } from 'react'
import { fetchState, startSession, tmuxName, type CtrlState } from './api'
import { useLiveState, useLiveDegraded } from './livestate'
import { sendAct } from './sendkeys'
import { SlashPalette } from './Palette'
import { isInfoCmd as registryIsInfoCmd } from './info/registry'
import { storageKey, getConfig } from './config'

// ── 还原原版 TUI:工作中 spinner/动词/计时;空闲态不再轮换 tips(已删)──────────
const SPIN = ['✶', '✳', '✺', '✻', '✽', '✻', '✺', '✳'] // 脉动星(仿 claude 的 loading 动画)
// claude 那串俏皮的工作动词;里世界抓不到实时动词时,前端轮换还原那股劲儿。
const VERBS = [
  'Cogitating', 'Conjuring', 'Simmering', 'Percolating', 'Noodling', 'Herding', 'Ruminating',
  'Finagling', 'Pondering', 'Vibing', 'Schlepping', 'Spelunking', 'Wrangling', 'Crunching',
  'Marinating', 'Brewing', 'Forging', 'Hatching', 'Mulling', 'Sussing', 'Tinkering', 'Working',
]

// 完整 h/m/s:大于 1h 出小时段,大于 1m 出分钟段,否则纯秒。
//   3725 → "1h 2m 5s" · 125 → "2m 5s" · 42 → "42s"
const fmtDur = (s: number) => {
  if (s < 0) s = 0
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (h > 0) return h + 'h ' + m + 'm ' + sec + 's'
  if (m > 0) return m + 'm ' + sec + 's'
  return sec + 's'
}

// live elapsed 形如 "7s"(后端/客户端只解析秒)→ 取秒数;解析不到则 null。
const parseElapsedSec = (e?: string): number | null => {
  if (!e) return null
  const m = /(\d+)\s*s/.exec(e)
  return m ? parseInt(m[1], 10) : null
}

// 力度 chip:常驻显示「最近一次已知的 effort」。
// 注意:Claude Code 普通输入态并不暴露当前思考力度——CtrlState.effort 只在用户开 /effort 面板
// (select 态、reEffort 命中)那一刻才有值,input 态恒为 undefined。故 chip 不能直接读 st.effort
// (那样在输入态永远不上屏)。务实折衷:每次任意 CtrlState 带 effort,就把它缓存进 localStorage
// (按 host+sid 分键,跨会话记忆);input 态的 chip 读这个缓存值显示。
// 因此 chip 展示的是「最近已知力度」,并非实时——屏幕在输入态确实不报力度。
// effort 文本里抓出 minimal/low/medium/high/max 关键词上色;抓不到就原样显示。
const EFFORT_TONE: Record<string, string> = {
  minimal: 'min',
  low: 'low',
  medium: 'med',
  high: 'high',
  max: 'max',
  ultra: 'max',
  ultrathink: 'max',
}

// 最近已知力度缓存:键 <prefix>effort:<host>:<sid>,跨会话记忆。
const effortKey = (host: string, sid: string) => storageKey('effort:' + host + ':' + sid)
function loadEffort(host: string, sid: string): string {
  try {
    return localStorage.getItem(effortKey(host, sid)) || ''
  } catch {
    return ''
  }
}
function saveEffort(host: string, sid: string, effort: string) {
  try {
    localStorage.setItem(effortKey(host, sid), effort)
  } catch {
    // 隐私模式/配额满等:静默,chip 退化为本会话内存值即可。
  }
}
function EffortChip({ effort }: { effort?: string }) {
  if (!effort) return null
  const low = effort.toLowerCase()
  let tone = 'med'
  let label = effort
  for (const k of Object.keys(EFFORT_TONE)) {
    if (low.includes(k)) {
      tone = EFFORT_TONE[k]
      label = k
      break
    }
  }
  return (
    <span className={'eff-chip eff-' + tone} title={'当前思考力度:' + effort}>
      ⚡{label}
    </span>
  )
}

// 工作中指示器:spinner 动画 + 动词 + 运行计时(+ 后端给到的 token,输出 token 为 ↓)+ 真实 tip(若有)。
//
// 计时:优先用 live elapsed(里世界真实运行时间),本地兜底计时「持续累加、绝不重置」。
//   关键修复:旧版本地 sec 从 0 起,且 live elapsed 仅在 spinner/interrupt 行可见时才有 → 多数帧 elapsed 为空,
//   于是退回本地 sec,但本地 sec 是这一次 BusyLine 挂载后从 0 计的;若组件因 state 抖动重挂载,sec 又归零,
//   分钟段永远出不来。现在:
//     - baseSec(useRef)记「首个 live elapsed 出现时的真实秒数」与「那一刻的本地经过秒」差值,作为锚点;
//       此后即使 live elapsed 又消失,也用 锚点 + 本地经过秒 推算,持续累加不回退。
//     - 无 live elapsed 时纯本地累加(也不回退)。
function BusyLine({ tokens, verb, tip, elapsed, onInterrupt }: { tokens?: string; verb?: string; tip?: string; elapsed?: string; onInterrupt: () => void }) {
  const [frame, setFrame] = useState(0)
  const [tick, setTick] = useState(0) // 每秒 +1,纯触发重渲染
  const [vi, setVi] = useState(() => Math.floor(Math.random() * VERBS.length))
  // 本地秒表起点(挂载时刻);offsetRef 为「真实秒 - 本地经过秒」的对齐偏移,锁住后只增不减。
  const mountAt = useRef(Date.now())
  const offsetRef = useRef(0)
  const lastShown = useRef(0)

  useEffect(() => {
    const a = window.setInterval(() => setFrame((f) => (f + 1) % SPIN.length), 110)
    const b = window.setInterval(() => setTick((t) => t + 1), 1000)
    const c = window.setInterval(() => setVi((i) => (i + 1) % VERBS.length), 4000)
    return () => {
      clearInterval(a)
      clearInterval(b)
      clearInterval(c)
    }
  }, [])

  // 本地经过秒(从挂载起,持续累加,不受 state 抖动影响——基于 wall clock 而非计数器)。
  const localSec = Math.floor((Date.now() - mountAt.current) / 1000)
  const liveSec = parseElapsedSec(elapsed)
  if (liveSec != null) {
    // live 给到真实秒:用它对齐偏移(真实秒 = 本地经过 + offset)。
    // 只在能让总时长「不回退」时更新偏移,杜绝 live 抖一下又把计时拉小。
    const cand = liveSec - localSec
    if (cand > offsetRef.current) offsetRef.current = cand
  }
  let dur = localSec + offsetRef.current
  // 末道防线:显示值单调不减(任何来源抖动都不让秒表倒走)。
  if (dur < lastShown.current) dur = lastShown.current
  lastShown.current = dur
  // tick 参与渲染,避免被 lint 当未用;每秒重算上面的 dur。
  void tick

  return (
    <div className="busy-wrap">
      <div className="cbar busy-bar">
        <div className="busy-line">
          <span className="busy-spin">{SPIN[frame]}</span>
          <span className="busy-verb" key={verb || VERBS[vi]}>
            {verb || VERBS[vi]}
            <span className="ell">…</span>
          </span>
          <span className="busy-meta">
            <span className="busy-dur">{fmtDur(dur)}</span>
            {tokens ? <span className="busy-tok"> · ↓ {tokens} tokens</span> : ''}
          </span>
        </div>
        <button className="cbtn danger busy-int" onClick={onInterrupt} title="中断 Claude(esc)">
          中断
        </button>
      </div>
      {tip ? <div className="tips">{tip}</div> : null}
    </div>
  )
}

// 长按发送阈值:超过此毫秒数 = ultracode 强力模式。
const LONGPRESS_MS = 400

// 表世界控件层:据后端 /state(里世界当前控件)渲染对应映射控件,点击经 sendkeys/start 驱动里世界。
// 每次操作都给可见反馈(toast),失败不再静默。
export function ControlBar({
  host,
  sid,
  cwd,
  onRunCmd,
  isInfoCmd = registryIsInfoCmd,
}: {
  host: string
  sid: string
  cwd?: string
  onRunCmd: (cmd: string) => void
  // 命令是否为「信息类」(走 onRunCmd 开 InfoSheet 抓屏展示)。缺省取 info/registry 的派生判定
  // (/cost /status /mcp /doctor /hooks /skills);消费方可传入自定义或 `() => false` 关闭信息卡。
  isInfoCmd?: (cmd: string) => boolean
}) {
  const tsess = tmuxName(sid)
  // 控件状态双源:WS 镜像在线 → 直接消费客户端 detectState(useLiveState,毫秒级、无抓屏);
  // 降级(WS 连不上)→ 回退后端 /state 轮询(老兜底路径,保留)。
  const live = useLiveState()
  const degraded = useLiveDegraded()
  const [polled, setPolled] = useState<CtrlState>({ kind: 'input' })
  const st = degraded ? polled : live
  // 最近已知力度(从 localStorage 初始化,跨会话记忆);input 态 chip 读它而非恒空的 st.effort。
  const [lastEffort, setLastEffort] = useState(() => loadEffort(host, sid))
  const [text, setText] = useState('')
  const [showPal, setShowPal] = useState(false)
  const [sugCfm, setSugCfm] = useState<string | null>(null) // 输入建议:待确认发送的建议文本(非空=弹自绘 confirm)
  const [intCfm, setIntCfm] = useState(false) // 中断确认:true=弹自绘 confirm(防误触打断 Claude)
  const [ultraArm, setUltraArm] = useState(false) // 长按已触发 ultracode(发送键变色 + 冒提示)
  const [msg, setMsg] = useState('')
  const timer = useRef<number | undefined>(undefined)
  const msgTimer = useRef<number | undefined>(undefined)
  // 长按状态:计时器 + 「本次 pointerup 是否应跳过普通发送」标记(长按已发过就不再普通发)。
  const pressTimer = useRef<number | undefined>(undefined)
  const longFired = useRef(false)

  const poll = () => fetchState(host, tsess).then(setPolled).catch(() => {})
  // 仅在降级时轮询 /state;WS 在线则停轮询、纯靠 useLiveState。降级↔在线切换会重跑此 effect。
  useEffect(() => {
    if (!degraded) return
    poll()
    timer.current = window.setInterval(poll, 1800)
    return () => clearInterval(timer.current)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [host, sid, degraded])

  // 卸载时清掉长按计时器,避免泄漏。
  useEffect(() => () => clearTimeout(pressTimer.current), [])

  // 切会话时重新载入该会话的最近已知力度。
  useEffect(() => {
    setLastEffort(loadEffort(host, sid))
  }, [host, sid])

  // 任意态(live 或 polled)一旦带 effort(用户开过 /effort 面板),就缓存为「最近已知力度」。
  useEffect(() => {
    if (st.effort && st.effort !== lastEffort) {
      setLastEffort(st.effort)
      saveEffort(host, sid, st.effort)
    }
  }, [st.effort, host, sid, lastEffort])

  const flash = (m: string) => {
    setMsg(m)
    clearTimeout(msgTimer.current)
    msgTimer.current = window.setTimeout(() => setMsg(''), 2600)
  }
  // 统一发键层(双轨):文本/回车走 WS INPUT 帧,语义键走 /sendkeys,降级时全走 /sendkeys(见 sendkeys.ts)。
  // toast 只在「提交类」操作时给(传 toast 文案);移动光标/调力度等不提示。
  const act = (body: { text?: string; keys?: string[]; enter?: boolean }, toast?: string) => {
    sendAct(host, tsess, body).then((ok) => {
      if (toast) flash(ok ? toast : '失败:会话未在运行?')
    })
    // 降级态靠轮询刷新;WS 在线态由 detectState 自动更新,无需手动 poll。
    if (degraded) setTimeout(poll, 400)
  }
  // 点选项 = 用方向键把里世界菜单光标移到目标项(不提交);按「确认」才回车提交。
  // 高亮跟随真实光标:WS 在线时 detectState ~150ms 内回报新 cur;降级时轮询回报。故不再做乐观本地高亮。
  const moveTo = (num: string) => {
    const opts = st.options || []
    const ci = opts.findIndex((o) => o.cur)
    const ti = opts.findIndex((o) => o.num === num)
    if (ci < 0 || ti < 0 || ci === ti) return
    const keys: string[] = Array(Math.abs(ti - ci)).fill(ti > ci ? 'Down' : 'Up')
    act({ keys })
  }
  // 普通发送 / ultracode 发送的公共出口:ultra=true 时消息尾部追加 " ultracode"。
  const sendText = (ultra: boolean) => {
    const t = text.trim()
    if (!t) return
    const payload = ultra ? t + ' ultracode' : t
    act({ text: payload, enter: true }, ultra ? '已发送 · ultracode' : '已发送')
    setText('')
  }
  const send = () => sendText(false)

  // ── 长按发送:pointerdown 起计时,≥400ms 触发 ultracode 发送;pointerup 若已触发则不再普通发送(无双发)。
  const onSendDown = () => {
    longFired.current = false
    clearTimeout(pressTimer.current)
    pressTimer.current = window.setTimeout(() => {
      longFired.current = true
      if (text.trim()) {
        setUltraArm(true) // 按钮变色 + 冒「+ultracode」提示
        sendText(true)
        // 短暂保留高亮态,松手后渐隐。
        window.setTimeout(() => setUltraArm(false), 700)
      }
    }, LONGPRESS_MS)
  }
  const onSendUp = () => {
    clearTimeout(pressTimer.current)
    if (longFired.current) {
      longFired.current = false
      return // 长按已发,跳过普通发送
    }
    send() // 轻点 = 普通发送
  }
  const onSendLeave = () => {
    // 指针滑出按钮:取消未触发的长按(避免误判),不发送。
    clearTimeout(pressTimer.current)
  }

  let content: ReactNode
  if (st.kind === 'offline') {
    content = (
      <div className="cbar">
        <div className="cbar-info">会话未在运行</div>
        <button
          className="cbtn primary"
          onClick={() =>
            startSession(host, tsess, cwd || '', getConfig().resumeCmd(sid)).then((ok) => {
              flash(ok ? '已启动,稍候…' : '启动失败')
              setTimeout(poll, 1000)
            })
          }
        >
          启动会话
        </button>
      </div>
    )
  } else if (st.kind === 'busy') {
    content = (
      <BusyLine tokens={st.tokens} verb={st.verb} tip={st.tip} elapsed={st.elapsed} onInterrupt={() => setIntCfm(true)} />
    )
  } else if (st.kind === 'select') {
    content = (
      <div className="cbar col">
        {st.title && <div className="cbar-title">{st.title}</div>}
        {(st.options || []).map((o) => (
          <button key={o.num} className={'cbtn opt' + (o.cur ? ' cur' : '')} onClick={() => moveTo(o.num)}>
            {o.num}. {o.label}
          </button>
        ))}
        {st.effort && (
          <div className="cbar-row">
            <button className="cbtn adj" onClick={() => act({ keys: ['Left'] })}>
              ◀
            </button>
            <span className="cbar-eff">{st.effort}</span>
            <button className="cbtn adj" onClick={() => act({ keys: ['Right'] })}>
              ▶
            </button>
          </div>
        )}
        <div className="cbar-row">
          {(st.actions || []).map((a, i) => (
            <button
              key={i}
              className={'cbtn' + (a.label === '取消' ? ' danger' : a.label === '确认' ? ' primary' : '')}
              onClick={() => act({ keys: a.keys, text: a.text }, a.label === '取消' ? undefined : '已提交')}
            >
              {a.label}
            </button>
          ))}
        </div>
      </div>
    )
  } else {
    content = (
      <div className="cbar">
        <button className="cbtn slash" onClick={() => setShowPal(true)} title="斜杠命令">
          /
        </button>
        <div className="cinput-wrap">
          <textarea
            className="cinput"
            rows={1}
            value={text}
            placeholder="输入消息…"
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault()
                send()
              }
            }}
          />
          {/* 力度 chip(贴输入框右上角小标):input 态 st.effort 恒空,故读「最近已知力度」缓存。
              从没开过 /effort → lastEffort 为空 → 不渲染(EffortChip 内部已判空)。
              注:显示的是最近已知力度,非实时(屏幕在输入态不报力度)。 */}
          <EffortChip effort={lastEffort} />
        </div>
        {/* 建议提示词:与输入行一体的紧凑 icon。有建议→accent 高亮可点(自绘 confirm 后直接发);无建议→灰态禁用。 */}
        <button
          className={'cbtn sug-ico' + (st.suggest ? '' : ' off')}
          disabled={!st.suggest}
          title={st.suggest ? '发送推荐内容' : '暂无推荐内容'}
          onClick={() => st.suggest && setSugCfm(st.suggest)}
        >
          ✨
        </button>
        {/* 发送:轻点=普通发送;长按≈400ms=尾部追加 ultracode 强发(按钮变色 + 冒「+ultracode」)。 */}
        <button
          className={'cbtn primary send-btn' + (ultraArm ? ' ultra' : '')}
          onPointerDown={onSendDown}
          onPointerUp={onSendUp}
          onPointerLeave={onSendLeave}
          onPointerCancel={onSendLeave}
          onContextMenu={(e) => e.preventDefault()}
        >
          {ultraArm ? '+ultracode' : '发送'}
        </button>
      </div>
    )
  }

  return (
    <>
      {showPal && (
        <SlashPalette
          onClose={() => setShowPal(false)}
          onPick={(cmd) => act({ text: cmd, enter: true })}
          onRun={onRunCmd}
          isInfoCmd={isInfoCmd}
        />
      )}
      {sugCfm && (
        // 输入建议确认弹层(自绘,仿 Palette 的 .cfm 范例;铁律:不用原生 confirm)。
        <div className="cfm">
          <div className="cfm-box">
            <div className="cfm-msg">发送推荐内容?</div>
            <div className="cfm-prev">{sugCfm}</div>
            <div className="cfm-btns">
              <button className="cbtn" onClick={(e) => { e.preventDefault(); setSugCfm(null) }}>取消</button>
              <button
                className="cbtn primary"
                onClick={(e) => { e.preventDefault(); act({ text: sugCfm, enter: true }, '已发送'); setSugCfm(null) }}
              >
                确认
              </button>
            </div>
          </div>
        </div>
      )}
      {intCfm && (
        // 中断确认弹层(自绘,铁律:不用原生 confirm):确认才发 Escape 打断 Claude,防误触。
        <div className="cfm">
          <div className="cfm-box">
            <div className="cfm-msg">中断 Claude 当前任务?</div>
            <div className="cfm-prev">将向里世界发送 Esc,打断正在进行的回合。</div>
            <div className="cfm-btns">
              <button className="cbtn" onClick={(e) => { e.preventDefault(); setIntCfm(false) }}>取消</button>
              <button
                className="cbtn danger"
                onClick={(e) => { e.preventDefault(); act({ keys: ['Escape'] }, '已中断'); setIntCfm(false) }}
              >
                中断
              </button>
            </div>
          </div>
        </div>
      )}
      {msg && <div className="ctoast">{msg}</div>}
      {content}
    </>
  )
}
