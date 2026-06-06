package control

// ctrlstate.go — 里世界(tmux/claude TUI)当前「控件状态」的判断器。
// 后端 capture 当前画面 → 解析成结构化状态,表世界(web/)据此渲染对应控件并经 sendkeys 驱动。
// 判断器放后端,前端永不读屏。kind:offline|busy|select|input。

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

type ctrlOption struct {
	Num   string `json:"num"`
	Label string `json:"label"`
	Cur   bool   `json:"cur"`
}
type ctrlAction struct {
	Label string   `json:"label"`
	Keys  []string `json:"keys,omitempty"`
	Text  string   `json:"text,omitempty"`
}
type ctrlState struct {
	Kind    string       `json:"kind"`
	Title   string       `json:"title,omitempty"`
	Options []ctrlOption `json:"options,omitempty"`
	Effort  string       `json:"effort,omitempty"`
	Actions []ctrlAction `json:"actions,omitempty"`
	Verb    string       `json:"verb,omitempty"`    // busy:工作动词(如 Zesting),从 spinner 行解析
	Tokens  string       `json:"tokens,omitempty"`  // busy:本轮 token 数(如 1.2k),从 busy 行解析
	Tip     string       `json:"tip,omitempty"`     // busy:里世界 Tip 行
	Elapsed string       `json:"elapsed,omitempty"` // busy:里世界真实运行时间(如 "7s"),从 spinner/interrupt 行解析;抓不到则前端本地兜底
	Hint    string       `json:"hint,omitempty"`    // input:底部提示行
	Suggest string       `json:"suggest,omitempty"` // input:里世界「输入建议」(Prompt suggestions,/config 开)解析出的整条建议文本;仅 input 态且确有建议时给
}

var (
	reOpt = regexp.MustCompile(`^\s*(❯|›|>)?\s*(\d+)[.)]\s+(\S.*?)\s*$`)
	// 力度行:任意单个圆点字符(◉◈✦○◐● 等都行,不枚举)+ …effort… + ←/→ to adjust。
	reEffort = regexp.MustCompile(`(?i)^\s*\S\s+(.+?effort.*?)\s*←/→\s*to adjust\s*$`)
	reFooter = regexp.MustCompile(`(?i)(\b(esc|enter)\b.*\bto\b|←/→\s*to adjust)`)
	reBusy   = regexp.MustCompile(`(?i)esc to interrupt`)
	// spinner 行:任意字形 + 大写开头单词 + …(如 "✢ Zesting…");尾部 … 排除已完成的 "Crunched for 7s"。
	reVerb   = regexp.MustCompile(`^\s*\S\s+([A-Z][a-zA-Z]+)…`)
	reTokens = regexp.MustCompile(`([\d.]+[kKmM]?)\s*tokens`)
	reTip    = regexp.MustCompile(`(?i)\bTip:\s*(.+?)\s*$`)
	// 运行时间:从 spinner / interrupt 行里抓「N s」(如 "(7s · …)" 或 "Crunched for 7s")。
	// 只在含 spinner 动词或 interrupt 字样的行上匹配,避免命中正文里的 "5s"。
	reElapsed  = regexp.MustCompile(`\b(\d+)s\b`)
	reTitle    = regexp.MustCompile(`(?i)^(select|choose|pick|do you|would you|permission|claude needs)`)
	reEnterTo  = regexp.MustCompile(`(?i)Enter\s+to\s`)
	reSessOnly = regexp.MustCompile(`(?i)\bs\s+to\s+use\s+this\s+session`)
	reWS       = regexp.MustCompile(`\s+`)
	// 输入框上下边框(claude 的 "─────"):claude 空闲输入框的上下沿,纯 ─ 一长串。
	// shell(zsh)的 '❯' 提示行没有这种边框,故 border 是「这是 claude 框」的强证据之一。
	reBorder = regexp.MustCompile(`^─{6,}\s*$`)
	// 输入框底栏提示行(空闲框下沿,如 "? for shortcuts · ← for agents" / "… to send" / "shift+tab")。
	// 与 livestate.ts 的 reInputHint 逐条对齐。busy 行 "esc to interrupt" 已被 reBusy 先吃、
	// select 行 "Enter to …/esc to cancel" 已先判 select,故此 hint 只在「非 busy、非 select」后
	// 用于「确认是 claude 输入框」,不抢 busy/select。shell 提示行不含这些字样。
	reInputHint = regexp.MustCompile(`(?i)(\?\s*for\s+shortcuts|for\s+agents|\bto\s+send\b|shift\s*\+\s*tab)`)
	// 输入建议行 / 真实输入行都以 "❯" 起头,但:
	//   真实输入行 = "❯" + U+00A0( ,不间断空格)+ 已输入文本
	//   建议行     = "❯" + 普通空格(U+0020)+ 建议全文(渲染在输入框上边框正上方那一格,空闲时该格为空行)
	//   历史用户气泡 = 同样 "❯ " 普通空格,但其后必跟 ⏺/spinner 回复行,且离输入框较远
	reInputLine = regexp.MustCompile(`^❯\x{00a0}`)
	// ANSI 转义序列(CSI / OSC 等),用于剥色与逐段属性扫描。
	reANSI = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)
)

