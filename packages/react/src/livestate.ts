// livestate.ts —— 客户端 detectState:把 agent/ctrlstate.go 的判断器移植到浏览器,直接吃 xterm 解析后的 buffer。
//
// 与后端的本质差异:
//   后端吃「tmux capture -e」的带 ANSI 原文,先 stripANSI 再正则;suggest 靠逐字符扫 SGR(dim/reverse)。
//   浏览器里 xterm 已把 ANSI 解析进 buffer(每格带属性),故:
//     - 文本 = line.translateToString(true)(已是无色干净文本,无需 stripANSI);
//     - suggest 的 dim/reverse 直接读 cell.isDim()/cell.isInverse(),不再解析转义序列。
//
// 产出与 api.ts 的 CtrlState 同构(kind/title/options/effort/actions/verb/tokens/tip/elapsed/hint/suggest),
// 供 P3 切换 ControlBar/AgentDock 的消费源。本阶段只产出与暴露,不改消费方。
import { create } from 'zustand'
import type { Terminal, IBufferLine } from '@xterm/xterm'
import type { CtrlState } from './api'

// ── 正则:与 ctrlstate.go 逐条对齐 ──
// reOpt — 编号选项行,「编号 + 标签」之间插入可选复选框分组(g3),用于识别多选菜单(checkbox 风格,
// Space 切换、Enter 确认);因复选框可选,单选菜单(无复选框)照旧命中。
//   g1=当前项游标(❯/›/>) g2=编号 g3=复选框字形(可空) g4=标签
// 复选框字形:已勾选 ◉●☑■◼ 或 [x]/[X]/[✔];未勾选 ◯○□◻ 或 [ ]/[]。与 ctrlstate.go 逐条对齐。
const reOpt = /^\s*(❯|›|>)?\s*(\d+)[.)]\s+([◯◉○●☑■□◻◼]|\[[ xX✔]?\])?\s*(\S.*?)\s*$/
// reCheckOn / reCheckOff — 把复选框字形判成 勾选 / 未勾选;两者皆不命中(g3 空)= 单选项。
const reCheckOn = /^(?:[◉●☑■◼]|\[[xX✔]\])$/
const reCheckOff = /^(?:[◯○□◻]|\[\s?\])$/
// 多选确认底栏:claude 多选菜单底部含「Space to select / to toggle」一类提示。与 ctrlstate.go 对齐。
const reSpaceSel = /\bspace\b.*\bto\b.*\b(select|toggle)\b/i
const reEffort = /^\s*\S\s+(.+?effort.*?)\s*←\/→\s*to adjust\s*$/i
const reFooter = /(\b(esc|enter)\b.*\bto\b|←\/→\s*to adjust)/i
const reBusy = /esc to interrupt/i
// spinner 行:任意字形 + 大写开头单词 + …(如 "✢ Zesting…")。
const reVerb = /^\s*\S\s+([A-Z][a-zA-Z]+)…/
const reTokens = /([\d.]+[kKmM]?)\s*tokens/
const reTip = /\bTip:\s*(.+?)\s*$/i
// 压缩阶段(/compact):spinner 行 "✻ Compacting conversation…" + 下一行真实进度条 "▓▓░… N%"。
// reVerb 抓不到 Compacting(其后非紧贴 …),故单独识别并解析百分比。与 ctrlstate.go 逐条对齐。
const reCompacting = /\bCompacting\b/i
const rePercent = /(\d{1,3})\s*%/
const reElapsed = /\b(\d+)s\b/
const reTitle = /^(select|choose|pick|do you|would you|permission|claude needs)/i
const reEnterTo = /Enter\s+to\s/i
const reSessOnly = /\bs\s+to\s+use\s+this\s+session/i
const reWS = /\s+/g
// 「确认看到输入框」的行定位:claude 空闲时底部有个框,框内首行以 "❯ " 起头(NBSP 或普通空格)。
// 与下面 reInputLine 同源,但语义不同:reInputLine 用于 suggest 定位(dim 鬼影),这里用于
// 「确认空闲」—— 只有真在可见屏看到这条输入行,detectState 才敢把状态判成 input(idle)。
// 放宽:行首可有空白;❯ 后跟空格/NBSP 再带任意内容,或就是裸 ❯(空框尾随 NBSP 被 trimRight 掉)。
// 为何放宽 —— 这是 /context 冻结 bug 的根因:跑完 /context、/model 等本地命令后,claude 回到底部
// 输入框,框上方塞满静态输出(⛀⛁⛶ 区块、"Estimated usage by category" 表、力度块),这些上方内容
// 既非 busy 也非 select,不被识别。原 reInputPrompt 死认「❯ 在第 0 列、其后必跟一个空格/NBSP」,
// 一旦因 box 边框/主题缩进,或 translateToString(true) 把空输入框尾随的 NBSP 当空白裁掉(只剩裸
// "❯")而漏认,hasIdlePrompt 就返 false → detectState 标 certain:false → store 保留上一个 busy →
// 看着「卡死」。放宽后只要看到输入框就确凿判 input(与后端 ctrlstate.go「看到框即空闲」对齐),
// 同时仍要求确凿看到框(非空缓冲),保留抗 reflow/重连瞬态的能力。
// claude 空闲输入框的「上下边框」:纯 ─ 一长串(框上沿/下沿)。与 ctrlstate.go 的 reBorder 对齐。
// 取代旧的 reInputPrompt(认「裸 ❯ 或 ❯+空格」为空闲框):但 shell(zsh)的 '❯ ' 提示符
// 正是「❯+普通空格」→ 被误判成 claude 空闲框,用户斜杠命令被打进 zsh(本次修复的 bug 根因)。
// 改用 border —— shell 提示行没有 ─── 边框,据此区分 claude 框 vs shell 提示符。
const reBorder = /^─{6,}\s*$/
// 输入框底栏提示行(空闲框下沿,如 "? for shortcuts · ← for agents" / "… to send" / "shift+tab")。
// 第二条独立的「确认空闲」信号:输入行因渲染瞬态没匹配上 reInputPrompt 时,尾部见此提示也算确凿空闲。
// busy 行是 "esc to interrupt"(已被 reBusy 先吃),select 行是 "Enter to …/esc to cancel"(已先判 select),
// 故此 hint 只在「非 busy、非 select」时生效,不抢 busy/select。
const reInputHint = /(\?\s*for\s+shortcuts|for\s+agents|\bto\s+send\b|shift\s*\+\s*tab)/i
// ── shell(非 claude)证据:用于「未确认 claude 框」时区分 stable-shell vs 半重绘瞬态 ──
// 客户端只信「zsh 报错行」这一条不可被重绘伪造的强信号(command not found / no such file or directory)。
// 为何不用「行首 ❯+空格」或「非空缓冲+有 ❯ 行」这类结构启发(它们曾在此判 offline):xterm 在半重绘
// 期会把 claude 输入行那格 NBSP 当 NULL→渲染成普通空格(translateToString 用 WHITESPACE_CELL_CHAR),
// 于是一个正在重画的 claude 屏会短暂呈现 '❯ '(普通空格)或稀疏 '❯'+陈旧文本 —— 被这些启发误判成 shell,
// 把上一帧的 busy 硬翻成 offline(F7 违规)。zsh 报错行不会被 claude 的 reflow 伪造,故只认它:F1(含
// "command not found")仍翻 offline,F7(无报错行)落不进来 → certain:false → store 保留 last-known。
// 注:device 端(ctrlstate.go)读 tmux 原文,NBSP 可靠,故那边靠 ❯+NBSP 的 reInputLine 即可,无此问题、
// 也无此启发;两端保持对齐(都不含 bare-❯/稀疏缓冲启发)。
const reShellErr = /(command not found|no such file or directory)/i
// 输入行定位:行首 "❯" 后跟「NBSP(U+00A0)或普通空格(U+0020)」。
// 为何放宽:后端吃 tmux 原文,NBSP 与普通空格泾渭分明,故 reInputLine 死认 NBSP 即可区分
// 真实输入行 vs 建议/历史气泡(后两者用普通空格)。但浏览器侧文本来自 xterm 的
// translateToString/getChars,可能把 U+00A0 归一成普通空格 → 死认 NBSP 会永不命中,
// WS 在线态的 ✨ suggest 静默失效。故这里对 NBSP 与普通空格都放宽匹配,仅用来「定位输入行」。
// 真正区分 suggest(dim 鬼影)vs 真实输入,仍靠 cell 的 dim/inverse 属性(见 suggestFromLine)——
// 那才是判定信号;放宽不会误判:非建议行在 suggestFromLine 里无 dim 段会直接返回 ''。
const reInputLine = /^❯[\u0020\u00a0]/

