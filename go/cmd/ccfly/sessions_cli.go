package main

// sessions_cli.go — 会话控制入口:ccfly list / attach / new。
//
// 动机(防双写):同一 claude 会话若同时存在两个进程(裸终端一份 + --resume 又一份),
// 双方 append 同一 jsonl,消息交错。让 tmux 成为唯一控制入口:
//   list    列出全部会话(live = 已有可控 tmux pane);
//   attach  接入会话 —— 已 live 则 tmux attach 镜像现场;未 live 则先确定性杀掉既有
//           claude 进程(control.Takeover,fail-closed,定位不到且仍活跃 → 拒绝),
//           再 new-session -A + claude --resume 在 tmux 里重建;
//   new     在新 tmux 会话里起一个全新 claude(SessionStart hook 会登记 pane↔sid 真值表)。
// attach/new 以 exec 接管当前 TTY(进程被 tmux 客户端替换,体验同手敲 tmux)。

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/ccfly/ccfly/go/internal/control"
	"github.com/ccfly/ccfly/go/internal/mesh"
)

// cliClaudeDir 解析 --claude-dir(与 serve/connect 同名同义)并注入 control。
func cliClaudeDir(fs *flag.FlagSet) *string {
	return fs.String("claude-dir", os.Getenv("CCFLY_CLAUDE_DIR"),
		"Claude projects dir (env CCFLY_CLAUDE_DIR; default ~/.claude/projects)")
}

// sessionOpts 是新建/恢复会话时透传给 claude 的权限选项(new / attach / picker 共用)。
type sessionOpts struct {
	skipPerms bool   // → --dangerously-skip-permissions(跳过所有权限确认)
	permMode  string // → --permission-mode <mode>;"" 表示不传(用 claude 默认)
}

// permModes 是 claude --permission-mode 接受的取值;picker 的 'p' 也按此顺序循环。
var permModes = []string{"default", "acceptEdits", "plan", "bypassPermissions"}

// addPermFlags 给一个 FlagSet 注册权限相关 flag,返回承接其值的 sessionOpts。
func addPermFlags(fs *flag.FlagSet) *sessionOpts {
	o := &sessionOpts{}
	fs.BoolVar(&o.skipPerms, "dangerously-skip-permissions", false,
		"pass --dangerously-skip-permissions to claude (skip ALL permission prompts)")
	fs.BoolVar(&o.skipPerms, "yolo", false, "alias of --dangerously-skip-permissions")
	fs.StringVar(&o.permMode, "permission-mode", "",
		"pass --permission-mode to claude: default|acceptEdits|plan|bypassPermissions")
	return o
}

// validate 校验 permission-mode 取值(空 = 不传,合法)。
func (o sessionOpts) validate() error {
	if o.permMode == "" {
		return nil
	}
	for _, m := range permModes {
		if o.permMode == m {
			return nil
		}
	}
	return fmt.Errorf("invalid --permission-mode %q (want %s)", o.permMode, strings.Join(permModes, "|"))
}

// claudeArgs 把选项展开成追加到 claude 的命令行参数。skip 优先(它等价 bypassPermissions);
// permMode 为空或 default 不传(用 claude 缺省行为)。
func (o sessionOpts) claudeArgs() []string {
	if o.skipPerms {
		return []string{"--dangerously-skip-permissions"}
	}
	if o.permMode != "" && o.permMode != "default" {
		return []string{"--permission-mode", o.permMode}
	}
	return nil
}

// dirGroup — ccfly ls 的一个目录组:该 cwd 下的全部会话(组内已按最近活动倒序)。
type dirGroup struct {
	Cwd  string
	Rows []control.CLISessionRow
}

// groupByDir 把会话按 cwd 分组:组内按最近活动倒序(Age 升序 = 最新在前,live 同龄优先),
// 组间按「组内最新会话」倒序(最近干过活的目录排最上)。纯函数,便于单测。
func groupByDir(rows []control.CLISessionRow) []dirGroup {
	byCwd := map[string][]control.CLISessionRow{}
	for _, r := range rows {
		byCwd[r.Cwd] = append(byCwd[r.Cwd], r)
	}
	groups := make([]dirGroup, 0, len(byCwd))
	for cwd, rs := range byCwd {
		sort.SliceStable(rs, func(i, j int) bool {
			if rs[i].Age != rs[j].Age {
				return rs[i].Age < rs[j].Age
			}
			return rs[i].Live && !rs[j].Live
		})
		groups = append(groups, dirGroup{Cwd: cwd, Rows: rs})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].Rows[0].Age < groups[j].Rows[0].Age
	})
	return groups
}

// 每目录默认最多展示的会话条数(live 的永远全展示;-a 解除上限)。
const lsPerDirCap = 5

