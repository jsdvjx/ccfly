import { useEffect, useRef, useState, type ReactNode, type ChangeEvent, type ClipboardEvent, type DragEvent } from 'react'
import { fetchState, startSession, tmuxName, type CtrlState } from './api'
import { useLiveState, useLiveDegraded, useLiveCertainInput, useLiveStore } from './livestate'
import { sendAct, type SendBody } from './sendkeys'
import { SlashPalette } from './Palette'
import { isInfoCmd as registryIsInfoCmd } from './info/registry'
import { storageKey, getConfig } from './config'
// 富 select 客户端分类:命中已知菜单(model/permission/effort/confirm/sessionScope/multi)时渲富 UI,
// 否则回落本文件内既有的通用 select 渲染。分类器与各富组件全是纯展示,点击映射回同一套 helpers
// (navTo/moveTo/toggleAt/act)发键,无任何 parser/state 改动。
import { selectKind, type RichSelectHelpers } from './select/selectKind'
import { RichModelSelect } from './select/RichModelSelect'
import { useEngineState } from './engine/react'
import { EngineControl } from './engine/states'
import { RichPermissionSelect } from './select/RichPermissionSelect'
import { RichEffortSelect } from './select/RichEffortSelect'
import { RichConfirmSelect } from './select/RichConfirmSelect'
import { RichSessionScopeSelect } from './select/RichSessionScopeSelect'
import { RichMultiSelect } from './select/RichMultiSelect'
import { RichListSelect } from './select/RichListSelect'
// 空闲 input 态的快捷 chip 行(Round-2 A):模型/力度 chip(发 /model、/effort,菜单由 Rich 组件接管)
// + 压缩/清空快捷斜杠。纯展示、走 act/onRunCmd,不改解析层。
import { ComposeChips } from './ComposeChips'
// 图片/文件附件条 + 其状态 hook(ControlBar 持有 state);预判扫描器(里世界是否在要图)。
import { AttachmentBar, useAttachments, scanWantsImage } from './AttachmentBar'
// 读取消息流末条 assistant 的 model 名,供模型 chip 显示「当前模型」(只读 store,无耦合、无 state 改动)。
import { useStore } from './store'

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

// 最近已知力度:Claude Code 普通输入态不暴露当前思考力度(CtrlState.effort 仅在 /effort 面板那刻有值,
// input 态恒 undefined)。务实折衷:每次任意 CtrlState 带 effort 就缓存进 localStorage(按 host+sid 分键),
// 供「模型 chip」展示「模型 + 版本 + 最近已知力度」。展示在 ComposeChips 内,力度的修改入口在模型页(/model)。
// 缓存键 <prefix>effort:<host>:<sid>,跨会话记忆。
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