// 可见区行范围 [start, end):对齐后端 `tmux capture-pane -p -e`(无 -S,仅当前可见屏 ~rows 行)。
// 锚点用 baseY(滚到底时视口顶行的 buffer 索引)而非 viewportY:LiveTerm 是恒在底部的无头镜像,
// 永不向上滚,故 baseY 就是「当前可见屏」的顶。扫 baseY..baseY+rows 这一屏,杜绝吸到滚上去的
// 陈旧 spinner/Tip/tokens/elapsed/effort/suggest(它们都只在当前屏有效)。
function visibleRange(term: Terminal): { start: number; end: number } {
  const buf = term.buffer.active
  const start = Math.max(0, buf.baseY)
  const end = Math.min(buf.length, start + term.rows)
  return { start, end }
}

// 从 xterm buffer 抽出可见区文本行(剥色后)。
function readLines(term: Terminal): { texts: string[]; lineCount: number } {
  const buf = term.buffer.active
  const texts: string[] = []
  // 仅取当前可见屏(与后端一致):全扫循环的「行集合」就是这一屏,不含 scrollback。
  const { start, end } = visibleRange(term)
  for (let y = start; y < end; y++) {
    const line = buf.getLine(y)
    texts.push(line ? line.translateToString(true) : '')
  }
  return { texts, lineCount: end - start }
}