// runList — ccfly ls:以目录为核心的会话总览。
//   - 目录按「最近干活」倒序;组内会话按最近活动倒序;
//   - 每行给出**可直接复制执行**的接管命令:live → `tmux a -t <真 pane 名>`(panemap 真值表
//     解析,/clear 后名字残留也指向真 pane);离线 → `ccfly attach <sid8>`(先杀残留进程
//     再在 tmux 里 resume,防双写);
//   - 默认每目录最多 5 条(live 恒全展示),-a 看全部。
func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	dir := cliClaudeDir(fs)
	all := fs.Bool("a", false, "show all sessions (default: 5 most recent per directory)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ccfly ls [-a] [--claude-dir <dir>]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if *dir != "" {
		control.SetClaudeDir(*dir)
	}
	rows, err := control.CLISessions()
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("没有会话。`ccfly new [dir]` 在 tmux 里起一个新 claude。")
		return nil
	}

	// ANSI 仅在真终端上用;dot 两种着色的转义串等长(\x1b[32m vs \x1b[90m),
	// tabwriter 对每格的隐藏字节开销一致,列对齐不破。
	tty := false
	if fi, e := os.Stdout.Stat(); e == nil && fi.Mode()&os.ModeCharDevice != 0 {
		tty = os.Getenv("NO_COLOR") == ""
	}
	paint := func(code, s string) string {
		if !tty {
			return s
		}
		return "\x1b[" + code + "m" + s + "\x1b[0m"
	}

	groups := groupByDir(rows)
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	offline := 0
	for gi, g := range groups {
		if gi > 0 {
			fmt.Fprintln(w)
		}
		nLive := 0
		for _, r := range g.Rows {
			if r.Live {
				nLive++
			}
		}
		cwd := collapseHome(g.Cwd)
		if cwd == "" {
			cwd = "(未知目录)"
		}
		head := fmt.Sprintf("%s · %d 会话", cwd, len(g.Rows))
		if nLive > 0 {
			head += fmt.Sprintf(" · %d live", nLive)
		}
		fmt.Fprintln(w, paint("1", head)) // bold 目录头
		shown := 0
		for _, r := range g.Rows {
			if !r.Live && !*all && shown >= lsPerDirCap {
				continue // live 恒展示;离线超出上限的跳过(下面统一给 +N 提示)
			}
			shown++
			dot, state, attach := paint("90", "○"), "-", "ccfly attach "+r.Sid[:8]
			if r.Live {
				dot, state = paint("32", "●"), r.State
				if r.Tmux != "" {
					attach = "tmux a -t " + r.Tmux
				}
			} else {
				offline++
			}
			fmt.Fprintf(w, "  %s %s\t%s\t%s\t%s\t%s\n",
				dot, r.Sid[:8], fmtAge(r.Age), state, attach, trunc(r.Title, 40))
		}
		if rest := len(g.Rows) - shown; rest > 0 {
			fmt.Fprintf(w, "  %s\n", paint("90", fmt.Sprintf("… 还有 %d 个更早的会话(ccfly ls -a 查看)", rest)))
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Println()
	hint := "接管:直接执行行尾命令。"
	if offline > 0 {
		hint += "离线会话的 `ccfly attach` 会先清掉残留 claude 进程,再在 tmux 里 resume(防双写)。"
	}
	fmt.Println(paint("90", hint))
	return nil
}

// runAttach — ccfly attach|a [sid|sid8|cc-sid8]:
//   无参 → 交互式选择器(picker.go):目录选项目 → 选会话 → 接管;
//   带参 → 直接接管。已 live 则 tmux attach 镜像;未 live 先 Takeover(杀既有进程)
//   再 new-session -A 重建。两条入口殊途同归到 attachSid。
func runAttach(args []string) error {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	dir := cliClaudeDir(fs)
	opts := addPermFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ccfly a [--permission-mode <m>] [--dangerously-skip-permissions] [sid|sid8|cc-sid8]")
		fmt.Fprintln(os.Stderr, "  无参 = 交互式选择器(可接管已有会话、也可新建)")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if err := opts.validate(); err != nil {
		return err
	}
	if *dir != "" {
		control.SetClaudeDir(*dir)
	}
	if fs.NArg() < 1 {
		// 无参:进 TUI 选择器(可接管或新建,权限选项可现场切)。返回时终端已恢复,可安全 exec tmux。
		res, err := runPicker(*opts)
		if err != nil {
			return err
		}
		switch res.action {
		case pickAttach:
			return attachSid(res.sid, res.opts.claudeArgs())
		case pickNew:
			return newSession(res.dir, res.opts)
		default:
			return nil // 用户退出,无声结束
		}
	}

	rows, err := control.CLISessions()
	if err != nil {
		return err
	}
	sid := matchSid(fs.Arg(0), rows)
	if sid == "" {
		return fmt.Errorf("unknown or ambiguous session: %s (try `ccfly ls`)", fs.Arg(0))
	}
	return attachSid(sid, opts.claudeArgs())
}

// attachSid — 接管一个已解析的完整 sid(attach 的唯一落点,防双写语义在此)。
// claudeArgs 仅在「离线 resume 重建」时生效(live 会话只 attach,claude 已在跑)。
func attachSid(sid string, claudeArgs []string) error {
	rows, err := control.CLISessions()
	if err != nil {
		return err
	}
	live := false
	for _, r := range rows {
		if r.Sid == sid {
			live = r.Live
		}
	}
	if !live {
		// 未 live:接管语义 —— 杀掉既有 claude 进程(确定性定位;找不到且仍活跃会拒绝),
		// 然后由下面的 new-session -A 在 tmux 里 claude --resume 重建。
		res, err := control.Takeover(sid)
		if err != nil {
			return fmt.Errorf("takeover: %w", err)
		}
		if len(res.Killed) > 0 {
			fmt.Fprintf(os.Stderr, "ccfly attach: killed existing claude process %v\n", res.Killed)
		}
	}
	mesh.EnsureTmuxProxyEnv() // 据云端下发策略设好 CCFLY_TMUX_PROXY,CLIAttachArgs 据此注入会话
	return execTmux(control.CLIAttachArgs(sid, claudeArgs))
}

// runNew — ccfly new [dir]:新 tmux 会话里起全新 claude(默认当前目录)。
// 会话名 cc-<rand8>:真实 sid 启动后由 SessionStart hook 写进 panemap 真值表,
// 名字只是给人看的,解析一律走真值表。
func runNew(args []string) error {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	_ = cliClaudeDir(fs) // 接受同名 flag(new 本身不读盘,保持口径一致)
	opts := addPermFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ccfly new [--permission-mode <m>] [--dangerously-skip-permissions] [dir]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if err := opts.validate(); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return newSession(fs.Arg(0), *opts) // 显式给了目录 → 直接用
	}
	if stdinIsTTY() { // 无参 + 交互终端 → 目录浏览器选目录
		dir, ok, err := browseDir(".", opts)
		if err != nil {
			return err
		}
		if !ok {
			return nil // 用户取消,无声结束
		}
		return newSession(dir, *opts)
	}
	return newSession(".", *opts) // 非交互(脚本/管道):沿用当前目录
}

