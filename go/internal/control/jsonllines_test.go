package control

import (
	"strings"
	"testing"
)

// TestJSONLLinesNoTruncation:超大行(>旧 16/64MB 上限)不再吞掉其后所有行 —— A3 的核心回归。
// 旧 bufio.Scanner 在超长行处 Scan() 返回 false、循环结束,该行之后的内容全部不可见。
func TestJSONLLinesNoTruncation(t *testing.T) {
	huge := strings.Repeat("x", 20*1024*1024) // 20MB,超过旧 16MB 上限
	input := `{"n":1}` + "\n" +
		`{"big":"` + huge + `"}` + "\n" +
		`{"n":3}` + "\n" // 这一行在旧实现里会被「吞掉」
	var got []string
	for line := range jsonlLines(strings.NewReader(input)) {
		got = append(got, string(line[:min(len(line), 12)]))
	}
	if len(got) != 3 {
		t.Fatalf("应产出 3 行(超大行不截断、其后行可见),得 %d: %v", len(got), got)
	}
	if got[0] != `{"n":1}` || got[2] != `{"n":3}` {
		t.Fatalf("首/末行应完整:%q … %q", got[0], got[2])
	}
}

// TestJSONLLinesEdges:空行跳过、\r\n 剥除、末尾无换行的半截行也产出、早停。
func TestJSONLLinesEdges(t *testing.T) {
	input := "a\r\n\n\nb\nc" // a(带\r) / 两空行 / b / c(无尾换行)
	var got []string
	for line := range jsonlLines(strings.NewReader(input)) {
		got = append(got, string(line))
	}
	if strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("应为 a,b,c(空行跳过、\\r 剥除、末尾半截行产出),得 %v", got)
	}
	// 早停:yield 返回 false 即结束。
	n := 0
	for range jsonlLines(strings.NewReader("1\n2\n3\n")) {
		n++
		if n == 2 {
			break
		}
	}
	if n != 2 {
		t.Fatalf("break 应在第 2 行停下,得 %d", n)
	}
}