// 尾窗 k 行(对齐 Go 的 tail)。
function tail(lines: string[], k: number): string[] {
  const n = lines.length
  return n - k < 0 ? lines : lines.slice(n - k)
}

// rstrip 全文尾部空白后按行切(对齐 Go 的 strings.TrimRight + Split)。
function toLines(texts: string[]): string[] {
  // texts 已是逐行;去掉末尾连续空行(translateToString 已逐行 trimRight)。
  let end = texts.length
  while (end > 0 && texts[end - 1].trim() === '') end--
  return texts.slice(0, end)
}

// ── suggest:在「真实输入行(❯+U+00A0)」上读 dim 段为建议主体,紧贴其前的 inverse 单字并回建议首字 ──
// 完全对应 ctrlstate.go 的 suggestFromInputLine,但属性来自 xterm cell(isDim/isInverse)而非 SGR 解析。
function parseSuggest(term: Terminal): string {
  const buf = term.buffer.active
  // 仅在当前可见屏找输入行(对齐后端「仅可见屏」),不吸滚上去的陈旧鬼影。
  const { start, end } = visibleRange(term)
  for (let y = start; y < end; y++) {
    const line = buf.getLine(y)
    if (!line) continue
    const text = line.translateToString(true)
    if (!reInputLine.test(text)) continue
    const s = suggestFromLine(line)
    if (s) return s
  }
  return ''
}

// 逐格扫一条输入行:收集 dim 段(建议主体);记最近 inverse 单字,dim 段开启时并入建议首字。
function suggestFromLine(line: IBufferLine): string {
  let dimBody = ''
  let head = '' // 紧贴 dim 之前的 inverse 单字(光标块压住的建议首字)
  let dimStarted = false
  let lastInverse = ''
  const len = line.length
  const cell = line.getCell(0)
  if (!cell) return ''
  for (let x = 0; x < len; x++) {
    const c = line.getCell(x, cell)
    if (!c) continue
    const chars = c.getChars()
    if (chars === '') continue // 空格/空格(宽字符尾格)等
    if (c.isDim()) {
      if (!dimStarted) {
        dimStarted = true
        head = lastInverse // dim 段开始 → 并入刚压在光标下的首字
      }
      dimBody += chars
    } else if (c.isInverse()) {
      lastInverse = chars // 记最近反显单字符,dim 段开启时可能并入
    }
  }
  const body = dimBody.trim()
  if (body === '') return '' // 无 dim 段 → 空框或真实输入,均非建议
  let sug = (head.trim() + body).trim()
  if (sug.startsWith('❯')) sug = sug.slice(1).trim()
  return sug
}

