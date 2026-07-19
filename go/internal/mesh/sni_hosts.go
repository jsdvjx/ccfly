package mesh

// sni_hosts.go — 主机名合法性校验(全平台共用)+ hosts 托管块**剥离**逻辑。
//
// 2026-07-19 三端统一为「内嵌 CoreDNS on :53」后,ccfly 不再主动写 hosts(旧 Windows hosts 方案
// 与 macOS 旧版曾写过)。保留剥离逻辑只为卸载/重装时**清理旧版残留**(见 darwin restoreUnixHosts、
// restoreResolver 的兼容清理)。新建/追加 hosts 块的函数已随方案下线删除。

import "strings"

// 信任点 = OSS 本身：清单对象 public-read 但仅 agent 持 AK 可写，HTTPS 传输防篡改。设备信 OSS 内容，
// 不再按厂商白名单二次过滤——去掉后加任何厂商域名(OpenAI 等)都纯 OSS 热更、不发版。仅保留基本合法性
// sanity(防 OSS 万一返回乱码/通配/超长主机名，这不是信任判断)。

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
	// 完整起始标记行（旧版写入用）。剥离用前缀匹配，故标记文案微调也不影响清理旧块。
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
