package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadCmdResult 用内联 jsonl 夹具验证「信息类斜杠命令结果」从 jsonl 读 isMeta markdown:
//   - 跳过真用户消息(无 isMeta)、跳过 isMeta 但 content 为数组/空的行、跳过坏行;
//   - 返回首条 type:user+isMeta:true 且 content 为非空字符串的 markdown 及其行末游标;
//   - since 越界/为负当 0;再用返回游标续扫应 found:false。
func TestReadCmdResult(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"local_command","content":"<command-name>/context</command-name>"}`,
		`{"type":"user","isMeta":false,"message":{"content":"this is a real user message"}}`,                 // 真用户:无 isMeta → 跳过
		`{"type":"user","isMeta":true,"message":{"content":[{"type":"text","text":"array form"}]}}`,           // isMeta 但数组型 → 跳过
		`{not valid json`,                                                                                     // 坏行 → 跳过
		`{"type":"user","isMeta":true,"message":{"content":"## Context Usage\n\n| Category | Tokens |\n|--|--|"}}`, // 命中
		`{"type":"user","isMeta":true,"message":{"content":"## second one (should not be returned)"}}`,
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// since<0 当 0;命中首条 markdown。
	md, cursor, found := readCmdResult(path, -5)
	if !found {
		t.Fatalf("found=false,应命中首条 isMeta markdown")
	}
	if !strings.HasPrefix(md, "## Context Usage") || !strings.Contains(md, "| Category | Tokens |") {
		t.Fatalf("markdown 不对: %q", md)
	}

	// 用返回游标续扫:跳过第二条(我们只要首条)? 实际续扫会拿到第二条,
	// 这里验证「从命中行之后再扫」能拿到下一条(证明 cursor 指向行末、可续)。
	md2, _, found2 := readCmdResult(path, cursor)
	if !found2 || !strings.Contains(md2, "second one") {
		t.Fatalf("从 cursor 续扫应拿到第二条 isMeta,得 found=%v md=%q", found2, md2)
	}

	// since=EOF → found:false,cursor 不倒退。
	fi, _ := os.Stat(path)
	_, cur3, found3 := readCmdResult(path, fi.Size())
	if found3 {
		t.Fatalf("since=EOF 不应再命中")
	}
	if cur3 != fi.Size() {
		t.Fatalf("cursor 应等于 EOF=%d,得 %d", fi.Size(), cur3)
	}
}

// TestReadCmdResultRealScratch 对本地任意 claude 会话(若存在)跑 since=0:若该会话含
// isMeta markdown(如 resume 的 local-command-caveat、或 /context 产出),则断言能取到;
// 否则跳过。不写死任何机器路径 / session id(见 realfixture_test.go 的发现助手)。
func TestReadCmdResultRealScratch(t *testing.T) {
	projectDir, sid := discoverRealSession()
	if projectDir == "" {
		t.Skip("requires a local claude session")
	}
	path := filepath.Join(projectDir, sid+".jsonl")
	// since=0 取首条 isMeta string(可能是 resume 的 local-command-caveat 或命令 markdown)。
	md, cursor, found := readCmdResult(path, 0)
	if !found {
		t.Skip("local claude session has no isMeta markdown line")
	}
	if strings.TrimSpace(md) == "" {
		t.Fatalf("取到的 markdown 为空")
	}
	t.Logf("real session first isMeta head: %q", md[:min(80, len(md))])

	// 续扫:整个文件里至少应有一条命令产出的 markdown(实测 /context 写 ## Context Usage)。
	got := false
	for cursor > 0 {
		var m string
		var c int64
		var f bool
		m, c, f = readCmdResult(path, cursor)
		if !f {
			break
		}
		cursor = c
		if strings.Contains(m, "## Context Usage") {
			got = true
			break
		}
	}
	if !got {
		t.Logf("注意:scratch 文件未含 ## Context Usage(可能该会话未跑过 /context)")
	}
}