// detectState 的产物:除了 CtrlState 本体,带一个「这次判定是否确凿」的信号。
//   certain:true  —— 确凿看到了 busy / select / 清晰输入框(空闲)。store 可放心覆盖。
//   certain:false —— 既无 busy/select,又没看到清晰输入框(缓冲空 / 半重绘 / reflow 瞬态)。
//     这种「不确定」帧不能把 busy 降级成 input,store 应保留 last-known(见 applyDetect)。
// 之所以不直接在 CtrlState.kind 上加 'unknown':CtrlState 是与后端 /state 同构的契约(api.ts),
// 消费方(ControlBar/AgentDock)只认 offline/busy/select/input。把「确凿与否」收在 store 边界,
// 对外仍只暴露四态的 CtrlState,消费方零改动。
export interface DetectResult {
  state: CtrlState
  certain: boolean
}

// 「确认这是 claude 的空闲输入框」:可见屏内存在 纯 ─── 边框(reBorder,框上沿/下沿),
// 或尾部存在输入框底栏提示行(reInputHint,如 "? for shortcuts · ← for agents")。任一命中即确认。
// 这是把状态判成 input(idle)的硬门槛 —— 仅「这一帧没读到 busy」不算空闲(可能是 reflow/空缓冲/shell)。
//
// 关键修正:旧实现还认「裸 ❯ 或 ❯+空格」(reInputPrompt)为空闲框,但 shell(zsh)的 '❯ ' 提示符
// 正是这形态 → 误判成 claude 空闲框,用户斜杠命令被打进 zsh。现仅认 border/hint —— 这俩是 claude 框
// 独有、shell 提示符没有的结构。客户端不靠 ❯+NBSP(后端独有的可靠信号):xterm 可能把 NBSP 归一成
// 普通空格,据此区分会失效;border/hint 在浏览器侧稳定可见。
//
// 两路信号互为兜底:静态满屏(/context 等本地命令跑完)时,框上下沿 ─── 在屏,border 路命中;
// 万一某帧边框因重绘瞬态没匹配上,底栏 "? for shortcuts" 提示一般还在,hint 路兜住。两路都只在
// 「非 busy(reBusy 先吃)、非 select(reFooter+opts/effort 先判)」后才被调用,故不抢 busy/select。
function hasIdlePrompt(term: Terminal): boolean {
  const buf = term.buffer.active
  const { start, end } = visibleRange(term)
  for (let y = start; y < end; y++) {
    const line = buf.getLine(y)
    if (!line) continue
    if (reBorder.test(line.translateToString(true))) return true
  }
  // 兜底:尾部若见输入框底栏提示行,同样算确认是 claude 框。
  const last = end - 1
  for (let y = last; y >= start && y >= last - 6; y--) {
    const line = buf.getLine(y)
    if (!line) continue
    if (reInputHint.test(line.translateToString(true))) return true
  }
  return false
}

