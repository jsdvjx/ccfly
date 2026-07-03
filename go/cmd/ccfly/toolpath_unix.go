//go:build !windows

package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

func ensureToolPath() {
	const fallbackExtras = "/opt/homebrew/bin:/usr/local/bin"
	current := os.Getenv("PATH")
	if current == "" {
		current = "/usr/bin:/bin:/usr/sbin:/sbin"
	}

	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, sh, "-ilc", "echo $PATH")
	cmd.Stderr = nil
	out, err := cmd.Output()

	if err == nil {
		loginPath := strings.TrimSpace(string(out))
		if strings.Contains(loginPath, "/") {
			os.Setenv("PATH", loginPath)
			log.Printf("ccfly: PATH resolved from login shell (%d entries)", strings.Count(loginPath, ":")+1)
			return
		}
	}
	os.Setenv("PATH", fallbackExtras+":"+current)
	log.Printf("ccfly: PATH fallback (login shell probe failed), prepended %s", fallbackExtras)
}
