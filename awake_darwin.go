package main

import (
	"os"
	"os/exec"
	"strconv"
)

// holdAwake keeps the Mac from idle sleep until the returned func is called.
// caffeinate also ends by itself if the switcher dies first.
func holdAwake(string) (func(), error) {
	cmd := exec.Command("caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}, nil
}