// 「这帧是不是一个稳定的 shell(非 claude)屏」:仅在 hasIdlePrompt 为 false(没看到 claude 框)时问。
// 用途:把默认分支的「未确认 claude 框」进一步劈成两类 ——
//   (a) stable shell  → 确凿 offline(claude 没在跑,表世界显示「会话未在运行 / 启动会话」);
//   (b) 半重绘/空缓冲瞬态 → 不确凿,保留 last-known(关键:别把 busy 误降成 offline,见 F7)。
// 唯一证据 = zsh 报错行("command not found" / "no such file or directory")。这是 shell 独有、claude
// 的 reflow 绝不会伪造的强信号:F1(真 shell,含报错)→ true → offline;F7(半重绘的 claude 屏,
// 可能短暂呈现 '❯ '/稀疏残屏但没有报错行)→ false → 不确凿、保留 last-known(busy 不被误翻 offline)。
// 刻意不再用「❯+普通空格」或「非空缓冲+有 ❯ 行」做证据:见上方 reShellErr 处注释(xterm 会把重绘中的
// NBSP 格渲染成普通空格,那两条会把正在重画的 claude 屏误判成 shell)。
function shellEvidence(lines: string[]): boolean {
  // 关键:只扫屏幕「底部 prompt 区」(尾 6 行),绝不扫 scrollback / 对话正文。
  // 真正表示「claude 没在跑」的 zsh 报错,紧贴 shell 提示符出现在屏底 —— 失败的
  // `claude --resume`(PATH 缺失)、被打进 zsh 的斜杠命令("zsh: command not found: /context")。
  // 而扫全缓冲会致命误判:编程会话里 claude 的输出 / 历史正文极常含「command not found」
  // 「no such file or directory」(本仓库的 PATH 调试会话满屏皆是 —— 见 cc-5dea5258),
  // 一个正在显示这类文字、但当帧没读到输入框边框(/cost 报告/工具输出/半重绘)的 live claude
  // 屏会被误判成 offline → certain:true 覆盖 last-known → ControlBar 翻成「会话未在运行 /
  // 启动会话」、斜杠按钮消失 → 用户发不了命令、信息卡也开不出来(本次修复的 bug 根因)。
  for (const ln of tail(lines, 6)) {
    if (reShellErr.test(ln)) return true
  }
  return false
}

