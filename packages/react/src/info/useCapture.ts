import { useCallback, useEffect, useRef, useState } from 'react'
import { sendKeys, captureUntil, fetchCapture, sleep, fetchState, fetchTranscript, fetchCmdResult, tmuxName } from '../api'
import { storageKey } from '../config'
import { stripAnsi } from '../blocks/Ansi'
import type { CmdCard } from './registry'

// 开卡的五种结局。ok=真的解析出了目标卡;notfound=驱动了但没等到目标(被挡/未响应);
// busy=里世界生成中(不打扰);offline=会话没运行;loading=进行中。
export type Phase = 'loading' | 'ok' | 'notfound' | 'busy' | 'offline'

// ── 持久化 SWR 缓存(localStorage)──
// 键 = per host:sid:cmd;值 = { out, md, ts }(ts=该结果的刷新时间戳)。
// 打开时若有缓存 → 立即先显示旧数据(staleTs 驱动「上次刷新于 X · 加载中」提示条),后台再抓屏取数;
// 成功才更新内容 + ts;失败/notfound 不覆盖旧缓存(stale 继续展示)。tab 类按 cmd 维度天然分键(每 tab 一张 card)。
const ckey = (host: string, sid: string, cmd: string) => storageKey(`info:${host}:${sid}:${cmd}`)
interface Cached { out: string; md: boolean; ts: number }
function readCache(host: string, sid: string, cmd: string): Cached | null {
  try {
    const s = localStorage.getItem(ckey(host, sid, cmd))
    if (!s) return null
    const v = JSON.parse(s)
    if (v && typeof v.out === 'string' && typeof v.ts === 'number') return { out: v.out, md: !!v.md, ts: v.ts }
  } catch { /* ignore */ }
  return null
}
function writeCache(host: string, sid: string, cmd: string, out: string, md: boolean): number {
  const ts = Date.now()
  try { localStorage.setItem(ckey(host, sid, cmd), JSON.stringify({ out, md, ts })) } catch { /* 配额满等 → 静默 */ }
  return ts
}

export interface Capture {
  out: string // 抓屏卡:带色 TUI 原文;jsonl 卡:那段干净 markdown
  phase: Phase
  raw: boolean
  setRaw: (v: boolean) => void
  md: boolean // out 是否为 markdown(viaJsonl 命令为 true → CardBody 直接 <MD> 渲染,不再 parse)
  run: () => void // 刷新/打开/切 tab 同一路径:有缓存先秒显、后台必抓(SWR)
  ts: number // 当前展示内容的刷新时间戳(0=本次会话尚无结果);驱动「刷新于 X」/「上次刷新于 X」提示
  revalidating: boolean // true=正在后台抓新数据(SWR:旧数据照显,提示条转「加载中」)
}

// 相对时间(中文紧凑)。
export function relTime(ts: number): string {
  if (!ts) return ''
  const s = Math.max(0, (Date.now() - ts) / 1000)
  if (s < 10) return '刚刚'
  if (s < 60) return Math.floor(s) + '秒前'
  if (s < 3600) return Math.floor(s / 60) + '分钟前'
  if (s < 86400) return Math.floor(s / 3600) + '小时前'
  return Math.floor(s / 86400) + '天前'
}

