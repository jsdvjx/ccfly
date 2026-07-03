//go:build windows

package svc

import (
	"os/user"

	"golang.org/x/sys/windows"
)

func isRoot() bool {
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid,
	)
	if err != nil {
		return false
	}
	defer windows.FreeSid(sid)
	member, err := windows.Token(0).IsMember(sid)
	if err != nil {
		return false
	}
	return member
}

func runAs(system bool) (name, home string, uid, gid int, err error) {
	u, err := user.Current()
	if err != nil {
		return "", "", -1, -1, err
	}
	return u.Username, u.HomeDir, 0, 0, nil
}

func chownTree(root string, uid, gid int) error {
	return nil
}