// stripANSI 去掉所有 ANSI 转义序列(判定/解析吃无色文本)。
func stripANSI(s string) string { return reANSI.ReplaceAllString(s, "") }

func detectState(rawText string) ctrlState {
	// 入参为带 ANSI 的抓屏(handleState 用 -e);「输入建议」靠输入框里 dim 属性的鬼影文本识别,
	// 必须在剥色前抓。其余所有判定(busy/modal/options)一律吃剥色文本,逻辑零改动。
	suggest := parseSuggestANSI(rawText)
	text := stripANSI(rawText)
	lines := strings.Split(strings.TrimRight(text, " \n\r\t"), "\n")
	n := len(lines)
	tail := func(k int) []string {
		if n-k < 0 {
			return lines
		}
		return lines[n-k:]
	}

	// busy:生成中(claude 显示 "… (esc to interrupt)")。
	// 顺带解析 spinner 动词 / token 数 / Tip 行,喂给表世界还原原版 TUI(抓不到则前端兜底)。
	busy := false
	for _, ln := range tail(8) {
		if reBusy.MatchString(ln) {
			busy = true
			break
		}
	}
	if busy {
		st := ctrlState{Kind: "busy"}
		for _, ln := range lines {
			if st.Verb == "" {
				if m := reVerb.FindStringSubmatch(ln); m != nil {
					st.Verb = m[1]
				}
			}
			if st.Tip == "" {
				if m := reTip.FindStringSubmatch(ln); m != nil {
					st.Tip = strings.TrimSpace(m[1])
				}
			}
			// token 仅从 spinner / interrupt 行取,避免命中正文或历史里的 "tokens"。
			if st.Tokens == "" && (reBusy.MatchString(ln) || reVerb.MatchString(ln)) {
				if m := reTokens.FindStringSubmatch(ln); m != nil {
					st.Tokens = m[1]
				}
			}
			// 运行时间同样仅限 spinner / interrupt 行,避免误命中正文里的 "Ns"。
			if st.Elapsed == "" && (reBusy.MatchString(ln) || reVerb.MatchString(ln)) {
				if m := reElapsed.FindStringSubmatch(ln); m != nil {
					st.Elapsed = m[1] + "s"
				}
			}
		}
		return st
	}

	// 模态门槛:最后 6 行须有底栏(避免把正文/历史误判)
	modal := false
	for _, ln := range tail(6) {
		if reFooter.MatchString(ln) {
			modal = true
			break
		}
	}

	if modal {
		// 横向力度
		effort := ""
		for _, ln := range lines {
			if m := reEffort.FindStringSubmatch(ln); m != nil {
				effort = strings.TrimSpace(reWS.ReplaceAllString(m[1], " "))
				break
			}
		}
		// 编号选项:自底向上,≥2、从 1 起、含 ❯ 当前项
		var opts []ctrlOption
		started := false
		firstIdx := -1
		for i := n - 1; i >= 0; i-- {
			if m := reOpt.FindStringSubmatch(lines[i]); m != nil {
				opts = append([]ctrlOption{{Num: m[2], Label: reWS.ReplaceAllString(m[3], " "), Cur: m[1] != ""}}, opts...)
				started = true
				firstIdx = i
			} else if started {
				if strings.TrimSpace(lines[i]) == "" {
					continue
				}
				break
			}
		}
		hasCur := false
		for _, o := range opts {
			if o.Cur {
				hasCur = true
			}
		}
		if !(len(opts) >= 2 && opts[0].Num == "1" && hasCur) {
			opts = nil
		}

		if len(opts) > 0 || effort != "" {
			joinTail := strings.Join(tail(6), " ")
			actions := []ctrlAction{}
			if reEnterTo.MatchString(joinTail) {
				actions = append(actions, ctrlAction{Label: "确认", Keys: []string{"Enter"}})
			}
			if reSessOnly.MatchString(joinTail) {
				actions = append(actions, ctrlAction{Label: "本次", Text: "s"})
			}
			actions = append(actions, ctrlAction{Label: "取消", Keys: []string{"Escape"}})

			title := ""
			if firstIdx >= 0 {
				for y := firstIdx - 1; y >= 0 && y >= firstIdx-6; y-- {
					t := strings.TrimSpace(lines[y])
					if t == "" {
						continue
					}
					if strings.HasSuffix(t, "?") || strings.HasSuffix(t, ":") || strings.HasSuffix(t, "：") || reTitle.MatchString(t) {
						title = t
						break
					}
				}
			}
			if title == "" && len(opts) == 0 && effort != "" {
				title = "调整力度"
			}
			return ctrlState{Kind: "select", Title: title, Options: opts, Effort: effort, Actions: actions}
		}
	}

	// 默认分支:既非 busy 也非 select。此前这里无条件返回 Kind:"input" —— 这正是 bug 根因:
	// 一个停在 zsh '❯ ' 提示符的 tmux pane(claude 没在跑)也被判成 input,表世界给出发送框,
	// 用户的斜杠命令被打进 zsh("zsh: command not found: context")。
	//
	// 修正:只有「确认这是 claude 的输入框」才判 input,否则判 offline(pane 里不是 claude,
	// 表世界据此显示「会话未在运行 / 启动会话」而非发送框)。
	// 判据(与 livestate.ts 对齐):claude 输入框必有 纯 ─── 边框(reBorder)、或底栏提示行
	// (reInputHint),后端额外可靠的是 ❯+NBSP 的当前输入行(reInputLine);三者任一命中即确认。
	// shell 的 '❯' 用普通空格且无边框/提示,三条都不命中 → offline。
	if isClaudeInput(lines) {
		// 建议已在入口从带色抓屏解析好(parseSuggestANSI)。
		return ctrlState{Kind: "input", Suggest: suggest}
	}
	return ctrlState{Kind: "offline"}
}

