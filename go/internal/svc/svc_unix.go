//go:build !windows

package svc

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

func isRoot() bool {
	return os.Geteuid() == 0
}

// hasSystemd reports whether this Linux host actually runs systemd as its init.
// Bare containers and minimal images frequently ship without it (no systemctl in
// PATH, or systemctl present but no live manager) — which is why `ccfly install`
// must not hard-fail there, but fall back to a detached background agent.
func hasSystemd() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	// /run/systemd/system exists iff systemd booted this machine (sd_booted(3)).
	if st, err := os.Stat("/run/systemd/system"); err != nil || !st.IsDir() {
		return false
	}
	return true
}

// startDetachedAgent launches `bin args...` in its own session (setsid) so it
// outlives the installer process — the no-systemd fallback that keeps the device
// online for the current session. Output is appended to logPath.
func startDetachedAgent(bin string, args, env []string, logPath string) error {
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()
	cmd := exec.Command(bin, args...)
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func runAs(system bool) (name, home string, uid, gid int, err error) {
	var u *user.User
	if system {
		if n := os.Getenv("SUDO_USER"); n != "" && n != "root" {
			u, err = user.Lookup(n)
		} else {
			u, err = user.Current()
		}
	} else {
		u, err = user.Current()
	}
	if err != nil {
		return "", "", -1, -1, err
	}
	uid, _ = strconv.Atoi(u.Uid)
	gid, _ = strconv.Atoi(u.Gid)
	return u.Username, u.HomeDir, uid, gid, nil
}

func chownTree(root string, uid, gid int) error {
	return filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		_ = os.Chown(p, uid, gid)
		return nil
	})
}
