package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestStatusJSONShape(t *testing.T) {
	sessionReset := time.Date(2026, 11, 14, 22, 30, 0, 0, time.UTC)
	weeklyReset := time.Date(2026, 11, 20, 9, 15, 0, 0, time.UTC)
	snap := snapshot{
		Active: "uuid-a",
		Accounts: []Account{
			{
				UUID: "uuid-a", Name: "Ada", Email: "ada@example.com", Plan: "Max",
				Usage: &Usage{Session: 42, SessionReset: sessionReset, Weekly: 12, WeeklyReset: weeklyReset,
					CheckedAt: time.Date(2026, 11, 14, 21, 0, 0, 0, time.UTC)},
			},
			{UUID: "uuid-b", Name: "Bob", Email: "bob@example.com", Plan: "Pro", Problem: signedOut},
		},
	}

	data, err := statusJSON(snap)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("not valid JSON: %s", data)
	}

	var got struct {
		Active   string `json:"active"`
		Accounts []struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			Plan  string `json:"plan"`
			Usage *struct {
				Session      float64 `json:"session"`
				SessionReset string  `json:"sessionReset"`
				Weekly       float64 `json:"weekly"`
				WeeklyReset  string  `json:"weeklyReset"`
			} `json:"usage"`
			Problem string `json:"problem"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, data)
	}
	if got.Active != snap.Active {
		t.Errorf("active = %q, want %q", got.Active, snap.Active)
	}
	if len(got.Accounts) != 2 {
		t.Fatalf("got %d accounts, want 2\n%s", len(got.Accounts), data)
	}

	a := got.Accounts[0]
	if a.Name != "Ada" || a.Email != "ada@example.com" || a.Plan != "Max" {
		t.Errorf("first account = %+v, want Ada, ada@example.com, Max", a)
	}
	if a.Usage == nil {
		t.Fatalf("first account has no usage\n%s", data)
	}
	if a.Usage.Session != 42 || a.Usage.Weekly != 12 {
		t.Errorf("usage = %v / %v, want 42 / 12", a.Usage.Session, a.Usage.Weekly)
	}
	for _, tc := range []struct {
		name string
		got  string
		want time.Time
	}{
		{"session reset", a.Usage.SessionReset, sessionReset},
		{"weekly reset", a.Usage.WeeklyReset, weeklyReset},
	} {
		at, err := time.Parse(time.RFC3339, tc.got)
		if err != nil {
			t.Errorf("%s %q is not RFC 3339: %v", tc.name, tc.got, err)
			continue
		}
		if !at.Equal(tc.want) {
			t.Errorf("%s = %s, want %s", tc.name, at, tc.want)
		}
	}

	b := got.Accounts[1]
	if b.Problem != signedOut || b.Usage != nil {
		t.Errorf("second account = %+v, want problem %q and no usage", b, signedOut)
	}
	if strings.Contains(strings.ToLower(string(data)), "token") {
		t.Errorf("output mentions tokens: %s", data)
	}
}

func TestStatusJSONEmptyAccounts(t *testing.T) {
	data, err := statusJSON(snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Accounts []Account `json:"accounts"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Accounts == nil {
		t.Errorf("accounts is null, want an empty list: %s", data)
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