// isClaudeInput 判断「当前屏是否确为 claude 的输入框」(用于默认分支区分 claude-idle vs shell 提示符)。
// 命中任一即确认:纯 ─── 边框(reBorder)/ 底栏提示行(reInputHint)/ ❯+NBSP 当前输入行(reInputLine)。
// 与 livestate.ts 的 hasIdlePrompt 逐条对齐;NBSP 一路为后端独有(tmux 原文里 NBSP 可靠,浏览器侧
// xterm 可能把 U+00A0 归一成普通空格,故客户端只用 border/hint,不用 NBSP)。
func isClaudeInput(lines []string) bool {
	for _, ln := range lines {
		if reBorder.MatchString(ln) || reInputHint.MatchString(ln) || reInputLine.MatchString(ln) {
			return true
		}
	}
	return false
}

// parseSuggestANSI — 从带 ANSI 的抓屏里解析里世界「输入建议」(Prompt suggestions)。
//
// claude 2.1.x 渲染实测(tmux capture -e):建议是输入框内的「鬼影补全文本」,直接接在
// 输入行 "❯"+U+00A0(空输入)之后,整段带 dim 属性(SGR 2);其首字符常压在反显光标块
// (SGR 7)下,余下为 dim。对比之下,用户真键入的文本是常规属性、光标块在文本尾部。形如:
//   空建议:  ESC[39m ❯ <U+00A0> ESC[7m <空格> ESC[0m              → 无 dim,无建议
//   有建议:  ESC[39m ❯ <U+00A0> ESC[7m g ESC[0;2m o ahead ESC[0m  → "go ahead"
//   真输入:  ESC[39m ❯ <U+00A0> hello world ESC[7m <空格> ESC[0m   → 常规属性,非建议
//
// 判据:在输入行(剥色后以 "❯"+U+00A0 起头)上,取带 dim(SGR 2)的那段文本为建议;若紧贴
// dim 段之前有一个反显(SGR 7)首字符,把它并回建议头(光标压住的就是建议首字)。无 dim 段则无建议。
func parseSuggestANSI(rawText string) string {
	for _, raw := range strings.Split(strings.TrimRight(rawText, " \n\r	"), "\n") {
		if !reInputLine.MatchString(stripANSI(raw)) {
			continue
		}
		if s := suggestFromInputLine(raw); s != "" {
			return s
		}
	}
	return ""
}