// 工作中指示器:spinner 动画 + 动词 + 运行计时(+ 后端给到的 token,输出 token 为 ↓)+ 真实 tip(若有)。
//
// 计时:优先用 live elapsed(里世界真实运行时间),本地兜底计时「持续累加、绝不重置」。
//   关键修复:旧版本地 sec 从 0 起,且 live elapsed 仅在 spinner/interrupt 行可见时才有 → 多数帧 elapsed 为空,
//   于是退回本地 sec,但本地 sec 是这一次 BusyLine 挂载后从 0 计的;若组件因 state 抖动重挂载,sec 又归零,
//   分钟段永远出不来。现在:
//     - baseSec(useRef)记「首个 live elapsed 出现时的真实秒数」与「那一刻的本地经过秒」差值,作为锚点;
//       此后即使 live elapsed 又消失,也用 锚点 + 本地经过秒 推算,持续累加不回退。
//     - 无 live elapsed 时纯本地累加(也不回退)。
function BusyLine({ tokens, verb, tip, elapsed, phase, percent, onInterrupt }: { tokens?: string; verb?: string; tip?: string; elapsed?: string; phase?: string; percent?: number; onInterrupt: () => void }) {
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

  // 压缩阶段(/compact):里世界给了真实进度条 → 渲染专属确定态进度条;没给百分比则不确定态(滚动条纹)。
  // 不臆造百分比:percent 仅在里世界进度条上确有数字(1-100)时才有,否则走不确定态。
  if (phase === 'compacting') {
    const determinate = typeof percent === 'number' && percent > 0
    const pct = determinate ? Math.min(100, Math.max(0, percent as number)) : 0
    return (
      <div className="busy-wrap">
        <div className="cbar busy-bar cmp-bar">
          <div className="cmp-main">
            <div className="cmp-head">
              <span className="cmp-ico" aria-hidden>🗜</span>
              <span className="cmp-label">正在压缩上下文<span className="ell">…</span></span>
              <span className="cmp-meta">{determinate ? pct + '%' : fmtDur(dur)}</span>
            </div>
            <div
              className={'cmp-track' + (determinate ? '' : ' indet')}
              role="progressbar"
              aria-label="压缩进度"
              aria-valuenow={determinate ? pct : undefined}
              aria-valuemin={0}
              aria-valuemax={100}
            >
              <div className="cmp-fill" style={determinate ? { width: pct + '%' } : undefined} />
            </div>
          </div>
          <button className="cbtn danger busy-int" onClick={onInterrupt} title="中断压缩(esc)">
            中断
          </button>
        </div>
      </div>
    )
  }

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
  const certainInput = useLiveCertainInput()
  // 新读屏引擎当前态(shadow):用于让新 modelSelect 控件接管 /model 选择器(见下方 content 分支)。
  const engineMatch = useEngineState()
  const [polled, setPolled] = useState<CtrlState>({ kind: 'input' })
  const st = degraded ? polled : live
  // 附件状态(ControlBar 持有;AttachmentBar 只渲染)。加入即上传,submit 漏斗读 donePaths 并进提交。
  const attachments = useAttachments(tsess)
  // 隐藏的原生 file input(📎 按钮触发 .click());capture 属性让移动端唤起相机/相册。
  const fileInputRef = useRef<HTMLInputElement>(null)
  // 拖拽态:dragover 高亮 compose 区(.drag-active)+ 让 📎 微亮(预判 a)。用计数器扛子元素 enter/leave 抖动。
  const [dragging, setDragging] = useState(false)
  const dragDepth = useRef(0)
  // 发送闸(与 submit funnel 同口径):空闲 input 态 + (降级 OR 确凿空闲框)才允许提交。
  // 绑到 发送/✨ 按钮的 disabled,让移动端用户看到「可发与否」的明确反馈(affordance),而非静默拒发。
  // 降级路径 certainInput 不可得(纯靠 /state 轮询),故只看 st.kind==='input'(理由见 submit 注释)。
  // 扩展:任一附件仍在上传时也禁发 —— 绝不把「还没落盘的路径」注入提交(否则里世界读到不存在的路径)。
  const canSend = st.kind === 'input' && (degraded || certainInput) && !attachments.anyUploading
  // 最近已知力度(从 localStorage 初始化,跨会话记忆);input 态 chip 读它而非恒空的 st.effort。
  const [lastEffort, setLastEffort] = useState(() => loadEffort(host, sid))
  // 当前模型:扫消息流末尾最近一条带 model 的 assistant 行(只读 store,不订阅整 items 数组避免无谓重渲——
  // zustand selector 仅在派生出的 model 字符串变化时触发,空流/无 model 时为 ''),供模型 chip 显示。
  const lastModel = useStore((s) => {
    for (let i = s.items.length - 1; i >= 0; i--) {
      const m = s.items[i].model
      if (m) return m
    }
    return ''
  })
  // 预判(b):里世界最近一条 assistant 文本是否「在要图」。只取末条 assistant 的 text(= 最近可见输出),
  // selector 返回字符串(仅它变才重渲),scanWantsImage 在下方按行扫(跳代码围栏、限末 ~12 行)。
  const lastAssistantText = useStore((s) => {
    for (let i = s.items.length - 1; i >= 0; i--) {
      const it = s.items[i]
      if (it.role === 'assistant' && it.text) return it.text
    }
    return ''
  })
  const [text, setText] = useState('')
  const [showPal, setShowPal] = useState(false)
  // 注:旧的「建议确认弹层」(sugCfm)已废除 —— ✨ 现在把建议填入 textarea 由用户编辑后经唯一漏斗
  // submit 发出(textarea 成为唯一缓冲),不再有「建议→并行直发」这条绕过漏斗的旁路(S3/S4)。
  const [intCfm, setIntCfm] = useState(false) // 中断确认:true=弹自绘 confirm(防误触打断 Claude)
  const [ultraArm, setUltraArm] = useState(false) // 长按已触发 ultracode(发送键变色 + 冒提示)
  // 按下态:由 pointerdown/up 直接驱动(不靠移动端时灵时不灵的 :active),按下那一刻必定缩放+提亮 ——
  // 直接解决「按下后不确定有没有按到」。
  const [pressed, setPressed] = useState(false)
  // 提交在飞标记:submit 把 donePaths 快照并进 act 那一刻 → 直到响应落定都为真。
  // 期间冻结附件改动(add/remove/retry)+ 禁发,杜绝「提交进行中改了附件集,导致已快照的路径与
  // 实际不符 / 新增项被随后的 reset 丢弃」的竞态(评审点名的 ATTACHMENT SUBMISSION RACE)。
  const [submitting, setSubmitting] = useState(false)
  const submittingRef = useRef(false) // 同步读取(事件处理里即时判,绕过 setState 异步)
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

  // 切会话时重新载入该会话的最近已知力度;并清空草稿(S15:草稿绝不跨会话/跨 sid 漂移,
  // 否则上个会话没发出去的残稿会乘下一次 Enter 灌进刚换上的新会话)。
  useEffect(() => {
    setLastEffort(loadEffort(host, sid))
    setText('')
    attachments.reset() // 附件同草稿:绝不跨会话漂移,否则上个会话的附件路径会乘下一次 Enter 灌进新会话
    // eslint-disable-next-line react-hooks/exhaustive-deps
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
  // 返回 sendAct 的 SendResult({ok, kind?}):submit funnel 据此区分「成功 / server floor 409 拒发 / 网络失败」,
  // 决定是否清空草稿、冒什么提示。act 自身仍只在传了 toast 时冒一句(409 会被 funnel 接管为更准的文案)。
  const act = (body: SendBody, toast?: string) => {
    const p = sendAct(host, tsess, body).then((r) => {
      if (toast) flash(r.ok ? toast : '失败:会话未在运行?')
      return r
    })
    // 降级态靠轮询刷新;WS 在线态由 detectState 自动更新,无需手动 poll。
    if (degraded) setTimeout(poll, 400)
    return p
  }
  // ── 唯一的「提交一条消息」漏斗(submit funnel)──
  // 所有「发聊天消息 / 斜杠命令 / 采纳建议」一律经此一处,落实「原子提交」三不变量:
  //   A 原子提交:act 带 clear:true → 后端打字前先 C-a C-k 清空里世界输入行(根因 A,杀拼接)。
  //   B 客户端 certain 闸:WS-live 路径下,仅当确凿空闲框(certainInput)才放行;非空闲拒发并保留草稿
  //     (根因 B 的快速 UX 闸)。降级路径 certainInput 不可得 → 退化为 st.kind==='input' 单条件
  //     (后端 isClaudeInput 本身就是 certain 信号,且 server floor 还会再核一次,故安全)。
  //   C server floor:后端在落键前重抓画面、非 input 一律 409(权威兜底,见 control.go)。
  // 提交成功才清空草稿;409(后端拒发)冒真实 kind 并【保留草稿】供空闲后重试 —— 杜绝「静默丢消息」。
  const stateLabel = (k?: CtrlState['kind']) =>
    k === 'busy' ? '设备忙,未发送' : k === 'select' ? '请先关闭菜单' : k === 'offline' ? '会话未在运行' : '未发送'
  const submit = (payload: string): void => {
    // 重入闸:一次提交在飞时拒绝再次提交(防双发,且让「冻结附件集」窗口语义清晰)。
    if (submittingRef.current) return
    // 附件融合(原生嵌图,不再拼进文本):把已成功上传的设备绝对路径作 images 字段随本次提交透传,
    // 由设备端「设系统剪贴板 → 发 C-v」原生粘贴成 `[Image #N]`(就像真在 Claude Code 里粘贴图片),
    // 而非旧版把 "/abs/path.png" 当字面文本拼进消息。这样:
    //   - 图片真正以「图」的形式随消息带上(里世界吐 [Image #N] 占位),而非一行容易被当普通文字的路径;
    //   - 文本就是用户原文(干净、可含空格/任意字符),不再受「路径拼接/空格转义/换行=提交」那套老约束。
    // 上传仍在飞:由 canSend(含 !anyUploading)禁发;这里取 donePaths 是「此刻已落盘成功项」快照
    //   (与下方冻结附件改动配合,杜绝提交期间集变化的竞态)。
    const paths = attachments.donePaths
    const t = payload.trim()
    // 纯文本为空但有图片 → 仍提交(纯「发图」消息);文本与图片都空才不发(绝不发孤零零的 clear+Enter)。
    if (!t && paths.length === 0) return
    // 客户端闸:WS-live 要求 certainInput;降级只看 st.kind(理由见上)。
    // 提交时刻从 store 直读最新值(而非用 render 时捕获的闭包值),避免陈旧闭包导致错判。
    const certainNow = useLiveStore.getState().certainInput
    if (st.kind !== 'input' || (!degraded && !certainNow)) {
      flash(stateLabel(st.kind === 'input' ? 'busy' : st.kind)) // 非空闲:冒原因、保留草稿、不发
      return
    }
    // 放行:原子提交(clear:true 让后端先清空里世界输入行,只送 textarea 内容)。
    // 置 submitting:从此刻到响应落定,附件改动(add/remove/retry)+ 再次提交均被冻结。
    submittingRef.current = true
    setSubmitting(true)
    const clearInflight = () => {
      submittingRef.current = false
      setSubmitting(false)
    }
    // 原子提交:clear:true 让设备先清空里世界输入行,只送 textarea 内容;images 让设备原生粘贴上传图。
    // (text 可为空、images 非空 = 纯发图;设备端会跳过打字、只 C-v 粘图后 Enter。)
    act({ text: t, enter: true, clear: true, images: paths })
      .then((r) => {
        if (r.ok) {
          flash('已发送')
          setText('') // 仅成功才清草稿
          attachments.reset() // 仅成功才清附件(附件集在提交期间被冻结,故 reset 不会误丢期间新增项)
        } else if (r.kind) {
          flash(stateLabel(r.kind)) // server floor 409:冒真实态,保留草稿+附件待重试
          // ── 409 后重新对齐里世界真态(/model-menu-stuck 修复)──
          // 病灶:server floor 在落键前重抓画面、判非 input → 409 带【后端权威 kind】。但旧实现只冒
          // 一句 toast 就完了 —— 从不把这份权威态写回 live store。与此同时,detectState 流在 TUI 重绘的
          // ~100ms 里可能抖出一帧 certain 的 input(applyDetect 会照单全收覆盖掉刚才的 select),于是
          // 设备明明还卡在 /model 菜单,前端却把 select 降成了 input:RichModelSelect 卸载、用户连那枚
          //「取消(Esc)」按钮都看不到 → 静默卡死、无路可退。
          // 修复:既然 409 已经告诉我们「后端此刻不是 input」,就主动去拿一份后端权威 /state 写回 store ——
          //   1) fetchState 取里世界真态(busy 带 verb/tokens、select 带 title/options/actions,正是富组件所需);
          //   2) applyDetect({state, certain:true}) 把它当确凿帧覆盖,select 重挂 RichModelSelect(取消=Esc 现身)、
          //      busy 重现忙碌条;
          //   3) markSettle 开一段宽限窗 —— 这段时间内 live 路径的 input 解析视为 provisional、只升不降
          //      (见 livestate.applyDetect),挡住紧随其后的瞬态 input 帧把刚救回的菜单又掀掉。
          // 格式无关:全程信后端 detectState 的结论,绝不在前端重解析 TUI;/model 菜单换了排版也照修。
          // 仅 WS-live 路径需要此救援(降级路径靠 1.8s 轮询 poll 自然刷 polled;此处对它无害且加速)。
          if (r.kind !== 'input') {
            fetchState(host, tsess)
              .then((fresh) => {
                if (fresh.kind !== 'input') {
                  // 后端仍说非 input → 把权威态写回 live store,让菜单/忙碌条重现且可操作。
                  useLiveStore.getState().applyDetect({ state: fresh, certain: true })
                  // 开宽限窗:挡住随后的瞬态 input 帧立刻又把它降级(只升不降,见 livestate)。
                  useLiveStore.getState().markSettle(600)
                }
              })
              .catch(() => {}) // 尽力而为的恢复:取态失败就静默(toast 已冒,草稿已保留)
            if (degraded) setTimeout(poll, 400) // 降级路径同步刷一次 polled(WS-live 由上面 applyDetect 接管)
          }
        } else {
          flash('失败:会话未在运行?') // 网络/其它失败:保留草稿+附件
        }
      })
      .finally(clearInflight) // 无论成败都解冻(失败时附件原样保留,可再次提交)
  }
  // navTo:把里世界菜单光标移到目标项的方向键序列(不含提交/切换),无需移动则返回 []。
  const navTo = (num: string): string[] => {
    const opts = st.options || []
    const ci = opts.findIndex((o) => o.cur)
    const ti = opts.findIndex((o) => o.num === num)
    if (ci < 0 || ti < 0 || ci === ti) return []
    return Array(Math.abs(ti - ci)).fill(ti > ci ? 'Down' : 'Up')
  }
  // 点选项 = 用方向键把里世界菜单光标移到目标项(不提交);按「确认」才回车提交。
  // 高亮跟随真实光标:WS 在线时 detectState ~150ms 内回报新 cur;降级时轮询回报。故不再做乐观本地高亮。
  const moveTo = (num: string) => {
    const keys = navTo(num)
    if (keys.length) act({ keys })
  }
  // 多选切换:把光标移到目标项后按 Space 勾/取消勾选。一次发键(导航 + Space 同批),
  // 避免拆成两次 sendkeys 造成「移动后未切换」的竞态。Space 后高亮/勾选态由下一帧 detectState 回报。
  const toggleAt = (num: string) => {
    act({ keys: [...navTo(num), 'Space'] }, '已切换')
  }
  // 普通发送 / ultracode 发送的公共出口:ultra=true 时消息尾部追加 " ultracode"。
  // 一律委派给唯一漏斗 submit —— 由它统一过 certain 闸 + 原子清空 + 成功才清草稿 + 409 保留草稿。
  const sendText = (ultra: boolean) => {
    const t = text.trim()
    // 图片仍在上传:整体等待,绝不只发文本把图漏掉(过去靠 disabled 静默拦,现在显式冒因)。
    if (attachments.anyUploading) {
      flash('图片上传中,请稍候…')
      return
    }
    // 允许「纯发图」:无文本但有已上传附件时也放行(submit 会把路径并进提交);两者都空才不发。
    if (!t && attachments.donePaths.length === 0) {
      // 不再静默:带了图却没就绪(失败/被移除)时把原因冒出来,免得「按了没反应」像发送坏了。
      if (attachments.items.length) flash('图片未就绪,点缩略图重试')
      return
    }
    // ultracode 只在有文本时追加(纯发图不挂 ultracode 关键词)。
    submit(ultra && t ? t + ' ultracode' : t)
  }
  const send = () => sendText(false)

  // ── 长按发送:pointerdown 即给按下态(is-pressed,即时反馈);≥400ms 触发强发
  //    (有文本→尾部追加 ultracode;纯图→普通发图)。pointerup 若长按已发则不再普通发送(无双发)。
  const onSendDown = () => {
    longFired.current = false
    setPressed(true) // 按下那一刻立即变形/变亮 —— 解决「不确定有没有按到」
    clearTimeout(pressTimer.current)
    pressTimer.current = window.setTimeout(() => {
      const t = text.trim()
      const hasImg = attachments.donePaths.length > 0
      if (!t && !hasImg) return // 没东西可发:不标记 longFired,留给 pointerup 的 send()(那里会按需冒因)
      longFired.current = true // 长按已发 → 抑制 pointerup 的普通发送
      if (t) {
        setUltraArm(true) // 有文本:按钮变色 + 冒「+ultracode」提示
        sendText(true)
        window.setTimeout(() => setUltraArm(false), 700) // 短暂保留高亮态,松手后渐隐
      } else {
        sendText(false) // 纯发图:ultracode 关键词只对文本有意义 → 直接普通发图(修「长按图片不发」)
      }
    }, LONGPRESS_MS)
  }
  const onSendUp = () => {
    setPressed(false)
    clearTimeout(pressTimer.current)
    if (longFired.current) {
      longFired.current = false
      return // 长按已发,跳过普通发送
    }
    send() // 轻点 = 普通发送
  }
  const onSendLeave = () => {
    // 指针滑出/取消:收起按下态 + 撤未触发的长按(避免误判),不发送。
    setPressed(false)
    clearTimeout(pressTimer.current)
  }

  // ── 附件入口接线(三入口共用 attachments.add)──
  // 提交在飞时(submittingRef)一律拒新增 —— 与「冻结附件集」窗口一致,绝不在已快照 donePaths 后混入新项。
  // (a) 文件选择器:📎 → 隐藏 input.click();选完把 files 喂 add,清空 input.value(允许连选同文件)。
  const onPickFiles = (e: ChangeEvent<HTMLInputElement>) => {
    if (!submittingRef.current) attachments.add(e.target.files)
    e.target.value = '' // 复位:同一文件再次选择仍触发 onChange
  }
  // (b) 粘贴:剪贴板里的 image/*(截图/复制图)→ 成附件。仅当确有图片项时 preventDefault,
  //     否则放过普通文本粘贴(不打断在 textarea 粘贴文字)。
  const onPaste = (e: ClipboardEvent) => {
    if (submittingRef.current) return // 提交在飞:不接新附件(纯文本粘贴也放过,交回浏览器默认行为)
    const items = e.clipboardData?.items
    if (!items) return
    const files: File[] = []
    for (let i = 0; i < items.length; i++) {
      const it = items[i]
      if (it.kind === 'file' && it.type.startsWith('image/')) {
        const f = it.getAsFile()
        if (f) files.push(f)
      }
    }
    if (files.length) {
      e.preventDefault() // 只在抓到图片时拦截,纯文本粘贴照常进 textarea
      attachments.add(files)
    }
  }
  // (c) 拖拽:dragenter/leave 用深度计数器扛子元素抖动;drop 取 files 喂 add 并复位高亮。
  const onDragEnter = (e: DragEvent) => {
    if (!Array.from(e.dataTransfer?.types || []).includes('Files')) return // 仅文件拖拽才高亮
    e.preventDefault()
    dragDepth.current++
    setDragging(true)
  }
  const onDragOver = (e: DragEvent) => {
    if (Array.from(e.dataTransfer?.types || []).includes('Files')) e.preventDefault() // 必须 preventDefault 才能 drop
  }
  const onDragLeave = () => {
    dragDepth.current = Math.max(0, dragDepth.current - 1)
    if (dragDepth.current === 0) setDragging(false)
  }
  const onDrop = (e: DragEvent) => {
    if (!Array.from(e.dataTransfer?.types || []).includes('Files')) return
    e.preventDefault()
    dragDepth.current = 0
    setDragging(false)
    if (!submittingRef.current) attachments.add(e.dataTransfer?.files) // 提交在飞:不接新附件
  }
  // 预判 📎 微亮(克制版,见上方设计说明):拖拽中(a),或里世界最近 assistant 输出在「要图」(b)。
  // (b) 仅 input 态扫描(空闲时才有意义,且避免 busy/select 帧无谓计算);scanWantsImage 内部跳代码围栏、限末 ~12 行。
  const attachHot = dragging || (st.kind === 'input' && scanWantsImage(lastAssistantText.split('\n')))

  let content: ReactNode
  if (engineMatch) {
    // 新读屏引擎命中某个 select 态(注册的全是 select,故非空即 select)→ 渲对应卡片。
    // 优先于旧 detectState:F1 无字形高亮也能接管。降级(WS 断、无引擎帧)时 engineMatch 为空 → 走下方旧分支。
    content = <EngineControl />
  } else if (st.kind === 'offline') {
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
      <BusyLine tokens={st.tokens} verb={st.verb} tip={st.tip} elapsed={st.elapsed} phase={st.phase} percent={st.percent} onInterrupt={() => setIntCfm(true)} />
    )
  } else if (st.kind === 'select') {
    // ── 富 select 客户端分类分发 ──
    // selectKind(st) 按固定优先级(model > permission > sessionScope > effort > confirm > multi
    // > list > generic)挑出第一个匹配的富组件;命中即用富 UI 渲染,'generic' 回落到下方既有通用渲染。
    // 全部富组件复用同一套 helpers(navTo/moveTo/toggleAt/act,均为本文件既有闭包),点击映射回
    // 与今天完全一致的发键路径——纯展示、零 parser/state 改动。
    const helpers: RichSelectHelpers = { navTo, moveTo, toggleAt, act }
    const kind = selectKind(st)
    // 多选菜单:任一选项带复选框态(checked!=undefined)。多选项点击 = 移动光标到该项并按 Space
    // 切换勾选(toggleAt);单选项点击 = 仅移动光标(moveTo),按「确认」才回车。复选框字形:
    // 勾选 ☑、未勾选 ☐(与 AskUserQuestionCard 的 ☑/□ 同族;单选项无复选框)。
    const isMulti = (st.options || []).some((o) => o.checked !== undefined)
    if (kind === 'model') {
      // 降级兜底(WS 断、无引擎帧):仍用旧卡。实时路径已由上方 engineMatch → EngineControl 接管。
      content = <RichModelSelect st={st} helpers={helpers} />
    } else if (kind === 'permission') {
      content = <RichPermissionSelect st={st} helpers={helpers} />
    } else if (kind === 'sessionScope') {
      content = <RichSessionScopeSelect st={st} helpers={helpers} />
    } else if (kind === 'effort') {
      content = <RichEffortSelect st={st} helpers={helpers} />
    } else if (kind === 'confirm') {
      content = <RichConfirmSelect st={st} helpers={helpers} />
    } else if (kind === 'multi') {
      content = <RichMultiSelect st={st} helpers={helpers} />
    } else if (kind === 'list') {
      content = <RichListSelect st={st} helpers={helpers} />
    } else {
    // ── generic 兜底:沿用既有通用 select 渲染(逐字保留,不回归单选/多选)。 ──
    content = (
      <div className="cbar col">
        {st.title && <div className="cbar-title">{st.title}</div>}
        {(st.options || []).map((o) => {
          const hasBox = o.checked !== undefined
          return (
            <button
              key={o.num}
              className={'cbtn opt' + (o.cur ? ' cur' : '') + (o.checked ? ' on' : '')}
              onClick={() => (hasBox ? toggleAt(o.num) : moveTo(o.num))}
            >
              {hasBox ? (o.checked ? '☑' : '☐') + ' ' : ''}
              {o.num}. {o.label}
            </button>
          )
        })}
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
          {/* 多选时已逐项渲染复选框+Space 切换,故隐去 action 行里那枚全局「切换」按钮(避免重复);
              仅当未带复选框态(纯靠底栏 Space 提示判定的多选)时保留它,确保仍有可用的切换入口。 */}
          {(st.actions || [])
            .filter((a) => !(isMulti && a.label === '切换'))
            .map((a, i) => (
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
    }
  } else {
    content = (
      // 空闲 input 态:纵向两段 —— 附件条 + 输入框(整宽)+ 动作工具行。
      // 合并改版:原独立的「快捷 chip 行(模型/力度/压缩/清空)」不再单占一行,而是连同 📎/斜杠/✨
      // 一起收进下方 .compose-actions 的横向可滚工具簇(.compose-tools);发送键独立常驻靠右。
      // 模型+力度合并成一枚「模型·力度」组合 chip(见 ComposeChips)。移动端少一行、更聚拢。
      // 拖拽接到整个 compose 容器:dragover 高亮(.drag-active)、drop 加附件(见 onDrag* 接线)。
      <div
        className={'cbar col compose' + (dragging ? ' drag-active' : '')}
        onDragEnter={onDragEnter}
        onDragOver={onDragOver}
        onDragLeave={onDragLeave}
        onDrop={onDrop}
      >
        {/* 次级工具行:一枚信息态「模型 chip(模型+版本+力度,可拉宽/换行)」+ 四枚 compact 图标按钮
            (压缩 / 清空 / 命令 / 建议,风格统一、定宽、等高)。行可换行(flex-wrap),不横滚。
            发送与附件【不在此行】—— 属「发消息」同业务,放到下方与输入框同处一行(.compose-row)。 */}
        <div className="compose-tools">
          {/* 模型 chip(opus 按钮,宽)+ 压缩 + 清空(compact 图标);均走唯一漏斗 submit。 */}
          <ComposeChips
            lastModel={lastModel}
            lastEffort={lastEffort}
            submit={submit}
            onRunCmd={onRunCmd}
            isInfoCmd={isInfoCmd}
          />
          {/* 命令:compact 图标按钮,开 SlashPalette(含 /compact、/clear 等全部命令)。 */}
          <button type="button" className="cchip cchip--icon" onClick={() => setShowPal(true)} title="斜杠命令" aria-label="斜杠命令">
            <span className="cchip-ico" aria-hidden>/</span>
          </button>
          {/* 建议:compact 图标按钮,与命令同处一行;常驻显示,无建议时禁用(dim)。点 → 填入 textarea(不并行直发,S3/S4)。 */}
          <button
            type="button"
            className="cchip cchip--icon cchip--sug"
            disabled={!st.suggest}
            title={st.suggest ? '采用推荐内容(填入输入框)' : '暂无推荐内容'}
            aria-label="采用推荐内容"
            onClick={() => st.suggest && setText(st.suggest)}
          >
            <span className="cchip-ico" aria-hidden>✨</span>
          </button>
        </div>
        {/* 附件缩略图条:核心 compose 行之上;空列表自动不占位(组件内返回 null)。
            提交在飞(submitting)时整条冻结:移除/重试不可点,杜绝改动正在提交的附件集(竞态防护)。 */}
        <AttachmentBar handle={attachments} disabled={submitting} />
        {/* 隐藏的原生 file input:📎 触发它;accept 同时收图片与任意文件,capture 让移动端唤起相机/相册。 */}
        <input
          ref={fileInputRef}
          type="file"
          accept="image/*,*/*"
          multiple
          capture="environment"
          style={{ display: 'none' }}
          onChange={onPickFiles}
        />
        {/* 核心 compose 行(同业务):📎 附件 + 输入框 + 发送 同处一行 —— 「加料 / 写 / 发」本是一回事。 */}
        <div className="compose-row">
          {/* 📎 文件选择器:点 → 唤起隐藏 input。预判微亮(attachHot):拖拽中 或 里世界在「要图」时 .is-hot。
              提交在飞时禁用(与冻结附件集一致,避免唤起选择器后回来已 reset 的窗口)。 */}
          <button
            className={'cbtn attach-btn' + (attachHot ? ' is-hot' : '')}
            disabled={submitting}
            onClick={() => fileInputRef.current?.click()}
            title="添加图片/文件"
            aria-label="添加图片或文件"
          >
            📎
          </button>
          <div className="cinput-wrap">
            <textarea
              className="cinput"
              rows={1}
              value={text}
              placeholder="输入消息…"
              onChange={(e) => setText(e.target.value)}
              onPaste={onPaste}
              onKeyDown={(e) => {
                // IME 合成态(中文输入法打拼音/选词/上屏用回车)不发送 —— 此时回车是「候选上屏/确认」,
                // 直接 send 会误触(常见:输英文时用回车把拼音原样上屏)。isComposing 为真即合成中,
                // 放过给输入法处理;keyCode===229 是老浏览器同义信号,一并兜住。合成结束后的回车才发送。
                // Cmd/Ctrl+Enter 始终发送(合成态也发):给「就是要发」的显式快捷键。
                const composing = e.nativeEvent.isComposing || e.keyCode === 229
                if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault()
                  send()
                  return
                }
                if (e.key === 'Enter' && !e.shiftKey && !composing) {
                  e.preventDefault()
                  send()
                }
              }}
            />
          </div>
          {/* 发送(与附件同处 compose 行):轻点=普通发送;长按≈400ms=有文本则追加 ultracode 强发、纯图则发图。
              不再用 disabled 硬拦(那会「按了毫无反应」且无从解释):改为始终可点 + 不可发态变暗(is-off),
              点了由 sendText/submit 漏斗按真实原因冒提示(上传中/设备忙/未就绪)。按下即 is-pressed 给即时反馈。 */}
          <button
            className={'cbtn primary send-btn' + (ultraArm ? ' ultra' : '') + (pressed ? ' is-pressed' : '') + (canSend ? '' : ' is-off')}
            aria-disabled={!canSend}
            onPointerDown={onSendDown}
            onPointerUp={onSendUp}
            onPointerLeave={onSendLeave}
            onPointerCancel={onSendLeave}
            onContextMenu={(e) => e.preventDefault()}
          >
            {ultraArm ? '+ultracode' : '发送'}
          </button>
        </div>
      </div>
    )
  }

  return (
    <>
      {showPal && (
        <SlashPalette
          onClose={() => setShowPal(false)}
          // 斜杠命令也走唯一漏斗:过 certain 闸 + clear:true 清空里世界输入行,绝不乘残稿一起提交。
          onPick={(cmd) => submit(cmd)}
          onRun={onRunCmd}
          isInfoCmd={isInfoCmd}
        />
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