// 唯一时序载体 —— 收敛旧 CostPanel.open / CmdSheet.run,并加「确定找到目标才算成功」的判定。
// SWR:打开时若 localStorage 有缓存 → 先秒显旧数据(phase=ok、ts=旧时间、revalidating=true),
//   随后照常抓屏/取数 →(成功)更新内容 + 新 ts、revalidating=false;(失败/notfound)保留旧数据、仅熄灭 revalidating(不回退 notfound)。
//   无缓存 → 走完整 loading 流程,成功后写缓存 + ts。
// 流程:探状态(busy 不打扰、offline 直接报)→ 统一 Esc 清场 → 发命令直达 →(rights 定向 Right / 否则按档延时)
//   → captureUntil 轮询到 parse 成功 → 以 parse 是否成功定 ok/notfound →(成功且 single-modal 抓完 Esc;成功才进缓存)。
export function useCapture(host: string, sid: string, card: CmdCard, tabs: boolean): Capture {
  const tsess = tmuxName(sid)
  const [out, setOut] = useState('')
  const [phase, setPhase] = useState<Phase>('loading')
  const [raw, setRaw] = useState(false)
  const [md, setMd] = useState(false) // out 是否为 markdown(jsonl 成功路径=true;抓屏/降级=false)
  const [ts, setTs] = useState(0) // 当前展示内容的刷新时间戳
  const [revalidating, setRevalidating] = useState(false) // 后台是否在抓新数据

  const run = useCallback(
    () => {
      setRaw(false)
      // SWR:每次 run 都「有缓存先秒显旧数据 + 后台必抓」—— 故无需区分 force:刷新键、切 tab、打开都同一路径(避免闪空)。
      const hit = readCache(host, sid, card.cmd)
      const haveStale = !!hit
      if (hit) {
        setOut(hit.out)
        setMd(hit.md)
        setTs(hit.ts)
        setPhase('ok')
        setRevalidating(true) // 旧数据照显 + 提示条转「正在加载新数据…」
      } else {
        setPhase('loading')
        setOut('')
        setMd(false)
        setTs(0)
        setRevalidating(false)
      }
      const { cmd, rights = 0, esc = 1 } = card.reach

      // 失败/中断收尾:有旧数据 → 保留(stale 继续展示,仅熄灭 revalidating);无旧数据 → 落到给定 phase。
      const settleFail = (p: Phase) => {
        if (haveStale) setRevalidating(false)
        else setPhase(p)
      }

      // viaJsonl 路径:发命令前取游标 → 提交 → 轮询 /cmdresult 拿干净 markdown → 直接 MD 渲染。
      // 摆脱抓屏 + ANSI 解析;失败(超时/取不到)降级回退到抓屏路径(走下方 go())。
      const goJsonl = async (): Promise<boolean> => {
        const stt = await fetchState(host, tsess)
        if (stt.kind === 'offline') { settleFail('offline'); return true }
        if (stt.kind === 'busy') { settleFail('busy'); return true }
        // 1) 发命令前的游标:用当前 transcript EOF 作 since,只认此后追加的 isMeta。
        let since: number
        try { since = (await fetchTranscript(host, sid)).cursor } catch { since = 0 }
        // 2) 提交命令(/context 打印进流,无挡路面板 → 不发 Esc)。
        // sendKeys 现返回 SendResult({ok, kind?});这里只关心成功与否,取 .ok。
        const { ok } = await sendKeys(host, tsess, { text: cmd, enter: true })
        if (!ok) { settleFail('offline'); return true }
        // 3) 轮询 /cmdresult 直到 found(~6 次 × 600ms);since 随后端返回的 cursor 推进,不重扫。
        for (let i = 0; i < 6; i++) {
          await sleep(600)
          const r = await fetchCmdResult(host, sid, since)
          if (r.found && r.markdown.trim()) {
            setOut(r.markdown)
            setMd(true)
            setPhase('ok')
            setTs(writeCache(host, sid, card.cmd, r.markdown, true)) // 只缓存成功 + 刷新 ts
            setRevalidating(false)
            return true
          }
          if (typeof r.cursor === 'number' && r.cursor > since) since = r.cursor
        }
        return false // 超时 → 让调用方回退抓屏(由 go() 自己收尾)
      }

      const go = async () => {
        // 1) 先探状态:busy 发 Esc 会中断生成 → 不打扰;offline 无从驱动 → 直接报(有旧数据则保留)。
        const stt = await fetchState(host, tsess)
        if (stt.kind === 'offline') return settleFail('offline')
        if (stt.kind === 'busy') return settleFail('busy')
        // 2) 按卡清场:关掉可能挡路的菜单/上一个面板,再发命令直达目标。Esc 次数来自 reach.esc(唯一真相)。
        for (let i = 0; i < esc; i++) {
          await sendKeys(host, tsess, { keys: ['Escape'] })
          await sleep(150)
        }
        if (esc > 0) await sleep(150)
        const { ok } = await sendKeys(host, tsess, { text: cmd, enter: true })
        if (!ok) return settleFail('offline')
        if (rights > 0) {
          await sleep(1300) // 无直达命令(stats):落地后定向 N×Right,单次不累积
          sendKeys(host, tsess, { keys: Array(rights).fill('Right') })
          await sleep(700)
        } else {
          await sleep(1000) // 统一留足:停留面板取网络也需时
        }
        // 3) 轮询到能解析出卡为止;最终以 parse 是否成功判定「确实找到目标」。
        // 抓带色原文(ansi:true)供展示;ok 判定与下方 parse 一律先 stripAnsi(解析逻辑零影响)。
        const t = await captureUntil(host, tsess, {
          ok: (x) => card.mod.parse(x) != null,
          tries: rights > 0 || tabs ? 5 : 6,
          gap: 550,
          ansi: true,
        })
        const found = card.mod.parse(stripAnsi(t)) != null
        if (found) {
          setOut(t) // out 带色:供「原始」回退 / 解析失败回退 用 AnsiText 渲染
          setMd(false) // 抓屏/降级路径:out 是带色 ANSI,非 markdown
          setPhase('ok')
          setTs(writeCache(host, sid, card.cmd, t, false)) // 只缓存成功结果 + 刷新 ts
          setRevalidating(false)
          if (card.modal && !tabs && esc > 0) sendKeys(host, tsess, { keys: Array(esc).fill('Escape') }) // single-modal 抓完按 esc 次关里世界面板
        } else {
          // 失败:有旧数据 → 保留 stale(不覆盖、不回退 notfound);无旧数据 → 落 notfound 并展示带色原文供「原始」回退。
          if (haveStale) {
            setRevalidating(false)
            if (card.modal && !tabs && esc > 0) sendKeys(host, tsess, { keys: Array(esc).fill('Escape') }) // 仍把里世界面板收掉
          } else {
            setOut(t)
            setMd(false)
            setPhase('notfound')
          }
        }
      }
      // viaJsonl 命令先走 jsonl 路径;它返回 false(超时/取不到)才降级抓屏。其余命令直接抓屏。
      if (card.viaJsonl) goJsonl().then((done) => { if (!done) go() })
      else go()
    },
    [host, tsess, card, tabs, sid],
  )

  // 防 StrictMode dev 双发:同 (host|tsess|cmd) 首跑一次。
  const ran = useRef('')
  useEffect(() => {
    const k = host + '|' + tsess + '|' + card.cmd
    if (ran.current === k) return
    ran.current = k
    run()
  }, [host, tsess, card.cmd, run])

  return { out, phase, raw, setRaw, md, run, ts, revalidating }
}