// suggestFromInputLine 逐字符扫一条带 ANSI 的输入行,按当前 SGR 属性收「鬼影建议」文本。
func suggestFromInputLine(raw string) string {
	var dim strings.Builder // dim 段(建议主体)
	head := ""              // 紧贴 dim 之前的反显单字符(建议首字)
	dimOn, revOn := false, false
	lastRev := ""
	i := 0
	for i < len(raw) {
		if raw[i] == 0x1b { // ANSI 转义:吃掉整段,更新 dim/reverse 状态
			loc := reANSI.FindStringIndex(raw[i:])
			if loc == nil || loc[0] != 0 {
				i++
				continue
			}
			seq := raw[i : i+loc[1]]
			applySGR(seq, &dimOn, &revOn)
			if dimOn && head == "" {
				head = lastRev // dim 段开始,把刚压在光标下的首字并入
			}
			i += loc[1]
			continue
		}
		r, sz := utf8.DecodeRuneInString(raw[i:])
		i += sz
		if dimOn {
			dim.WriteRune(r)
		} else if revOn {
			lastRev = string(r) // 记最近的反显单字符,dim 段开启时可能并入
		}
	}
	body := strings.TrimSpace(dim.String())
	if body == "" {
		return "" // 无 dim 段 → 空框或真实输入,均非建议
	}
	sug := strings.TrimSpace(strings.TrimSpace(head) + body)
	sug = strings.TrimSpace(strings.TrimPrefix(sug, "❯"))
	return sug
}

// applySGR 解析一条 SGR 序列(ESC[…m),更新 dim(2)/reverse(7) 开关;0/22 关 dim、0/27 关 reverse。
func applySGR(seq string, dimOn, revOn *bool) {
	if !strings.HasSuffix(seq, "m") || !strings.HasPrefix(seq, "\x1b[") {
		return
	}
	params := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m")
	if params == "" {
		params = "0"
	}
	for _, pp := range strings.Split(params, ";") {
		switch pp {
		case "0":
			*dimOn, *revOn = false, false
		case "2":
			*dimOn = true
		case "22":
			*dimOn = false
		case "7":
			*revOn = true
		case "27":
			*revOn = false
		}
	}
}
