package main

// sessions_cli_test.go — ccfly ls 分组/排序的回归。

import (
	"testing"

	"github.com/jsdvjx/ccfly/go/internal/control"
)

func TestGroupByDir(t *testing.T) {
	rows := []control.CLISessionRow{
		{Sid: "a1", Cwd: "/p1", Age: 300},
		{Sid: "b1", Cwd: "/p2", Age: 60},
		{Sid: "a2", Cwd: "/p1", Age: 10},
		{Sid: "b2", Cwd: "/p2", Age: 60, Live: true}, // 同龄:live 优先
		{Sid: "a3", Cwd: "/p1", Age: 7200},
	}
	groups := groupByDir(rows)
	if len(groups) != 2 {
		t.Fatalf("应 2 组,得 %d", len(groups))
	}
	// 组间:p1 的最新(10s)比 p2 的最新(60s)更近 → p1 在前。
	if groups[0].Cwd != "/p1" || groups[1].Cwd != "/p2" {
		t.Fatalf("组序应 [/p1 /p2],得 [%s %s]", groups[0].Cwd, groups[1].Cwd)
	}
	// 组内:时间倒序(Age 升序)。
	got := []string{groups[0].Rows[0].Sid, groups[0].Rows[1].Sid, groups[0].Rows[2].Sid}
	if got[0] != "a2" || got[1] != "a1" || got[2] != "a3" {
		t.Fatalf("/p1 组内应 [a2 a1 a3],得 %v", got)
	}
	// 同龄并列:live 优先。
	if groups[1].Rows[0].Sid != "b2" {
		t.Fatalf("/p2 同龄应 live 优先(b2),得 %s", groups[1].Rows[0].Sid)
	}
}
