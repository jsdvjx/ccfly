// SessionView —— 一个完整会话视图(上游 App 里的 Session 组件,去掉 SessionPicker / host-sid 路由)。
//
// 组合:消息流(TranscriptView,含缓存/SSE/上滑分页)+ 顶栏(标题/⌨ 终端)+ 常驻镜像终端(LiveTerm)
//       + 运行中子代理概览(AgentDock)+ 控件层(ControlBar)。
//
// 与上游的差异:
//   - 不含 SessionPicker:列表与路由是「消费方」的事。SessionView 直接收 sid(+ 可选 host/cwd)。
//   - host 默认空串:控制服务端点由 CCFlyProvider(config.baseUrl)决定,host 仅作 fetchSessions 过滤键
//     与各 api 函数的(已不参与 URL 的)首参占位。多数单设备消费方传 sid 即可。
//   - 信息命令(/cost /status /mcp /doctor /skills /hooks …):走 InfoSheet —— 后台驱动命令抓屏、解析成
//     原生卡展示(info/ 子树已抽入本包)。/context 例外:它运行时往 jsonl 写 ## Context Usage,直接进消息流,
//     故 ⓘ 仍直发 /context 到里世界(不开 InfoSheet),与上游一致。
//
// 必须在 <CCFlyProvider> 子树内使用。模块级单例 host(ReaderHost/SubagentHost/WorkflowOverlayHost/LightboxHost)
// 由 <CCFlyHosts/> 提供(见 hosts.tsx),消费方在 App 根挂一次。
import { useEffect, useMemo, useRef, useState, type RefObject } from 'react'
import {
  fetchTranscript,
  fetchTranscriptTail,
  fetchTranscriptOlder,
  streamTranscript,
  fetchSessions,
  terminalUrl,
  sendKeys,
  tmuxName,
} from './api'
import { idbGetTx, idbPutTx, purgeLegacyTx } from './idb'
import { useStore } from './store'
import { TranscriptView, shortModel } from './components'
import { ControlBar } from './ControlBar'
import { AgentDock } from './AgentDock'
import { LiveTerm } from './LiveTerm'
import { liveTermHandle } from './liveconn'
import { useLiveDegraded } from './livestate'
import { SessionContext } from './blocks/ctx'
import { storageKey } from './config'
import { InfoSheet } from './info/InfoSheet'
import type { Item, SessionMeta } from './types'

function fmtTok(n?: number): string {
  if (!n) return ''
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k'
  return '' + n
}