// ── detectState:对齐 ctrlstate.go 的 detectState,吃 xterm buffer ──
// 返回 DetectResult(state + certain),由 store.applyDetect 决定是否覆盖 last-known。
export function detectState(term: Terminal): DetectResult {
  const suggest = parseSuggest(term)
  const { texts } = readLines(term)
  const lines = toLines(texts)
  const n = lines.length

  // busy:尾 8 行含 "esc to interrupt"。
  let busy = false
  for (const ln of tail(lines, 8)) {
    if (reBusy.test(ln)) {
      busy = true
      break
    }
  }
  if (busy) {
    const st: CtrlState = { kind: 'busy' }
    for (const ln of lines) {
      if (!st.verb) {
        const m = reVerb.exec(ln)
        if (m) st.verb = m[1]
      }
      if (!st.tip) {
        const m = reTip.exec(ln)
        if (m) st.tip = m[1].trim()
      }
      // token 仅从 spinner / interrupt 行取。
      if (!st.tokens && (reBusy.test(ln) || reVerb.test(ln))) {
        const m = reTokens.exec(ln)
        if (m) st.tokens = m[1]
      }
      // 运行时间同样仅限 spinner / interrupt 行。
      if (!st.elapsed && (reBusy.test(ln) || reVerb.test(ln))) {
        const m = reElapsed.exec(ln)
        if (m) st.elapsed = m[1] + 's'
      }
    }
    // 压缩阶段识别 + 进度百分比解析(与 ctrlstate.go 逐条对齐)。
    // 阶段:可见屏任一行命中 "Compacting" → phase=compacting,verb 兜底为 "Compacting"。
    // 百分比:不再受 spinner 行位置/窄窗约束 —— 进度条("▓▓░… N%")可能离 spinner 行任意远
    // (中间夹着上一条 prompt 的 dim 鬼影、背景色进度条格被 translateToString(true) 渲成空格、底部 "esc to interrupt"),
    // 故扫【整屏】抓 (\d{1,3})% 并取【最后一个】落在 0..100 的匹配 —— 鬼影里的陈旧数字在上、
    // 当前真实进度数字在进度条行(更靠下),取最后一个即取到权威值。抓不到则 percent 留空 → 前端不确定态,绝不臆造。
    let compacting = false
    for (const ln of lines) {
      if (reCompacting.test(ln)) {
        compacting = true
        break
      }
    }
    if (compacting) {
      st.phase = 'compacting'
      if (!st.verb) st.verb = 'Compacting'
      for (const ln of lines) {
        const m = rePercent.exec(ln)
        if (m) {
          const p = parseInt(m[1], 10)
          if (p >= 0 && p <= 100) st.percent = p // 不 break:取整屏最后一个 plausible 匹配
        }
      }
    }
    return { state: st, certain: true }
  }

  // 模态门槛:最后 6 行须有底栏。
  let modal = false
  for (const ln of tail(lines, 6)) {
    if (reFooter.test(ln)) {
      modal = true
      break
    }
  }

  if (modal) {
    // 横向力度
    let effort = ''
    for (const ln of lines) {
      const m = reEffort.exec(ln)
      if (m) {
        effort = m[1].replace(reWS, ' ').trim()
        break
      }
    }
    // 编号选项:自底向上,≥2、从 1 起、含 ❯ 当前项。
    // g3=复选框字形(多选独有,可空):勾选→checked=true、未勾选→false、无字形→undefined(单选)。
    let opts: { num: string; label: string; cur: boolean; checked?: boolean }[] = []
    let started = false
    let firstIdx = -1
    for (let i = n - 1; i >= 0; i--) {
      const m = reOpt.exec(lines[i])
      if (m) {
        const o: { num: string; label: string; cur: boolean; checked?: boolean } = {
          num: m[2],
          label: m[4].replace(reWS, ' '),
          cur: !!m[1],
        }
        const box = m[3]
        if (box) {
          if (reCheckOn.test(box)) o.checked = true
          else if (reCheckOff.test(box)) o.checked = false
        }
        opts.unshift(o)
        started = true
        firstIdx = i
      } else if (started) {
        if (lines[i].trim() === '') continue
        break
      }
    }
    const hasCur = opts.some((o) => o.cur)
    if (!(opts.length >= 2 && opts[0].num === '1' && hasCur)) opts = []

    if (opts.length > 0 || effort !== '') {
      const joinTail = tail(lines, 6).join(' ')
      // 多选判定:任一选项带复选框字形(checked!=undefined),或底栏含「Space to select/toggle」。
      const multi = opts.some((o) => o.checked !== undefined) || reSpaceSel.test(joinTail)
      const actions: { label: string; keys?: string[]; text?: string }[] = []
      // 多选:先给「切换(Space)」动作 —— 移动光标到目标项后按 Space 勾/取消勾选。
      if (multi) actions.push({ label: '切换', keys: ['Space'] })
      if (reEnterTo.test(joinTail)) actions.push({ label: '确认', keys: ['Enter'] })
      if (reSessOnly.test(joinTail)) actions.push({ label: '本次', text: 's' })
      actions.push({ label: '取消', keys: ['Escape'] })

      let title = ''
      if (firstIdx >= 0) {
        for (let y = firstIdx - 1; y >= 0 && y >= firstIdx - 6; y--) {
          const t = lines[y].trim()
          if (t === '') continue
          if (
            t.endsWith('?') ||
            t.endsWith(':') ||
            t.endsWith('：') ||
            reTitle.test(t)
          ) {
            title = t
            break
          }
        }
      }
      if (title === '' && opts.length === 0 && effort !== '') title = '调整力度'
      return { state: { kind: 'select', title, options: opts, effort, actions }, certain: true }
    }
  }

  // 走到这里:既无 busy,也无 select。三岔(与 ctrlstate.go 的默认分支对齐,客户端多一层瞬态保护):
  //   1) 确认是 claude 框(hasIdlePrompt:border/hint)→ 确凿空闲 input。
  //   2) 否则、但有稳定 shell 证据(shellEvidence)→ 确凿 offline:claude 没在跑(停在 zsh '❯ '),
  //      表世界据此显示「会话未在运行 / 启动会话」而非发送框。这正是「斜杠命令被打进 zsh」bug 的修复。
  //   3) 既非 claude 框、又无 shell 证据(空缓冲 / 半重绘 / reflow 瞬态,F7)→ 不确凿:
  //      返回一个 provisional input 帧但 certain:false,让 store 保留 last-known(别把 busy 误降级)。
  // 之所以把 offline 也判 certain:true:offline 是「确凿不是 claude」的稳定结论,需要覆盖 last-known
  // 才能让 ControlBar 从旧的 input/busy 切到「启动会话」;而瞬态(3)绝不给 certain,故不会误翻。
  if (hasIdlePrompt(term)) {
    return { state: { kind: 'input', suggest }, certain: true }
  }
  if (shellEvidence(lines)) {
    return { state: { kind: 'offline' }, certain: true }
  }
  return { state: { kind: 'input', suggest }, certain: false }
}

