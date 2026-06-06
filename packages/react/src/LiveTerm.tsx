// LiveTerm.tsx —— 常驻隐藏 xterm:浏览器内自管一个 @xterm/xterm 实例,经 ttyd.ts 连到当前 sid 的 tmux 会话(cc-<id8>)。
//
// 角色(里世界 ↔ 表世界的「镜像」):它是表世界里一份始终在线的里世界拷贝。WS 输出实时灌进 term.buffer,
// livestate.ts 据此逐 ~150ms 重算「当前控件状态」(busy/select/input + suggest),供 P3 的 ControlBar/AgentDock 消费,
// 取代「按需轮询后端 /state + capture」——前端从此本地读自己镜像的屏,毫秒级、无抓屏竞态。
//
// 隐藏但有像素:用 visibility:hidden 的离屏 div(仍有宽高),让 FitAddon 能算出真实列行并 resize tmux pane,
// 把 pane 钉成稳定尺寸(根除 80x24:无尺寸时 fit() 会退化成 80x24,模态/spinner 行会被换行打乱,判定就废了)。
//
// attach 目标如何确定:把与 terminalUrl() 同构的 args 交给 ttyd.ts —— arg[0]=cc-<id8>(tmux 会话名),
// arg[1]=cwd,arg[2]='claude --resume <sid>'。包装脚本据 arg[0] 做 `tmux new -A -s cc-<id8>`,同名即 attach 镜像。
import { useEffect, useRef } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { connect, type TtydConn } from './ttyd'
import { detectState, useLiveStore } from './livestate'
import { liveTermHandle } from './liveconn'
import { tmuxName, fetchState } from './api'

// 隐藏容器尺寸:够大以容纳 claude 模态/spinner 不折行(列数≈宽/字宽)。固定一份「桌面级」尺寸,
// 让 detectState 在稳定布局上工作(与可见 ttyd 端尺寸无关,各连接各自 resize 自己的 pane —— 见下「遗留」)。
const COLS_PX = 1100
const ROWS_PX = 640

