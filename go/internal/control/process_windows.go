//go:build windows

package control

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Windows, FindProcess always succeeds. Use Signal(nil) to probe.
	// Fallback: try to open the process handle.
	err = p.Signal(os.Signal(nil))
	// If Signal returns an error about "process already finished", it's dead.
	if err != nil {
		return false
	}
	return true
}

func processSignalTerm(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(os.Interrupt)
}

func processSignalKill(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func processListByCommand(sid string) []int {
	// Use WMIC on Windows to list processes with command lines
	b, err := exec.Command("wmic", "process", "get", "ProcessId,CommandLine", "/format:csv").Output()
	if err != nil {
		// Fallback to tasklist
		return nil
	}
	var out []int
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, sid) {
			continue
		}
		// CSV format: Node,CommandLine,ProcessId
		fields := strings.Split(line, ",")
		if len(fields) < 3 {
			continue
		}
		pidStr := strings.TrimSpace(fields[len(fields)-1])
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid <= 1 {
			continue
		}
		cmdLine := strings.Join(fields[1:len(fields)-1], ",")
		if !strings.Contains(filepath.Base(cmdLine), "claude") {
			continue
		}
		out = append(out, pid)
	}
	return out
}

func processLstart(pid int) (string, error) {
	// On Windows, WMIC can get process creation date
	b, err := exec.Command("wmic", "process", "where", fmt.Sprintf("ProcessId=%d", pid), "get", "CreationDate", "/value").Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "CreationDate=") {
			return strings.TrimPrefix(line, "CreationDate="), nil
		}
	}
	return "", fmt.Errorf("creation date not found for pid %d", pid)
}
