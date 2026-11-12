package main

import (
	"log"
	"os/exec"
	"sync/atomic"
	"time"
)

// inhibitLookPath finds systemd-inhibit. Tests replace it.
var inhibitLookPath = exec.LookPath

// holdAwake keeps the computer from sleeping until the returned func is
// called. It takes a logind sleep and idle lock through systemd-inhibit, the
// kind of lock Electron's power save blocker takes for Claude's keep awake.
// Without systemd-inhibit, as on a system without systemd, it does nothing.
//
// systemd-inhibit holds the lock while its command runs. The command is cat
// reading a pipe from this process: closing the pipe ends it, and so does
// the switcher dying first, because the kernel then closes the pipe.
func holdAwake(reason string) (func(), error) {
	bin, err := inhibitLookPath("systemd-inhibit")
	if err != nil {
		return func() {}, nil
	}
	cmd := exec.Command(bin, inhibitArgs(reason)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var freed atomic.Bool
	done := make(chan struct{})
	go func() {
		// An early end means there is no lock, for example when logind
		// refused it. The switch goes on without one.
		if err := cmd.Wait(); err != nil && !freed.Load() {
			log.Printf("systemd-inhibit ended early: %v", err)
		}
		close(done)
	}()
	return func() {
		freed.Store(true)
		stdin.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}, nil
}

// inhibitArgs are the systemd-inhibit arguments for a lock that lasts until
// its standard input closes.
func inhibitArgs(reason string) []string {
	return []string{
		"--what=sleep:idle",
		"--who=Claude Account Switcher",
		"--why=" + reason,
		"--mode=block",
		"cat",
	}
}
