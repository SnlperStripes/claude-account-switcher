package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
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
// which would otherwise fail on Windows while the handle is still open. The
// crash output holds its own copy of the handle, so it is reset as well.
func setupTestLog(t *testing.T, path string) {
	t.Helper()
	before := log.Writer()
	setupLog(path, false)
	t.Cleanup(func() { debug.SetCrashOutput(nil, debug.CrashOptions{}) })
	if f, ok := log.Writer().(*os.File); ok && f != before {
		t.Cleanup(func() { f.Close() })
	}
}

// TestSetupLogKeepsCrashes runs itself again in a child process that panics
// after setupLog, then checks that the panic and its stack are in the log.
func TestSetupLogKeepsCrashes(t *testing.T) {
	if path := os.Getenv("SWITCHER_CRASH_LOG"); path != "" {
		setupLog(path, false)
		panic("test crash")
	}

	path := filepath.Join(t.TempDir(), "switcher.log")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSetupLogKeepsCrashes$")
	cmd.Env = append(os.Environ(), "SWITCHER_CRASH_LOG="+path)
	if err := cmd.Run(); err == nil {
		t.Fatal("child process did not crash")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"panic: test crash", "goroutine ", "TestSetupLogKeepsCrashes"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("log does not contain %q:\n%s", want, got)
		}
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

	data, err := statusJSON(snap, time.Date(2026, 11, 14, 21, 0, 0, 0, time.UTC))
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
	data, err := statusJSON(snapshot{}, time.Now())
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

func TestStatusJSONZeroesAWindowWhoseResetHasPassed(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	snap := snapshot{
		Active: "u",
		Accounts: []Account{{
			UUID: "u", Name: "Ada", Email: "ada@example.com", Plan: "Max",
			Usage: &Usage{
				Session: 97, SessionReset: now.Add(-time.Hour),
				Weekly: 40, WeeklyReset: now.Add(48 * time.Hour),
			},
		}},
	}

	data, err := statusJSON(snap, now)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Accounts []struct {
			Usage *struct {
				Session float64 `json:"session"`
				Weekly  float64 `json:"weekly"`
			} `json:"usage"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, data)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].Usage == nil {
		t.Fatalf("missing usage in %s", data)
	}
	if got.Accounts[0].Usage.Session != 0 {
		t.Errorf("session = %v, want 0: the reset time has passed", got.Accounts[0].Usage.Session)
	}
	if got.Accounts[0].Usage.Weekly != 40 {
		t.Errorf("weekly = %v, want 40: its reset is still in the future", got.Accounts[0].Usage.Weekly)
	}
	if snap.Accounts[0].Usage.Session != 97 {
		t.Errorf("the snapshot was modified: session = %v, want 97 untouched", snap.Accounts[0].Usage.Session)
	}
}

// An account that has not used its 5-hour window has no reset time. The JSON
// leaves it out instead of printing the year 1 for scripts to trip over.
func TestStatusJSONLeavesOutUnsetResetTimes(t *testing.T) {
	snap := snapshot{Accounts: []Account{{
		UUID: "u", Name: "Ada", Plan: "Pro",
		Usage: &Usage{
			Weekly: 6, WeeklyReset: time.Date(2026, 11, 21, 7, 0, 0, 0, time.UTC),
			CheckedAt: time.Date(2026, 11, 15, 11, 59, 0, 0, time.UTC),
		},
	}}}

	data, err := statusJSON(snap, time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "0001-01-01") {
		t.Errorf("an unset reset time is printed as the year 1:\n%s", data)
	}
	for _, key := range []string{`"sessionReset"`, `"modelReset"`} {
		if strings.Contains(string(data), key) {
			t.Errorf("%s is present although it was never set:\n%s", key, data)
		}
	}
	if !strings.Contains(string(data), `"weeklyReset": "2026-11-21T07:00:00Z"`) {
		t.Errorf("the weekly reset time is missing:\n%s", data)
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