// ── zustand store ──
interface LiveState {
  state: CtrlState
  // certainInput:把 detectState 已算出却被丢弃的 certain 信号「提级」为发送前置条件。
  // 仅当 applyDetect 确凿提交了一个 {kind:'input'}(certain 的空闲框)时为 true;提交 busy/select/offline、
  // 或保留 last-known(certain:false 的 reflow/重连/「/context 冻结」held 帧)时为 false。
  // ControlBar 的 submit funnel 用它做 WS-live 路径的发送闸:非 certain-input 拒发(保留草稿),
  // 杜绝「错时机/错上下文」误发(根因 B 的客户端快速闸;权威兜底仍是后端 server floor)。
  certainInput: boolean
  // 降级:WS 未连上 / 未握手 / 还没收到任何输出 → true(此时 P3 应回退到后端 /state 轮询)。
  degraded: boolean
  // settle 窗口截止时刻(epoch ms)。> now 时,所有解析视为 provisional:即便 certain 也只「升级」
  // 不「降级」(保留 last-known),用于扛尺寸跳动 / reflow 的瞬态。0 = 无 settle 窗口。
  settleUntil: number
  // 最近一次确凿提交「非 input」(busy/select/offline)的时刻(epoch ms)。0 = 从未。
  // 用于 applyDetect 的迟滞(hysteresis)闸:刚落定一个 select/busy 后的极短窗口内(HYST_MS),
  // 单帧 certain 的 input 极可能是 TUI 重绘中途的半成品(/model 等菜单在重排那 ~100ms 会一闪 input)——
  // 据此把它判作瞬态、拒绝降级,保留 last-known。这是 /model-menu-stuck 在「无 409、纯 WS 抖动」
  // 路径下的兜底:server floor 重对齐(ControlBar)修「拒发后」,迟滞闸修「刚进菜单时的自发抖动」。
  lastNonInputTime: number
  // 直接覆盖(老接口,保留;但已不在热路径用)。
  setState: (s: CtrlState) => void
  // 解析入口:按「确凿与否 + settle 窗口」决定覆盖还是保留 last-known。
  applyDetect: (r: DetectResult) => void
  // 开一个 ms 毫秒的 settle 窗口(尺寸跳动 / 重连时调用)。多次调用取较晚截止。
  markSettle: (ms: number) => void
  setDegraded: (d: boolean) => void
  resetLive: () => void
}

// 同 kind 的两个 input 是否在「实质内容」上变了(suggest)。busy/select 的字段(verb/tokens/options…)
// 也会变,但那属于「同态刷新」,直接放行即可;这里只用于避免无谓的引用变更触发重渲染。
function sameState(a: CtrlState, b: CtrlState): boolean {
  if (a.kind !== b.kind) return false
  return JSON.stringify(a) === JSON.stringify(b)
}

// 迟滞窗口:刚确凿落定一个非 input(select/busy)后的这段时间内,拒绝单帧 certain 的 input 降级。
// 300ms 够覆盖 claude TUI 进/出菜单时那一两帧重绘半成品,又短到不会卡住「真的回到了输入框」——
// 用户真正退出菜单后的输入框会持续在屏,下一帧(>300ms 后)就放行。settle 窗口(800ms)管 reflow,
// 这条管「菜单内自发抖动」,二者正交叠加(都只挡 input 降级,绝不挡 busy/select 升级)。
const HYST_MS = 300

