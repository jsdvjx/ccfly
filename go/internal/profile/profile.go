// Package profile 定义 ccfly 的运行期「能力档」(即用户类型):
//
//   - full       —— 默认,行为与历史完全一致(全部功能开)。
//   - host       —— VM 主机代理(ccfly-hostd):接入 overlay + 用 expose 桥把 spawn API 暴露到 overlay +
//     自装常驻服务;关掉用不到的 claude 登录 / 会话代理注入 / UI 同步。
//   - instance   —— 托管「普通用户实例」:允许受控 connect 接入 cc.hn overlay(被 Hub 反代纳管),
//     但禁 overlay 端口转发(expose/forward)、禁向会话注入 mesh 代理(实例用用户自带 env)、
//     禁 claude 账号登录、禁安装常驻服务、禁运行期拉 UI。
//   - restricted —— 纯受限:连 connect 接入也禁,只留本地 serve + 查看/驱动会话。
//
// 能力位(由 Mode 派生,见 forMode):
//
//	MeshJoin       connect / Pair / mesh.Connect 接入 overlay
//	OverlayBridge  --overlay-expose / --overlay-forward 端口转发
//	MeshProxy      向 tmux 会话注入出网代理 env(CCFLY_TMUX_PROXY / NODE_EXTRA_CA_CERTS)
//	Claude         ccfly claude login / logout
//	Install        install / uninstall / svc.Install 常驻服务
//	UISync         运行期从 npm 拉新 web UI
//
// 解析(「最严格者胜、env 只能降权」,严格度 restricted > instance > host > full;各档能力为全序子集):
//
//  1. 编译期默认 defaultMode —— 由 `-ldflags "-X github.com/jsdvjx/ccfly/go/internal/profile.defaultMode=instance"`
//     注入;缺省 "full"(npm 分发不注入,保持现状,对现有用户零影响)。
//  2. root 拥有的策略文件 /etc/ccfly/profile.json(可用 $CCFLY_PROFILE_FILE 覆盖):{"mode":"instance"}。
//  3. 环境变量 CCFLY_PROFILE。
//
// 三者取「严格度最高者」—— 任一来源更严即更严;env / 文件都不能把更严的编译默认「升回」更宽松
// (restricted 一旦判定即粘住;instance 的编译默认也无法被 env=full 升回)。
//
// 安全模型:这是「功能开关」,不是鉴权边界。真正的硬边界由镜像同时做到 —— 把 defaultMode 编进去
// (env / 文件无法升权)+ 容器以非 root 运行(改不动 /etc/ccfly/profile.json、也换不掉二进制)。
package profile

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
)

// 模式名。
const (
	ModeFull       = "full"
	ModeHost       = "host"
	ModeInstance   = "instance"
	ModeRestricted = "restricted"
)

// defaultProfileFile 是 root 策略文件的默认路径;可用环境变量 CCFLY_PROFILE_FILE 覆盖。
const defaultProfileFile = "/etc/ccfly/profile.json"

// defaultMode 是编译期默认能力档,可被 -ldflags 覆盖。"full" | "instance" | "restricted"。
// 缺省 "full" —— 不注入时(如 npm 分发)保持现状。
var defaultMode = "full"

// Profile 是解析后、不可变的能力档。各布尔位由 Mode 派生(见 forMode)。
type Profile struct {
	Mode          string // "full" | "instance" | "restricted"
	MeshJoin      bool   // connect / Pair / mesh.Connect 接入 overlay
	OverlayBridge bool   // --overlay-expose / --overlay-forward 端口转发
	MeshProxy     bool   // 向 tmux 会话注入出网代理 env
	Claude        bool   // ccfly claude login / logout 账号管理
	Install       bool   // install / uninstall / svc.Install 常驻服务
	UISync        bool   // 运行期从 npm 拉新 web UI
}

var (
	once   sync.Once
	cached Profile
)

// Current 返回本进程的能力档。只解析一次(进程内不可变),随后返回缓存。
func Current() Profile {
	once.Do(func() { cached = resolve() })
	return cached
}

// Restricted 是 Current().Mode == ModeRestricted 的便捷判定。
func Restricted() bool { return Current().Mode == ModeRestricted }

// resolve 求解最终能力档:三来源取「严格度最高者」(纯函数,不读缓存,便于测试)。
func resolve() Profile {
	mode := normalize(defaultMode)                                // 1) 编译期默认(缺省 full)
	mode = strictest(mode, fileMode())                            // 2) root 策略文件:只能加严
	mode = strictest(mode, normalize(os.Getenv("CCFLY_PROFILE"))) // 3) 环境变量:只能加严
	return forMode(mode)
}

// severity:数值越大 = 能力越少 = 越严格。各档能力是全序子集(full ⊃ host ⊃ instance ⊃ restricted),
// 故可用单一数值表达「最严格者胜 / env 只能降权」。未知 / 空 / full → 0(最宽松)。
func severity(mode string) int {
	switch normalize(mode) {
	case ModeRestricted:
		return 3
	case ModeInstance:
		return 2
	case ModeHost:
		return 1
	default:
		return 0
	}
}

// strictest 返回 a、b 中严格度更高者(平级保留 a)。
func strictest(a, b string) string {
	if severity(b) > severity(a) {
		return b
	}
	return a
}

// fileMode 读策略文件里的 mode;读不到 / 解析失败 / 无 mode → ""(视为未指定)。
func fileMode() string {
	path := strings.TrimSpace(os.Getenv("CCFLY_PROFILE_FILE"))
	if path == "" {
		path = defaultProfileFile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return ""
	}
	return normalize(doc.Mode)
}

// forMode 把模式名映射成各功能位:
//   - restricted —— 全关。
//   - instance   —— 仅 MeshJoin(允许受控接入 overlay)。
//   - 其余(full / 未知 / typo)—— 全开(保守:绝不因 ldflags typo 悄悄关功能;更严的档必须显式声明)。
func forMode(mode string) Profile {
	switch mode {
	case ModeRestricted:
		return Profile{Mode: ModeRestricted}
	case ModeInstance:
		return Profile{Mode: ModeInstance, MeshJoin: true}
	case ModeHost:
		// host-agent(ccfly-hostd):接入 overlay(MeshJoin)+ 用 expose 桥把 spawn API 暴露到
		// overlay(OverlayBridge)+ 自装常驻服务(Install);关掉用不到的 Claude/MeshProxy/UISync。
		return Profile{Mode: ModeHost, MeshJoin: true, OverlayBridge: true, Install: true}
	default:
		return Profile{
			Mode: ModeFull, MeshJoin: true, OverlayBridge: true,
			MeshProxy: true, Claude: true, Install: true, UISync: true,
		}
	}
}

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