// 长会话快速跳转:右侧 scrubber。默认收起成贴右缘的小水滴,滚动/触碰展开,空闲 1.5s 自动收回。
// 拖动/交互期间保持展开,拖动时显示百分比。内容超两屏才出现(否则连水滴都不显示)。
function ScrollNav({ scroller }: { scroller: RefObject<HTMLDivElement | null> }) {
  const [pct, setPct] = useState(0)
  const [show, setShow] = useState(false)
  const [drag, setDrag] = useState(false)
  const [active, setActive] = useState(false)
  const track = useRef<HTMLDivElement>(null)
  const idle = useRef(0)

  // 标记交互 → 展开,并重置 1.5s 空闲定时器(到点收回)。
  const wake = () => {
    setActive(true)
    if (idle.current) clearTimeout(idle.current)
    idle.current = window.setTimeout(() => setActive(false), 1500)
  }

  useEffect(() => {
    const el = scroller.current
    if (!el) return
    let raf = 0
    const onScroll = () => {
      wake()
      if (raf) return
      raf = requestAnimationFrame(() => {
        raf = 0
        const max = el.scrollHeight - el.clientHeight
        setPct(max > 0 ? el.scrollTop / max : 0)
        setShow(el.scrollHeight > el.clientHeight * 2) // 超过两屏才显示
      })
    }
    el.addEventListener('scroll', onScroll, { passive: true })
    onScroll()
    return () => {
      el.removeEventListener('scroll', onScroll)
      if (idle.current) clearTimeout(idle.current)
    }
  }, [scroller])

  const jump = (frac: number) => {
    const el = scroller.current
    if (!el) return
    el.scrollTop = Math.max(0, Math.min(1, frac)) * (el.scrollHeight - el.clientHeight)
  }
  const onMove = (e: React.PointerEvent) => {
    const tr = track.current
    if (!tr) return
    const r = tr.getBoundingClientRect()
    jump((e.clientY - r.top) / r.height)
  }

  if (!show) return null
  return (
    <div
      className={'snav' + (active || drag ? ' active' : '')}
      onPointerMove={() => drag && wake()}
    >
      {/* 收起态的小水滴把手:点/触即展开;展开后让位给真正的控件 */}
      <button className="snav-grip" onPointerDown={wake} aria-label="展开导航" />
      <button className="snav-btn" onClick={() => jump(0)} title="顶部">
        ⤒
      </button>
      <div
        ref={track}
        className="snav-track"
        onPointerDown={(e) => {
          setDrag(true)
          wake()
          e.currentTarget.setPointerCapture(e.pointerId)
          onMove(e)
        }}
        onPointerMove={(e) => drag && onMove(e)}
        onPointerUp={(e) => {
          setDrag(false)
          wake()
          e.currentTarget.releasePointerCapture(e.pointerId)
        }}
      >
        <div className={'snav-thumb' + (drag ? ' on' : '')} style={{ top: pct * 100 + '%' }}>
          {drag && <span className="snav-pct">{Math.round(pct * 100)}%</span>}
        </div>
      </div>
      <button className="snav-btn" onClick={() => jump(1)} title="底部">
        ⤓
      </button>
    </div>
  )
}

// 渲染窗口上限:不做虚拟化,故「初始只渲染尾窗、上滑再增长」是保持首屏快的关键。
// 后端尾窗也约 150 条,与此对齐。
const WINDOW = 150

// ── SessionView props ──
// sid 必填;host 可选(默认空串,控制端点由 Provider 决定);cwd 可选(起会话/终端 attach 用);
// onBack 可选(顶栏返回箭头;不传则不渲染返回键)。
export interface SessionViewProps {
  sid: string
  host?: string
  cwd?: string
  onBack?: () => void
}

