//go:build windows

package main

import "github.com/jsdvjx/ccfly/go/internal/control"

func runTermBridge(args []string) error { return control.RunTermBridge(args) }
