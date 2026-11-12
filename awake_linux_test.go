package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInhibitArgs(t *testing.T) {
	got := inhibitArgs("Switching Claude accounts")
	want := []string{
		"--what=sleep:idle",
		"--who=Claude Account Switcher",
		"--why=Switching Claude accounts",
		"--mode=block",
		"cat",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("inhibitArgs = %q, want %q", got, want)
	}
}

// Without systemd-inhibit there is no lock, and that is no error.
func TestHoldAwakeWithoutSystemdInhibit(t *testing.T) {
	old := inhibitLookPath
	t.Cleanup(func() { inhibitLookPath = old })
	inhibitLookPath = func(string) (string, error) { return "", errors.New("not found") }

	free, err := holdAwake("test")
	if err != nil || free == nil {
		t.Fatalf("holdAwake: free is nil (%v), err %v; want a no-op and no error", free == nil, err)
	}
	free()
}

// A stand-in for systemd-inhibit runs the command after its options, the way
// the real one does once it has the lock. The command must keep running
// while the lock is held, and freeing must end it.
func TestHoldAwakeFreeEndsTheCommand(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	fake := filepath.Join(dir, "systemd-inhibit")
	script := "#!/bin/sh\n" +
		"while [ \"${1#--}\" != \"$1\" ]; do shift; done\n" +
		"echo $$ > '" + pidFile + "'\n" +
		"exec \"$@\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := inhibitLookPath
	t.Cleanup(func() { inhibitLookPath = old })
	inhibitLookPath = func(string) (string, error) { return fake, nil }

	free, err := holdAwake("test")
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	for deadline := time.Now().Add(5 * time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		raw, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	if pid == 0 {
		free()
		t.Fatal("the stand-in never ran its command")
	}
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(pid, 0); err != nil {
		free()
		t.Fatalf("the command is not running while the lock is held: %v", err)
	}

	stopped := make(chan struct{})
	go func() { free(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("free did not return")
	}
	// free waited for the process, so its pid is gone, not a zombie.
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("the command %d still runs after free", pid)
	}
}