export function LiveTerm({ host, sid, cwd }: { host: string; sid: string; cwd?: string }) {
  // wrap = 定位容器(隐藏时离屏 / 显示时铺满成可见浮层);hostRef = xterm 挂载点。
  const wrapRef = useRef<HTMLDivElement>(null)
  const hostRef = useRef<HTMLDivElement>(null)
  const applyDetect = useLiveStore((s) => s.applyDetect)
  const markSettle = useLiveStore((s) => s.markSettle)
  const setDegraded = useLiveStore((s) => s.setDegraded)
  const resetLive = useLiveStore((s) => s.resetLive)

  useEffect(() => {
    const el = hostRef.current
    const wrap = wrapRef.current
    if (!el || !wrap) return
    resetLive()

    // 显隐状态:隐藏时是「只读镜像」(离屏、不收键盘),显示时是「可交互终端」(铺满、收键盘走统一发键层)。
    let shown = false

    // 省电参数:不闪光标、不显示滚动、关换行重排开销(尺寸固定故无所谓)。
    // disableStdin:false —— 允许 xterm 触发 onData;但隐藏(镜像)态我们在 onData 里直接丢弃,绝不改里世界。
    const term = new Terminal({
      cursorBlink: false,
      cursorInactiveStyle: 'none',
      disableStdin: false,
      scrollback: 2000,
      allowProposedApi: true,
      convertEol: false,
      fontFamily: 'monospace',
      fontSize: 13,
      theme: { background: '#0f1115', foreground: '#e6e8eb' },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(el)
    liveTermHandle.term = term

    // fit():按容器像素算列行,锁定后通知里世界 resize(钉住 pane,根除 80x24)。
    const doFit = () => {
      try {
        fit.fit()
      } catch {
        /* 容器还没布局好,忽略 */
      }
    }
    doFit()

    let conn: TtydConn | null = null
    let recalcTimer = 0
    let writeAcc = false
    let settleTimer = 0 // settle 窗口结束后做一次「干净重算」的定时器
    let degradeTimer = 0 // 断连后「宽限期」定时器:宽限内重连成功则取消,长时间连不上才真降级
    let everConnected = false // 是否曾握手成功过(首连失败立即降级;已连过则走宽限)
    let gateTimer = 0 // 探活轮询定时器:非 live 会话不连 /term,周期性探活,变 live 即接上镜像
    let disposed = false // effect 已清理标记(异步探活回来后据此中止,避免对已卸载的 term 连边)

    // 断连宽限:瞬时断连 / 重连不应立刻翻 degraded(那会让消费方切到 /state 轮询、丢 last-known)。
    // 真正长时间连不上(> GRACE)才降级,回退 /state 轮询(ControlBar/AgentDock 的兜底)。
    const DEGRADE_GRACE_MS = 3000

    // settle 窗口时长:尺寸跳动 / 重连后,布局会重排几帧(reflow / 重绘 / 半屏)。这段时间内的解析
    // 视为 provisional —— 只升级不降级(见 livestate.applyDetect),跳完再按新布局干净重算一次。
    const SETTLE_MS = 800

    // 开 settle 窗口 + 安排窗口末尾的干净重算。多次触发(尺寸来回跳)会把末尾重算推后,合并为一次。
    const openSettle = () => {
      markSettle(SETTLE_MS)
      if (settleTimer) clearTimeout(settleTimer)
      settleTimer = window.setTimeout(() => {
        settleTimer = 0
        // 窗口已过,此刻 provisional 解除:按稳定的新布局做一次确凿重算(busy↔input 此时才允许切换)。
        scheduleRecalc()
      }, SETTLE_MS + 80)
    }

    // 节流重算(~150ms):term 写入后置一次延迟重算,期间多次写入合并为一次 detectState。
    // applyDetect(非 setState):带 certain 标记,settle 窗口内/不确凿帧不降级 busy(见 livestate)。
    const scheduleRecalc = () => {
      if (recalcTimer) {
        writeAcc = true
        return
      }
      recalcTimer = window.setTimeout(() => {
        recalcTimer = 0
        const again = writeAcc
        writeAcc = false
        try {
          applyDetect(detectState(term))
        } catch {
          /* 解析失败不致命,保留上次状态 */
        }
        if (again) scheduleRecalc() // 节流窗口内又有写入 → 再排一次,吃掉尾部更新
      }, 150)
    }

    // 订阅解析完成(比 onRender 更贴近「数据已落 buffer」),节流重算。
    const subWrite = term.onWriteParsed(() => scheduleRecalc())

    // 尺寸跳动(里世界 tmux 共享窗口随别端鼠标活动而 reflow → 镜像 xterm 收到 SIGWINCH 等价的 resize):
    // 此刻屏缓冲正被 reflow / 清屏重绘,detectState 可能瞬时读不到 busy 行。开 settle 窗口扛住,
    // 并主动 refit + 重渲染,让 xterm 按新尺寸重画完整布局,settle 末尾再干净重算。
    const subResize = term.onResize(() => {
      openSettle()
      // 让 xterm 按新列行重绘(刷新视口),避免半重绘残留干扰下一次解析。
      try {
        term.refresh(0, term.rows - 1)
      } catch {
        /* ignore */
      }
    })

    // 键盘 → 里世界:仅在「显示(可交互)」态把本地键入经 WS INPUT 帧直灌 stdin(打字/回车/退格等)。
    // 隐藏(镜像)态一律丢弃,绝不改里世界。语义控制键(方向/Esc/^C)由表世界控件经 /sendkeys 发,
    // 但可见终端里用户原生敲的这些键(已是 xterm 编码好的转义序列)直接当裸字节走 WS 即可,无需再映射。
    const subData = term.onData((d) => {
      if (!shown) return
      if (conn && conn.ready()) conn.sendInput(d)
    })

    // 建连:首帧用 fit 算出的列行;attach 目标 = cc-<id8>(同 terminalUrl 的 args)。
    const startConn = () => {
      const cols = term.cols || 80
      const rows = term.rows || 24
      conn = connect(
        host,
        tmuxName(sid),
        {
          onOpen: () => {
            everConnected = true
            // 重连成功 → 取消「待降级」宽限定时器(若有),不翻 degraded。
            if (degradeTimer) {
              clearTimeout(degradeTimer)
              degradeTimer = 0
            }
            // 连上即同步一次尺寸(确保里世界 pane = 本地列行)。
            if (conn) conn.resize(term.cols, term.rows)
            // 重连后服务端会重灌一屏(attach 重绘)—— 开 settle 窗口,期间解析视为 provisional,
            // 不让「半重绘的空帧」把 busy 降级成 input。
            openSettle()
            // 握手后短延时再判降级:等首批输出落屏。
            setTimeout(() => setDegraded(!conn || !conn.ready()), 600)
          },
          onOutput: (data) => {
            term.write(data)
            if (conn && conn.ready()) setDegraded(false)
          },
          // 断连不立刻降级、不 resetLive:保留 last-known(尤其 busy)。开宽限定时器,
          // 宽限内 onOpen 会取消它;长时间(> GRACE)连不上才真降级,回退 /state 轮询。
          // 首连还没成功过就断(everConnected=false)→ 立刻降级(初始就该走兜底)。
          onClose: () => {
            if (!everConnected) {
              setDegraded(true)
              return
            }
            if (degradeTimer) return
            degradeTimer = window.setTimeout(() => {
              degradeTimer = 0
              if (!conn || !conn.ready()) setDegraded(true)
            }, DEGRADE_GRACE_MS)
          },
        },
        {
          cwd: cwd || '',
          // 被动镜像只 attach、绝不 spawn 里世界:故意不传 resumeCmd。
          // 原因(本次修复):对非 live 会话,/term 的 `tmux new-session -A -s cc-x claude --resume <sid>`
          // 会新建会话跑 claude --resume,而它常常立刻退出(claude 不在 service PATH、或该 sid 正被别处占用),
          // 于是 PTY EOF → WS 断 → ttyd 退避重连 → 再 spawn → 再退出……会话忽生忽灭,degraded 来回翻,
          // ControlBar 在「发送框 ↔ 会话未运行」间抖,斜杠按钮跟着闪、点不中。
          // 现在镜像只对「已在跑」的会话 attach(见下 ensureConn 的探活门),起会话改由「启动会话」按钮走 /start 显式做。
          cols,
          rows,
        },
      )
      liveTermHandle.conn = conn // 暴露给统一发键层(WS INPUT 轨)
    }

    // 探活门:被动镜像绝不自动 spawn —— 仅当该 tmux 会话「已在跑」(/state 非 offline)才连 /term(attach)。
    // 非 live → 不连、保持降级,让 ControlBar 稳定显示「会话未运行 / 启动会话」(由按钮走 /start 显式启动);
    // 每 2.5s 探一次,一旦会话变 live(用户点了启动、或别端起了它)即自动接上镜像。一旦连上就交给 ttyd 自管重连。
    const ensureConn = () => {
      if (disposed || conn) return
      fetchState(host, tmuxName(sid))
        .then((stt) => {
          if (disposed || conn) return
          if (stt.kind !== 'offline') {
            startConn() // 已在跑 → attach(不 spawn)
          } else {
            setDegraded(true) // 明确降级:消费方走 /state 轮询,稳定显示离线态(不抖)
            gateTimer = window.setTimeout(ensureConn, 2500)
          }
        })
        .catch(() => {
          if (disposed || conn) return
          setDegraded(true)
          gateTimer = window.setTimeout(ensureConn, 2500)
        })
    }
    ensureConn()

    // 容器尺寸变化(显隐切换会改宽高)→ refit + 通知里世界把 pane 钉到新列行。
    const ro = new ResizeObserver(() => {
      doFit()
      if (conn) conn.resize(term.cols, term.rows)
    })
    ro.observe(el)

    // ── 显隐句柄(P4 秒切)──
    // 同一份 xterm + 同一条 WS 连接,显隐只动 DOM 定位/可见性 —— 不重连、不重建,毫秒级。
    // 显示:wrap 由「离屏镜像」变「视口浮层」,host 切深色可读样式 → refit(按浮层真实尺寸算列行)+ resize pane + 聚焦收键盘。
    // 隐藏:wrap 退回离屏,disableStdin 复位,refit 回桌面级尺寸(保持 detectState 在稳定布局上工作)。
    const applyHidden = () => {
      wrap.style.left = '-10000px'
      wrap.style.right = ''
      wrap.style.top = '0'
      wrap.style.bottom = ''
      wrap.style.width = COLS_PX + 'px'
      wrap.style.height = ROWS_PX + 'px'
      wrap.style.visibility = 'hidden'
      wrap.style.pointerEvents = 'none'
      wrap.style.zIndex = '-1'
      wrap.style.padding = '0'
      wrap.style.background = ''
    }
    const applyShown = () => {
      wrap.style.left = '0'
      wrap.style.right = '0'
      wrap.style.bottom = '0'
      wrap.style.top = ''
      wrap.style.width = ''
      // 浮层高度:占视口约 62%(下半屏弹出),给终端足够行数又不全遮上文。
      wrap.style.height = 'min(62vh, 520px)'
      wrap.style.visibility = 'visible'
      wrap.style.pointerEvents = 'auto'
      wrap.style.zIndex = '60'
      wrap.style.padding = 'calc(env(safe-area-inset-bottom)) 8px 8px'
      wrap.style.background = '#0f1115'
    }

    liveTermHandle.refit = () => {
      doFit()
      if (conn) conn.resize(term.cols, term.rows)
    }
    liveTermHandle.isShown = () => shown
    liveTermHandle.show = () => {
      if (shown) return
      shown = true
      applyShown()
      // 等浮层布局生效(下一帧)再 fit:此时容器已是真实可见尺寸。
      requestAnimationFrame(() => {
        doFit()
        if (conn) conn.resize(term.cols, term.rows)
        term.focus()
      })
    }
    liveTermHandle.hide = () => {
      if (!shown) return
      shown = false
      term.blur()
      applyHidden()
      // 退回离屏后回桌面级列行(detectState 依赖稳定大尺寸)。
      requestAnimationFrame(() => {
        doFit()
        if (conn) conn.resize(term.cols, term.rows)
      })
    }
    applyHidden()

    return () => {
      disposed = true
      if (gateTimer) clearTimeout(gateTimer)
      if (recalcTimer) clearTimeout(recalcTimer)
      if (settleTimer) clearTimeout(settleTimer)
      if (degradeTimer) clearTimeout(degradeTimer)
      subWrite.dispose()
      subResize.dispose()
      subData.dispose()
      ro.disconnect()
      if (conn) conn.close()
      term.dispose()
      if (liveTermHandle.term === term) liveTermHandle.term = null
      liveTermHandle.conn = null
      liveTermHandle.show = () => {}
      liveTermHandle.hide = () => {}
      liveTermHandle.refit = () => {}
      liveTermHandle.isShown = () => false
      resetLive()
    }
    // host/sid/cwd 变 → 整体重建(换会话)。setState/setDegraded/resetLive 是 zustand 稳定动作,不入依赖。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [host, sid, cwd])

  // 定位容器:初始离屏镜像态(尺寸/可见性由上面的 applyHidden/applyShown 在 effect 里接管)。
  // 离屏时 visibility:hidden + 视口外,但保留宽高让 FitAddon 能测量;显示时变下半屏可见浮层。
  return (
    <div
      ref={wrapRef}
      style={{
        position: 'fixed',
        left: '-10000px',
        top: 0,
        width: COLS_PX + 'px',
        height: ROWS_PX + 'px',
        visibility: 'hidden',
        pointerEvents: 'none',
        overflow: 'hidden',
        zIndex: -1,
        boxShadow: '0 -8px 32px rgba(0,0,0,0.6)',
        borderTop: '1px solid var(--bd)',
      }}
    >
      <div ref={hostRef} style={{ width: '100%', height: '100%' }} />
    </div>
  )
}
