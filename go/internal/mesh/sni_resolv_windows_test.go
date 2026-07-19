//go:build windows

package mesh

// sni_resolv_windows_test.go — 网卡 DNS 指向/恢复:命令序列与备份内容(外部命令全部打桩,不碰真系统)。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func withWindowsNetStubs(t *testing.T) (backupPath string, netshLog *[][]string) {
	t.Helper()
	dir := t.TempDir()
	backupPath = filepath.Join(dir, "dns-backup.json")

	oldPS, oldNetsh, oldFlush, oldProgramData := runPS, runNetsh, flushDNS, os.Getenv("ProgramData")
	log := [][]string{}
	runPS = func(script string) ([]byte, error) {
		return []byte(`[{"alias":"Ethernet0","dhcp":true,"servers":["192.168.1.1"]},{"alias":"Ethernet1","dhcp":false,"servers":["10.0.0.53","10.0.0.54"]}]`), nil
	}
	runNetsh = func(args ...string) error {
		log = append(log, append([]string(nil), args...))
		return nil
	}
	flushDNS = func() {}
	_ = os.Setenv("ProgramData", dir)
	t.Cleanup(func() {
		runPS, runNetsh, flushDNS = oldPS, oldNetsh, oldFlush
		_ = os.Setenv("ProgramData", oldProgramData)
	})
	return filepath.Join(dir, "ccfly", "dns-backup.json"), &log
}

func TestWindowsPointResolverBacksUpAndSetsDNS(t *testing.T) {
	backup, log := withWindowsNetStubs(t)

	if err := pointResolver(nil, "223.5.5.5", nil); err != nil {
		t.Fatal(err)
	}
	// 备份文件落盘且内容正确。
	b, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	var ifaces []ifaceDNS
	if err := json.Unmarshal(b, &ifaces); err != nil || len(ifaces) != 2 {
		t.Fatalf("backup content: %v %v", ifaces, err)
	}
	// 每块网卡:set static 127.0.0.1 primary + add upstream index=2。
	want := [][]string{
		{"interface", "ip", "set", "dns", "name=Ethernet0", "static", "127.0.0.1", "primary"},
		{"interface", "ip", "add", "dns", "name=Ethernet0", "223.5.5.5", "index=2"},
		{"interface", "ip", "set", "dns", "name=Ethernet1", "static", "127.0.0.1", "primary"},
		{"interface", "ip", "add", "dns", "name=Ethernet1", "223.5.5.5", "index=2"},
	}
	if !reflect.DeepEqual(*log, want) {
		t.Fatalf("netsh sequence:\ngot  %v\nwant %v", *log, want)
	}

	// 幂等:备份已存在时不覆盖(保住最初的原始配置)。
	if err := os.WriteFile(backup, []byte(`[{"alias":"Ethernet0","dhcp":true,"servers":["1.1.1.1"]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pointResolver(nil, "223.5.5.5", nil); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(backup)
	if !strings.Contains(string(b), "1.1.1.1") {
		t.Fatal("second pointResolver overwrote the original backup")
	}
}

func TestWindowsRestoreResolverRestoresPerBackup(t *testing.T) {
	backup, log := withWindowsNetStubs(t)
	if err := pointResolver(nil, "223.5.5.5", nil); err != nil {
		t.Fatal(err)
	}
	*log = nil

	if err := restoreResolver(); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"interface", "ip", "set", "dns", "name=Ethernet0", "dhcp"},
		{"interface", "ip", "set", "dns", "name=Ethernet1", "static", "10.0.0.53", "primary"},
		{"interface", "ip", "add", "dns", "name=Ethernet1", "10.0.0.54", "index=2"},
	}
	if !reflect.DeepEqual(*log, want) {
		t.Fatalf("restore sequence:\ngot  %v\nwant %v", *log, want)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatal("backup file should be removed after successful restore")
	}
	// 幂等:再次恢复 = no-op。
	if err := restoreResolver(); err != nil {
		t.Fatal(err)
	}
	if len(*log) != len(want) {
		t.Fatal("second restore should not issue netsh commands")
	}
}