export const useLiveStore = create<LiveState>((set, get) => ({
  state: { kind: 'input' },
  certainInput: false,
  degraded: true,
  settleUntil: 0,
  lastNonInputTime: 0,
  setState: (s) => set({ state: s }),
  applyDetect: (r) => {
    const { state: prev, settleUntil, lastNonInputTime } = get()
    const now = Date.now()
    const provisional = now < settleUntil
    // 决策:何时用新解析覆盖 last-known?
    //   1) 不确凿(certain:false)—— 永不覆盖(reflow/空缓冲/重连瞬态),保留 last-known。
    //   2) settle 窗口内的「确认空闲(input)」—— 视为 provisional,不降级,保留 last-known。
    //      (settle 期间布局正在重排,即便此刻读到了输入框也可能是半成品;确认 busy/select 仍放行,
    //       因为那是「升级到更具体态」,不会丢思考中。)
    //   3) 其余(确凿 busy/select,或非 settle 期的确认 input)—— 覆盖。
    //
    // certainInput(发送闸信号):同口径计算 ——
    //   - 不确凿(1)/ provisional 期被挡住升级的 input(2)/ 保留 last-known 的 held 帧 → 一律 false
    //     (S8「/context 冻结」、S14 重连这些 held 帧正确地读作「非 certain-input」,客户端闸拒发,
    //      最终由 server floor 兜底放行真正空闲的提交);
    //   - 确凿提交一个 {kind:'input'} → true;确凿提交 busy/select/offline → false。
    if (!r.certain) {
      set({ certainInput: false })
      return
    }
    if (provisional && r.state.kind === 'input' && prev.kind !== 'input') {
      set({ certainInput: false })
      return
    }
    // 迟滞闸(anti-flap):刚落定非 input(select/busy)后的 HYST_MS 内,拒绝单帧 input 降级 ——
    // 那一帧极可能是菜单进/出时 TUI 重绘的半成品(/model-menu-stuck 的「自发抖动」路径)。
    // 与上面的 settle 闸同口径:只挡「非 input→input」的降级,绝不挡 busy/select 升级(它们 kind!=='input',
    // 不进此分支),也不影响 offline(offline 是确凿结论,需覆盖 last-known 才能切「启动会话」)。
    // 被挡时 certainInput 置 false(同 provisional 路径):发送闸保守拒发,真正空闲由下一帧或 server floor 放行。
    if (r.state.kind === 'input' && prev.kind !== 'input' && now - lastNonInputTime < HYST_MS) {
      set({ certainInput: false })
      return
    }
    const certainInput = r.state.kind === 'input'
    if (sameState(prev, r.state)) {
      // 同态不重写 state(避免无谓重渲),但 certainInput 仍按本帧刷新(它不参与 sameState 比较)。
      if (get().certainInput !== certainInput) set({ certainInput })
      return
    }
    // 确凿提交一个非 input(select/busy/offline)→ 记下时刻,供上面的迟滞闸在随后短窗内挡住瞬态 input。
    if (r.state.kind !== 'input') set({ state: r.state, certainInput, lastNonInputTime: now })
    else set({ state: r.state, certainInput })
  },
  markSettle: (ms) => {
    const until = Date.now() + ms
    if (until > get().settleUntil) set({ settleUntil: until })
  },
  setDegraded: (d) => set({ degraded: d }),
  // resetLive:仅「真正换会话(host/sid 变)」时调用 —— 把镜像状态归零、重新进入降级待握手。
  // 瞬时断连 / 重连不得调用它(那会把 busy 抹成 input,正是要修的丢状态)。
  resetLive: () => set({ state: { kind: 'input' }, certainInput: false, degraded: true, settleUntil: 0, lastNonInputTime: 0 }),
}))

// 消费侧便捷 hook。
export const useLiveState = (): CtrlState => useLiveStore((s) => s.state)
export const useLiveDegraded = (): boolean => useLiveStore((s) => s.degraded)
// certainInput 发送闸:WS-live 路径下,只有确凿空闲框才放行提交(见 ControlBar.submit)。
export const useLiveCertainInput = (): boolean => useLiveStore((s) => s.certainInput)
