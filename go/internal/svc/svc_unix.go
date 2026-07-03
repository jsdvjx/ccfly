//go:build !windows

package svc

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

func isRoot() bool {
	return os.Geteuid() == 0
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