// newSession 在 dir 里起一个全新 claude 会话(带权限选项),exec 接管当前 TTY。
// runNew(CLI)与 picker 的「＋新建会话」共用同一落点。
func newSession(dir string, opts sessionOpts) error {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("not a directory: %s", dir)
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := "cc-" + hex.EncodeToString(b)
	// 代理环境注入:据云端下发并持久化的策略把 CCFLY_TMUX_PROXY 设好(用户已设则尊重),
	// 新建会话默认带好代理 + 局域网 bypass。
	mesh.EnsureTmuxProxyEnv()
	targs := append([]string{"-u", "new-session"}, control.TmuxProxyEnvArgs()...)
	// claude 命令拼成单个 shell 串(权限参数取值受控:enum mode / 固定 flag,无注入风险),
	// 与 CLIAttachArgs 的 resume 命令口径一致(tmux 单参 = 交 shell 跑)。
	claudeCmd := strings.Join(append([]string{"claude"}, opts.claudeArgs()...), " ")
	targs = append(targs, "-A", "-s", name, "-c", dir, claudeCmd)
	return execTmux(targs)
}

// execTmux 以 exec 替换自身为 tmux 客户端(接管 TTY;退出码/信号语义同手敲 tmux)。
func execTmux(args []string) error {
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found in PATH")
	}
	return syscall.Exec(tmux, append([]string{"tmux"}, args...), os.Environ())
}

// matchSid:完整 sid / 任意前缀 / cc-<sid8>,唯一命中才采信。
func matchSid(q string, rows []control.CLISessionRow) string {
	p := strings.TrimPrefix(q, "cc-")
	var hit string
	for _, r := range rows {
		if r.Sid == q {
			return q
		}
		if strings.HasPrefix(r.Sid, p) {
			if hit != "" && hit != r.Sid {
				return ""
			}
			hit = r.Sid
		}
	}
	return hit
}

func fmtAge(sec int64) string {
	switch {
	case sec < 0:
		return "?"
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm", sec/60)
	case sec < 86400:
		return fmt.Sprintf("%dh", sec/3600)
	default:
		return fmt.Sprintf("%dd", sec/86400)
	}
}

func collapseHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
