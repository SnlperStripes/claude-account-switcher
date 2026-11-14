package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// logRotationLimit mirrors the condition in setupLog (main.go:79): an
// existing log is started over once it grows past 1 << 20 bytes.
const logRotationLimit = 1 << 20

func preserveLogger(t *testing.T) {
	t.Helper()
	writer, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	t.Cleanup(func() {
		log.SetOutput(writer)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
	})
}

// setupTestLog calls setupLog and closes the log file before TempDir cleanup,
// which would otherwise fail on Windows while the handle is still open.
func setupTestLog(t *testing.T, path string) {
	t.Helper()
	before := log.Writer()
	setupLog(path, false)
	if f, ok := log.Writer().(*os.File); ok && f != before {
		t.Cleanup(func() { f.Close() })
	}
}

func TestSetupLogStartsOverAfterLimit(t *testing.T) {
	preserveLogger(t)

	path := filepath.Join(t.TempDir(), "switcher.log")
	old := bytes.Repeat([]byte("old log line\n"), logRotationLimit/len("old log line\n")+1)
	if len(old) <= logRotationLimit {
		t.Fatalf("test log is %d bytes, want more than %d", len(old), logRotationLimit)
	}
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}

	setupTestLog(t, path)
	log.Print("entry after rotation")

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("old log line")) {
		t.Errorf("log still contains the old entries after rotation (size %d)", len(got))
	}
	if !strings.Contains(string(got), "entry after rotation") {
		t.Errorf("log does not contain the entry written after rotation: %q", got)
	}
}

func TestSetupLogKeepsFileBelowLimit(t *testing.T) {
	preserveLogger(t)

	path := filepath.Join(t.TempDir(), "switcher.log")
	kept := bytes.Repeat([]byte("k"), logRotationLimit-1)
	if err := os.WriteFile(path, kept, 0o600); err != nil {
		t.Fatal(err)
	}

	setupTestLog(t, path)
	log.Print("entry appended below limit")

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, kept) {
		t.Errorf("log below the limit was truncated or rewritten (size %d)", len(got))
	}
	if !strings.Contains(string(got), "entry appended below limit") {
		t.Errorf("log does not contain the entry written below the limit: %q", got[len(kept):])
	}
}
