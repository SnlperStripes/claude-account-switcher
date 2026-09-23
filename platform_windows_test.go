package main

import (
	"os"
	"testing"
)

func TestStoreAppID(t *testing.T) {
	pids, exe := desktopProcesses()
	if len(pids) == 0 {
		t.Skip("Claude is not running")
	}
	id := storeAppID(exe)
	t.Logf("exe %s -> %q", exe, id)
	if _, err := os.Stat(exe); err != nil {
		t.Fatal(err)
	}
}
