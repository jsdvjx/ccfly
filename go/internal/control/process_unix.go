//go:build !windows

package control

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func processSignalTerm(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

func processSignalKill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

func processListByCommand(sid string) []int {
	b, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	var out []int
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, sid) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 1 {
			continue
		}
		if filepath.Base(fields[1]) != "claude" {
			continue
		}
		out = append(out, pid)
	}
	return out
}

func processLstart(pid int) (string, error) {
	b, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
