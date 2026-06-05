package control

import "testing"

// 真机 claude 2.1.x 抓屏(tmux -e)实测的输入行三态:鬼影建议 / 真实键入 / 清空。
// 鬼影建议 = ❯+U+00A0 后接 SGR7 首字 + SGR2(dim)余文;真实键入 = 常规属性、光标块在尾部。
func TestParseSuggestANSI(t *testing.T) {
	const NBSP = " "
	cases := []struct {
		name string
		line string
		want string
	}{
		{
			name: "ghost suggestion",
			line: "\x1b[39m❯" + NBSP + "\x1b[7mg\x1b[0;2mo ahead\x1b[0m",
			want: "go ahead",
		},
		{
			name: "real typed input (no suggestion)",
			line: "\x1b[39m❯" + NBSP + "real typed input\x1b[7m \x1b[0m",
			want: "",
		},
		{
			name: "empty box cursor only (no suggestion)",
			line: "\x1b[39m❯" + NBSP + "\x1b[7m \x1b[0m",
			want: "",
		},
		{
			name: "no input line at all",
			line: "\x1b[2msome dim prose\x1b[0m",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			screen := "──────────\n" + c.line + "\n──────────\n"
			got := parseSuggestANSI(screen)
			if got != c.want {
				t.Fatalf("parseSuggestANSI = %q, want %q", got, c.want)
			}
		})
	}
}
