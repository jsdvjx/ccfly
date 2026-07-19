package mesh

// sni_hosts.go — Windows SNI arm 的 hosts 文件劫持（跨平台纯逻辑，便于单测）。
//
// 为什么 Windows 用 hosts 而非 NRPT+本地:53 DNS：DNS 型代理客户端（Clash/sing-box 的 TUN+fake-ip）
// 会占用 :53 并劫持 AI 域，导致本地 :53 DNS 起不来、SNI arm 整体回滚（实测）。hosts 文件解析优先级
// 高于任何 DNS（含 fake-ip），且映射到 127.0.0.1/::1 是 loopback、不被 TUN 抓 → 与 Clash 共存。
//
// 代价：hosts 无通配，必须逐个精确主机名（下方 sniPinnedHosts 静态维护）。这些是「必须从账号独立 IP
// 出网」的 Anthropic 控制面主机；下载/遥测/第三方不在此列（IP 无所谓，走用户默认出网）。
//
// 局部替换：只管理带标记的 ccfly 块，绝不动用户已有 hosts 条目。arm 时写块、teardown 时删块，幂等。

import "strings"

// sniPinnedHosts 是要钉到 loopback 的精确主机名（静态维护；Anthropic 新增控制面主机时在此补）。
// 仅列「账号级 IP 风控相关」的域：API + 登录/认证。遥测(sentry/datadog/honeycomb)与下载(npm/google/
// github)刻意不列——它们不该占用独立 IP，遥测更应经会话环境 DISABLE_* 关掉。
var sniPinnedHosts = []string{
	"api.anthropic.com",     // Claude API：账号流量本体，必须从独立 IP 出
	"console.anthropic.com", // 旧 Console/认证
	"claude.ai",             // claude.ai 订阅号 OAuth 登录：登录 IP 需与独立 IP 一致
	"platform.claude.com",   // 新 Console 认证域
	"statsig.anthropic.com", // 功能开关（沿用既有 statsig 拦截，Anthropic 自有子域）
}

// 信任点 = OSS 本身：清单对象 public-read 但仅 agent 持 AK 可写，HTTPS 传输防篡改。设备信 OSS 内容，
// 不再按厂商白名单二次过滤——去掉后加任何厂商域名(OpenAI 等)都纯 OSS 热更、不发版。仅保留基本合法性
// sanity(防 OSS 万一返回乱码/通配/超长主机名，这不是信任判断)。sniPinnedHosts 是 OSS 读不到时的编译期兜底。

// isValidSNIHost 只校验是不是一个合法 DNS 主机名(小写字母数字/点/连字符、含点、长度合理)，不判厂商。
func isValidSNIHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
	if len(h) < 3 || len(h) > 253 || !strings.Contains(h, ".") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				return false
			}
		}
		if label == "-" {
			return false
		}
	}
	return true
}

// filterAllowedHosts 归一化 + 去重 + 丢掉不合法主机名(sanity，非厂商过滤)。
func filterAllowedHosts(hosts []string) []string {
	out := make([]string, 0, len(hosts))
	seen := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
		if isValidSNIHost(h) && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

const (
	// 完整起始标记行（写入用）。剥离用前缀匹配，故标记文案微调也不影响清理旧块。
	hostsBeginLine   = "# BEGIN ccfly-sni (managed by ccfly — 局部块，勿手改；卸载 ccfly 可删本块)"
	hostsBeginPrefix = "# BEGIN ccfly-sni"
	hostsEndPrefix   = "# END ccfly-sni"
)

// stripCcflyHostsBlock 移除任意已存在的 ccfly 托管块（起止标记之间，含标记），保留其余全部用户条目。
// 幂等：无块则原样返回。按前缀匹配标记行，容忍 CRLF/LF 与前后空白。
func stripCcflyHostsBlock(content string) string {
	if !strings.Contains(content, hostsBeginPrefix) {
		return content
	}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	skip := false
	for _, ln := range lines {
		t := strings.TrimSpace(strings.TrimRight(ln, "\r"))
		if strings.HasPrefix(t, hostsBeginPrefix) {
			skip = true
			continue
		}
		if skip {
			if strings.HasPrefix(t, hostsEndPrefix) {
				skip = false
			}
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// buildCcflyHostsBlock 渲染 hosts 托管块（CRLF 行尾，Windows hosts）。见 buildCcflyHostsBlockEOL。
func buildCcflyHostsBlock(hosts []string) string { return buildCcflyHostsBlockEOL(hosts, "\r\n") }

// buildCcflyHostsBlockEOL 渲染 hosts 托管块（每个主机名同时钉 v4 127.0.0.1 与 v6 ::1；
// 必须钉 AAAA=::1，否则 app 的 IPv6 查询会落到真 Anthropic IPv6、绕过 SNI）。行尾由 eol 指定：
// Windows hosts 用 CRLF、Unix（/etc/hosts）用 LF。
func buildCcflyHostsBlockEOL(hosts []string, eol string) string {
	var b strings.Builder
	b.WriteString(hostsBeginLine + eol)
	for _, h := range hosts {
		if h = strings.TrimSpace(h); h == "" {
			continue
		}
		b.WriteString("127.0.0.1 " + h + eol)
		b.WriteString("::1 " + h + eol)
	}
	b.WriteString(hostsEndPrefix + eol)
	return b.String()
}

// applyCcflyHostsBlock 计算「写入后」的 hosts 全文（CRLF 行尾）。见 applyCcflyHostsBlockEOL。
func applyCcflyHostsBlock(existing string, hosts []string) string {
	return applyCcflyHostsBlockEOL(existing, hosts, "\r\n")
}

// applyCcflyHostsBlockEOL 计算「写入后」的 hosts 全文：先剥离旧 ccfly 块，再在末尾追加新块。
// 保留用户原有条目原样。existing 为空 → 只有块本身。eol 指定块与拼接处的行尾。
func applyCcflyHostsBlockEOL(existing string, hosts []string, eol string) string {
	cleaned := strings.TrimRight(stripCcflyHostsBlock(existing), "\r\n")
	block := buildCcflyHostsBlockEOL(hosts, eol)
	if cleaned == "" {
		return block
	}
	return cleaned + eol + block
}
