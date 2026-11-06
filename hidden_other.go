//go:build !windows

package main

import "os/exec"

// hidden keeps a command's window hidden. Only Windows has console windows to hide.
func hidden(cmd *exec.Cmd) *exec.Cmd { return cmd }