// ── 扫页式抓取(/cost 面板的 ← → 多 tab):一次开面板 → ← 到最左 → → 逐页扫,每屏用各 tab 的 parse 认页 ──
// 为什么换掉「逐 tab 按需抓屏」(旧 TabsSheet):切 tab 要重发命令 + 重抓屏,覆盖网下每次都赌时序,常失败
// (= 用户反馈「用量可以,但没法切换」)。claude 的 /cost 面板本身是一个 ← → 多页面板
// (Settings/Status/Config/Usage/Stats),故:开一次面板,后台把所有页都浏览+抓+解析好,切 tab 即时
// (读已抓结果,不再发命令)。= 用户诉求「点任一卡 → 后台自动把几页都浏览一遍并渲染」。
export interface SweepState {
  found: Record<string, { data: unknown; raw: string }> // key=tab.cmd → 该页 parse 结果 + 带色原文
  phase: 'loading' | 'ok' | 'offline' | 'busy'
  run: () => void
  ts: number
}

export function useSweep(host: string, sid: string, tabs: CmdCard[]): SweepState {
  const tsess = tmuxName(sid)
  const [found, setFound] = useState<Record<string, { data: unknown; raw: string }>>({})
  const [phase, setPhase] = useState<'loading' | 'ok' | 'offline' | 'busy'>('loading')
  const [ts, setTs] = useState(0)
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])

  const run = useCallback(() => {
    setPhase('loading')
    setFound({})
    void (async () => {
      // 0) 只挡 busy(claude 生成中,发命令会打断);offline 不挡 —— 客户端/设备的 offline 判定可能误报
      //    (见 livestate 的 shellEvidence 修复),直接试着开面板更稳,真死会话顶多抓到空、降级空态。
      const stt = await fetchState(host, tsess)
      if (stt.kind === 'busy') {
        if (alive.current) setPhase('busy')
        return
      }
      // 1) 开 /cost 面板(用量/状态/配置/统计/设置同属这一个 ← → 面板)。
      const { ok } = await sendKeys(host, tsess, { text: '/cost', enter: true })
      if (!ok) {
        if (alive.current) setPhase('offline')
        return
      }
      await sleep(1300)
      // 2) ← 到最左 tab(在最左继续 ← 是 no-op,安全),确保从头扫起。
      for (let i = 0; i < 5; i++) {
        await sendKeys(host, tsess, { keys: ['Left'] })
        await sleep(110)
      }
      await sleep(300)
      // 3) → 逐页扫;每屏剥色后让各 tab 的 parse 各试一遍(谁非空即是哪页),进度式回填 → tab 陆续点亮。
      const acc: Record<string, { data: unknown; raw: string }> = {}
      for (let step = 0; step < 7; step++) {
        if (!alive.current) return
        const raw = (await fetchCapture(host, tsess, 220, true)).replace(/\s+$/, '')
        const clean = stripAnsi(raw)
        let changed = false
        for (const t of tabs) {
          if (!acc[t.cmd]) {
            const d = t.mod.parse(clean)
            if (d != null) {
              acc[t.cmd] = { data: d, raw }
              changed = true
            }
          }
        }
        if (changed && alive.current) setFound({ ...acc })
        if (Object.keys(acc).length >= tabs.length) break
        if (step < 6) {
          await sendKeys(host, tsess, { keys: ['Right'] })
          await sleep(580)
        }
      }
      // 4) 关面板回干净输入态(/cost 面板:一次 Esc 关;再补一次兜底残留菜单)。
      await sendKeys(host, tsess, { keys: ['Escape'] })
      await sleep(120)
      await sendKeys(host, tsess, { keys: ['Escape'] })
      if (alive.current) {
        setFound({ ...acc })
        setPhase('ok')
        setTs(Date.now())
      }
    })()
  }, [host, tsess, tabs])

  // 首挂载跑一次(同 host|tsess 不重复)。
  const ran = useRef('')
  useEffect(() => {
    const k = host + '|' + tsess
    if (ran.current === k) return
    ran.current = k
    run()
  }, [host, tsess, run])

  return { found, phase, run, ts }
}