export function SessionView({ sid, host = '', cwd: cwdProp, onBack }: SessionViewProps) {
  const items = useStore((s) => s.items)
  const cursor = useStore((s) => s.cursor)
  const firstCursor = useStore((s) => s.firstCursor)
  const hasMore = useStore((s) => s.hasMore)
  const setInitial = useStore((s) => s.setInitial)
  const append = useStore((s) => s.append)
  const appendMany = useStore((s) => s.appendMany)
  const prependMany = useStore((s) => s.prependMany)
  const reset = useStore((s) => s.reset)
  const [meta, setMeta] = useState<SessionMeta | undefined>()
  const [err, setErr] = useState('')
  const [infoCmd, setInfoCmd] = useState<string | null>(null) // 当前打开的信息卡命令(/cost /status /mcp …);null=未开
  const [topLoading, setTopLoading] = useState(false)
  const [termOpen, setTermOpen] = useState(false) // 浮层终端(LiveTerm 显示态)是否打开
  const degraded = useLiveDegraded()
  // cwd:优先 props,其次会话 meta。
  const cwd = cwdProp ?? meta?.cwd
  // 无全局折叠开关:每张卡按自身类型的 TUI 习惯默认折叠/展开,点头部各自开合。
  const scroller = useRef<HTMLDivElement>(null)
  const bottom = useRef<HTMLDivElement>(null)
  const restored = useRef(false)
  const saveTimer = useRef(0)
  // 缓存命中时:整段缓存留在内存,只把末尾一窗喂给 store;更老切片放这供上滑「瞬时前插」(不走网络)。
  const olderBuf = useRef<Item[]>([])
  // 上滑前插防重入。
  const loadingOlder = useRef(false)

  // 载入(三段):
  //  1) 打开:idb 命中→缓存末尾一窗秒显(更老留内存);未命中→fetchTranscriptTail 尾窗。都落到底。
  //  2) 增量:fetchTranscript(since=cursor) 补齐到 EOF + SSE 跟随(append),写回 idb(防抖)。
  useEffect(() => {
    reset()
    setErr('')
    restored.current = false
    olderBuf.current = []
    loadingOlder.current = false
    setTopLoading(false)
    let cancelled = false
    let stop = () => {}
    const begin = (c: number) => {
      if (cancelled) return
      stop = streamTranscript(host, sid, c, (cc, it) => append(cc, it as Item))
    }

    ;(async () => {
      const cached = await idbGetTx(host, sid)
      if (cancelled) return

      if (cached && cached.items.length) {
        // 缓存秒显:整段缓存只渲染末尾一窗,更老切片留内存供上滑瞬时前插。
        const all = cached.items
        const tail = all.length > WINDOW ? all.slice(-WINDOW) : all
        olderBuf.current = all.length > WINDOW ? all.slice(0, all.length - WINDOW) : []
        // 渲染窗口已缩到尾窗;但「更老内存切片 / 后端更老」都还有 → hasMore=true。
        const wndHasMore = olderBuf.current.length > 0 || cached.hasMore
        setInitial(tail, cached.cursor, cached.firstCursor, wndHasMore)

        // 增量补齐到 EOF。文件变短(compact/clear)→ cursor 倒退 → 缓存失效,全量重取尾窗。
        try {
          const r = await fetchTranscript(host, sid, cached.cursor)
          if (cancelled) return
          if (r.cursor < cached.cursor) {
            olderBuf.current = []
            const t = await fetchTranscriptTail(host, sid)
            if (cancelled) return
            setInitial(t.items, t.cursor, t.firstCursor, !!t.hasMore)
            begin(t.cursor)
          } else {
            appendMany(r.items, r.cursor)
            begin(r.cursor)
          }
        } catch (e) {
          if (!cancelled) setErr(String(e))
        }
        return
      }

      // 未命中:首拉尾窗。
      try {
        const t = await fetchTranscriptTail(host, sid)
        if (cancelled) return
        setInitial(t.items, t.cursor, t.firstCursor, !!t.hasMore)
        begin(t.cursor)
      } catch (e) {
        if (!cancelled) setErr(String(e))
      }
    })()

    fetchSessions(host).then((ss) => !cancelled && setMeta(ss.find((s) => s.session_id === sid)))
    return () => {
      cancelled = true
      stop()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [host, sid])

  // 持久化缓存(防抖 800ms):把「内存里更老切片 + 当前渲染窗口」整段写回 idb(无 3MB 限制)。
  useEffect(() => {
    if (!items.length) return
    const t = setTimeout(() => {
      const full = [...olderBuf.current, ...items]
      idbPutTx(host, sid, { items: full, cursor, firstCursor, hasMore })
    }, 800)
    return () => clearTimeout(t)
  }, [items, cursor, firstCursor, hasMore, host, sid])

  // 进入定位 + 新消息自动到底:
  // 首次内容出现 → 恢复上次滚动位置;无记录则跳到最底(而非停在顶部)。之后新消息仅在近底时自动到底。
  const count = items.length
  useEffect(() => {
    const el = scroller.current
    if (!el || !count) return
    if (!restored.current) {
      restored.current = true
      const saved = Number(sessionStorage.getItem(storageKey('scroll:' + sid)) || 0)
      const toBottom = () => bottom.current?.scrollIntoView()
      requestAnimationFrame(() => {
        if (saved > 0 && saved <= el.scrollHeight - el.clientHeight + 8) el.scrollTop = saved
        else {
          toBottom()
          setTimeout(toBottom, 400) // 等 shiki 异步高亮把高度撑稳后再贴一次底
        }
      })
      return
    }
    if (el.scrollHeight - el.scrollTop - el.clientHeight < 240) bottom.current?.scrollIntoView()
  }, [count, sid])

  // 3) 上滑加载更老:近顶部且 hasMore 时前插一窗。优先吃内存里的更老切片(瞬时),用尽再走网络。
  //    锚定滚动:前插前记 scrollHeight,前插后 scrollTop += 新旧高度差,避免视口跳动。
  const loadOlder = () => {
    if (loadingOlder.current || !useStore.getState().hasMore) return
    const el = scroller.current
    if (!el) return
    loadingOlder.current = true
    const prevH = el.scrollHeight
    const prevTop = el.scrollTop
    // 前插后锚定视口(prevTop + 高度增量),避免视口跳动。React 重渲染异步、shiki 高亮还会再撑高,
    // 故连续两帧各贴一次(第二帧吃掉首帧后又长出的高度),稳住位置。
    const anchor = () => {
      requestAnimationFrame(() => {
        const e = scroller.current
        if (e) e.scrollTop = prevTop + (e.scrollHeight - prevH)
        requestAnimationFrame(() => {
          const e2 = scroller.current
          if (e2) e2.scrollTop = prevTop + (e2.scrollHeight - prevH)
        })
      })
    }

    // a) 内存里有更老切片 → 瞬时前插一窗,不走网络。
    if (olderBuf.current.length) {
      const buf = olderBuf.current
      const take = buf.length > WINDOW ? buf.slice(-WINDOW) : buf
      olderBuf.current = buf.length > WINDOW ? buf.slice(0, buf.length - WINDOW) : []
      const st = useStore.getState()
      // 还有更老:内存切片没吃完,或后端那侧仍 hasMore(firstCursor>0)。
      const stillMore = olderBuf.current.length > 0 || st.firstCursor > 0
      prependMany(take, st.firstCursor, stillMore)
      anchor()
      // 冷却 400ms 再解锁:回到顶部(scrollTop=0)时锚定若没把视口顶离顶部,onScroll 会连环触发、
      // 把内存里上千条更老一次性全量前插渲染 → 卡死/黑屏。冷却确保「一次滚动最多加载一窗」。
      window.setTimeout(() => {
        loadingOlder.current = false
      }, 400)
      return
    }

    // b) 内存切片用尽 → 网络取 before=firstCursor 的更老一窗。
    const before = useStore.getState().firstCursor
    if (before <= 0) {
      loadingOlder.current = false
      return
    }
    setTopLoading(true)
    fetchTranscriptOlder(host, sid, before)
      .then((r) => {
        prependMany(r.items, r.firstCursor ?? 0, !!r.hasMore)
        anchor()
      })
      .catch(() => {})
      .finally(() => {
        setTopLoading(false)
        loadingOlder.current = false
      })
  }

  // 记住滚动位置(节流 300ms 写 sessionStorage)+ 近顶部触发上滑加载更老。
  const onScroll = () => {
    const el = scroller.current
    if (!el) return
    if (el.scrollTop < 300 && useStore.getState().hasMore && !loadingOlder.current) loadOlder()
    if (saveTimer.current) return
    saveTimer.current = window.setTimeout(() => {
      saveTimer.current = 0
      const e = scroller.current
      if (e) sessionStorage.setItem(storageKey('scroll:' + sid), String(e.scrollTop))
    }, 300)
  }

  // 会话上下文:host/sid 稳定,memo 一次,供 AgentCard 等深层块懒加载子 transcript。
  const sessionCtx = useMemo(() => ({ host, sid }), [host, sid])

  // 「⌨ 终端」:WS 镜像在线 → 秒切常驻 LiveTerm 的显隐(不跳页、不重连);
  // WS 降级(连不上)→ 若消费方配了外部终端直链(terminalUrl 非空)则跳新标签页;
  // 否则(默认:ccfly 自带 /term 是 WS、无直链)什么都不做(等 LiveTerm 自动重连)。
  const toggleTerm = () => {
    if (degraded) {
      const url = terminalUrl(host, sid, cwd)
      if (url) window.open(url, '_blank', 'noopener')
      return
    }
    if (liveTermHandle.isShown()) {
      liveTermHandle.hide()
      setTermOpen(false)
    } else {
      liveTermHandle.show()
      setTermOpen(true)
    }
  }
  // 换会话时关掉浮层(LiveTerm 重建会复位为隐藏态)。
  useEffect(() => {
    setTermOpen(false)
  }, [host, sid])

  return (
    <div className="app">
      <header className="hdr">
        {onBack && (
          <button className="back" onClick={onBack} aria-label="返回">
            ‹
          </button>
        )}
        <div className="htxt">
          <div className="ht">{meta?.title || sid.slice(0, 8)}</div>
          <div className="hm">
            {[host, shortModel(meta?.model), meta?.tokens ? fmtTok(meta.tokens) + ' ctx' : '']
              .filter(Boolean)
              .join(' · ')}
          </div>
        </div>
        {/* /context 已退出信息体系:它运行时往 jsonl 写一条 ## Context Usage markdown,直接流进消息流渲染。
            故 ⓘ 不再开 InfoSheet,而是直接发 /context 到里世界 —— 输出随后出现在消息流(复用斜杠面板同一条 sendKeys)。 */}
        <button
          className="hbtn"
          onClick={() => sendKeys(host, tmuxName(sid), { text: '/context', enter: true })}
          title="上下文用量"
        >
          ⓘ
        </button>
        {/* WS 镜像在线:此键秒切下半屏浮层终端(显/隐同一份 xterm);WS 降级:回退跳 ttyd 新标签(见 toggleTerm)。 */}
        <button
          className={'term' + (termOpen ? ' on' : '')}
          onClick={toggleTerm}
          title={degraded ? '打开终端(新标签)' : termOpen ? '收起终端' : '展开终端'}
        >
          ⌨ 终端
        </button>
      </header>
      {/* 信息卡浮层:Palette/ControlBar 把信息类命令经 onRunCmd 传上来,InfoSheet 据 cmd 驱动抓屏并渲染原生卡。 */}
      {infoCmd && <InfoSheet host={host} sid={sid} cmd={infoCmd} onClose={() => setInfoCmd(null)} />}
      <div className="scroll" ref={scroller} onScroll={onScroll}>
        {topLoading && (
          <div className="tx-top-spin" aria-label="加载更早">
            ⟳ 加载更早…
          </div>
        )}
        {err && <div className="empty">读取失败: {err}</div>}
        <SessionContext.Provider value={sessionCtx}>
          <TranscriptView items={items} />
        </SessionContext.Provider>
        <div ref={bottom} />
      </div>
      <ScrollNav scroller={scroller} />
      {/* 常驻隐藏 xterm:连 ttyd WS 镜像里世界,客户端 detectState 供控件层消费。 */}
      <LiveTerm host={host} sid={sid} cwd={cwd} />
      <AgentDock host={host} sid={sid} />
      {/* onRunCmd:信息类命令(由 ControlBar 缺省的 registry.isInfoCmd 判定)经此打开 InfoSheet。 */}
      <ControlBar host={host} sid={sid} cwd={cwd} onRunCmd={(cmd) => setInfoCmd(cmd)} />
    </div>
  )
}

// 换版:旧 localStorage 的 <prefix>tx:* 是「图片拍平成文本」的旧形态,清掉它,缓存改走 IndexedDB(全量重取)。
// (模块加载即执行一次;依赖 config —— 须在 CCFlyProvider 设置 config 之后才有意义,故消费方可在挂载后再无妨。)
purgeLegacyTx()
