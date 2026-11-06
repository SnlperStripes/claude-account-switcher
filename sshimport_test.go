package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// sshTestWrite creates path and its parent folders with content.
func sshTestWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// sshTestStub replaces runSSH for the duration of the test.
func sshTestStub(t *testing.T, fn func(ctx context.Context, host, stdin string) ([]byte, error)) {
	t.Helper()
	old := runSSH
	runSSH = fn
	t.Cleanup(func() { runSSH = old })
}

func TestSSHHostsFromConfigAndEntries(t *testing.T) {
	userData := t.TempDir()
	root := t.TempDir()
	sshTestWrite(t, filepath.Join(userData, "ssh_configs.json"), `{"configs":[
		{"name":"a","sshHost":"alice@a","id":"1","source":"x"},
		{"name":"empty","sshHost":"","id":"2","source":"x"},
		{"name":"opt","sshHost":"-oProxyCommand=evil","id":"3","source":"x"},
		{"name":"dup","sshHost":"alice@a","id":"4","source":"x"}],"trustedHosts":["zed"]}`)
	sshTestWrite(t, filepath.Join(root, "a", "oa", "local_1.json"), `{"cliSessionId":"s1","sshConfig":{"sshHost":"bob@b"}}`)
	sshTestWrite(t, filepath.Join(root, "b", "ob", "local_2.json"), `{"cliSessionId":"s2","sshConfig":{"sshHost":"carol@c"}}`)
	sshTestWrite(t, filepath.Join(root, "b", "ob", "local_3.json"), `{"cliSessionId":"s3","sshConfig":{"sshHost":"bob@b"}}`)
	sshTestWrite(t, filepath.Join(root, "b", "ob", "local_4.json"), `{"cliSessionId":"s4","sshConfig":{"sshHost":"-x"}}`)
	sshTestWrite(t, filepath.Join(root, "b", "ob", "local_5.json"), `{"cliSessionId":"s5"}`)
	sshTestWrite(t, filepath.Join(root, "b", "ob", "local_6.json"), `not json`)

	got := sshHosts(userData, root)
	want := []string{"alice@a", "bob@b", "carol@c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sshHosts = %v, want %v", got, want)
	}

	if got := sshHosts(t.TempDir(), t.TempDir()); len(got) != 0 {
		t.Fatalf("sshHosts with no data = %v, want none", got)
	}
}

func TestKnownSSHSessionsAcrossAccounts(t *testing.T) {
	root := t.TempDir()
	sshTestWrite(t, filepath.Join(root, "a", "oa", "local_x.json"), `{"cliSessionId":"sess-1","lastActivityAt":1}`)
	sshTestWrite(t, filepath.Join(root, "b", "ob", "local_y.json"), `{"cliSessionId":"sess-2","lastActivityAt":2}`)
	sshTestWrite(t, filepath.Join(root, "b", "ob", "local_z.json"), `{"lastActivityAt":3}`)
	// The marker sits in another account than the entry it names.
	sshTestWrite(t, filepath.Join(root, "b", "ob", "deleted_x"), ``)
	// A marker whose local ID is the session ID itself.
	sshTestWrite(t, filepath.Join(root, "a", "oa", "deleted_sess-3"), ``)

	known, deleted := knownSSHSessions(root)
	if want := map[string]bool{"sess-1": true, "sess-2": true}; !reflect.DeepEqual(known, want) {
		t.Fatalf("known = %v, want %v", known, want)
	}
	if want := map[string]bool{"sess-1": true, "x": true, "sess-3": true}; !reflect.DeepEqual(deleted, want) {
		t.Fatalf("deleted = %v, want %v", deleted, want)
	}
}

func TestFetchSSHChats(t *testing.T) {
	userData := t.TempDir()
	root := t.TempDir()
	sshTestWrite(t, filepath.Join(userData, "ssh_configs.json"), `{"configs":[
		{"sshHost":"a@h1"},{"sshHost":"b@h2"},{"sshHost":"c@h3"}]}`)
	sshTestWrite(t, filepath.Join(root, "x", "ox", "local_known.json"), `{"cliSessionId":"known-1"}`)
	sshTestWrite(t, filepath.Join(root, "x", "ox", "local_gone.json"), `{"cliSessionId":"gone-cli"}`)
	sshTestWrite(t, filepath.Join(root, "x", "ox", "deleted_gone"), ``)

	line := func(c sshChat) string {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + "\n"
	}
	desktop := func(session, path string) sshChat {
		return sshChat{Session: session, Path: path, Entrypoint: "claude-desktop", Cwd: "/w"}
	}
	outputs := map[string]string{
		"a@h1": line(desktop("new-1", "/p/new-1.jsonl")) +
			line(sshChat{Session: "cli-1", Entrypoint: "cli"}) +
			line(desktop("known-1", "/p/known-1.jsonl")) +
			line(desktop("gone-cli", "/p/gone.jsonl")) +
			line(desktop("dup", "/p/dup-a.jsonl")) +
			"\n",
		"c@h3": line(desktop("dup", "/p/dup-c.jsonl")) +
			line(desktop("new-3", "/p/new-3.jsonl")),
	}

	var mu sync.Mutex
	var stdins []string
	sshTestStub(t, func(ctx context.Context, host, stdin string) ([]byte, error) {
		mu.Lock()
		stdins = append(stdins, stdin)
		mu.Unlock()
		if host == "b@h2" {
			return nil, errors.New("connection refused")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("ssh call has no timeout")
		}
		return []byte(outputs[host]), nil
	})

	got := fetchSSHChats(context.Background(), userData, root)
	want := []sshChat{
		{Host: "a@h1", Path: "/p/dup-a.jsonl", Session: "dup", Entrypoint: "claude-desktop", Cwd: "/w"},
		{Host: "a@h1", Path: "/p/new-1.jsonl", Session: "new-1", Entrypoint: "claude-desktop", Cwd: "/w"},
		{Host: "c@h3", Path: "/p/new-3.jsonl", Session: "new-3", Entrypoint: "claude-desktop", Cwd: "/w"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fetchSSHChats =\n%+v\nwant\n%+v", got, want)
	}

	if len(stdins) != 3 {
		t.Fatalf("ssh called %d times, want 3", len(stdins))
	}
	prefix := `KNOWN = set(["gone-cli","known-1"])` + "\n"
	for _, s := range stdins {
		if !strings.HasPrefix(s, prefix) || !strings.Contains(s, "import glob, json, os") {
			t.Fatalf("stdin does not start with the known list and script: %q", s[:min(len(s), 80)])
		}
	}
}

func TestWriteSSHChats(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	// Known through another account, so it must not be written.
	sshTestWrite(t, filepath.Join(root, "other", "oo", "local_k.json"), `{"cliSessionId":"known-1"}`)
	// Already has an entry, so it must be left as it is.
	existing := filepath.Join(root, "acc", "org", "local_cccccccccc-3.json")
	sshTestWrite(t, existing, "old")

	chats := []sshChat{
		{Host: "u@h", Path: "/r/a.jsonl", Session: "aaaaaaaaaa-1", First: "2026-11-01T10:00:00Z",
			Last: "2026-11-06T11:00:00Z", Cwd: "/home/u/proj", Branch: "main", Title: "My title",
			AITitle: "ai", Model: "m1"},
		{Host: "u@h", Path: "/r/b.jsonl", Session: "bbbbbbbbbb-2", First: "2026-10-01T00:00:00Z",
			Cwd: "/home/u/repo/.claude/worktrees/feat-x", AITitle: "Ai title"},
		{Host: "u@h", Path: "/r/c.jsonl", Session: "cccccccccc-3", First: "2026-11-05T00:00:00.123456789+02:00",
			Last: "not a time", Cwd: "/x"},
		{Host: "u@h", Path: "/r/d.jsonl", Session: "dddddddddd-4", First: "2026-11-05T00:00:00Z"},
		{Host: "u@h", Path: "/r/e.jsonl", Session: "eeeeeeeeee-5", First: "nope", Cwd: "/x"},
		{Host: "u@h", Path: "/r/k.jsonl", Session: "known-1", First: "2026-11-05T00:00:00Z", Cwd: "/x"},
	}

	n, err := writeSSHChats(root, "acc", "org", chats, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("written = %d, want 2", n)
	}
	if read := sshTestRead(t, existing); read != "old" {
		t.Fatalf("existing entry was overwritten: %q", read)
	}
	for _, skipped := range []string{"local_dddddddddd-4.json", "local_eeeeeeeeee-5.json", "local_known-1.json"} {
		if _, err := os.Stat(filepath.Join(root, "acc", "org", skipped)); !os.IsNotExist(err) {
			t.Fatalf("%s was written or stat failed: %v", skipped, err)
		}
	}

	a := sshTestRecord(t, filepath.Join(root, "acc", "org", "local_aaaaaaaaaa-1.json"))
	wantA := map[string]any{
		"sessionId":               "local_aaaaaaaaaa-1",
		"cliSessionId":            "aaaaaaaaaa-1",
		"cwd":                     "/home/u/proj",
		"originCwd":               "/home/u/proj",
		"createdAt":               float64(time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC).UnixMilli()),
		"lastActivityAt":          float64(time.Date(2026, 11, 6, 11, 0, 0, 0, time.UTC).UnixMilli()),
		"title":                   "My title",
		"titleSource":             "user",
		"isArchived":              false,
		"sshConfig":               map[string]any{"sshHost": "u@h"},
		"sshRemoteTranscriptPath": "/r/a.jsonl",
		"branch":                  "main",
		"model":                   "m1",
	}
	if !reflect.DeepEqual(a, wantA) {
		t.Fatalf("record A =\n%v\nwant\n%v", a, wantA)
	}

	b := sshTestRecord(t, filepath.Join(root, "acc", "org", "local_bbbbbbbbbb-2.json"))
	if b["title"] != "Ai title" || b["titleSource"] != "auto" {
		t.Fatalf("title fallback = %v (%v), want Ai title (auto)", b["title"], b["titleSource"])
	}
	if b["isArchived"] != true {
		t.Fatal("chat idle for 36 days is not archived")
	}
	if b["lastActivityAt"] != b["createdAt"] {
		t.Fatalf("lastActivityAt = %v, want createdAt %v when Last is missing", b["lastActivityAt"], b["createdAt"])
	}
	if b["originCwd"] != "/home/u/repo" || b["worktreePath"] != "/home/u/repo/.claude/worktrees/feat-x" ||
		b["worktreeName"] != "feat-x" {
		t.Fatalf("worktree split wrong: %v", b)
	}
	if _, ok := b["branch"]; ok {
		t.Fatal("empty branch was written")
	}

	// Chat C was skipped above because its entry existed, so check its
	// fallbacks on a fresh folder.
	fresh := t.TempDir()
	if _, err := writeSSHChats(fresh, "acc", "org", chats[2:3], now); err != nil {
		t.Fatal(err)
	}
	cr := sshTestRecord(t, filepath.Join(fresh, "acc", "org", "local_cccccccccc-3.json"))
	if cr["title"] != "SSH chat cccccccc" || cr["titleSource"] != "auto" {
		t.Fatalf("default title = %v (%v)", cr["title"], cr["titleSource"])
	}
	if cr["isArchived"] != false || cr["lastActivityAt"] != cr["createdAt"] {
		t.Fatalf("record C archive or activity wrong: %v", cr)
	}
	wantCreated := float64(time.Date(2026, 11, 4, 22, 0, 0, 123456789, time.UTC).UnixMilli())
	if cr["createdAt"] != wantCreated {
		t.Fatalf("createdAt = %v, want %v", cr["createdAt"], wantCreated)
	}
}

func TestSSHCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "ssh.json")
	got, err := loadSSHCache(path)
	if err != nil || got != nil {
		t.Fatalf("missing cache = %v, %v; want nil, nil", got, err)
	}

	want := []sshChat{
		{Host: "u@h", Path: "/p.jsonl", Session: "s1", First: "2026-11-01T00:00:00Z", Title: "t"},
		{Host: "v@h", Path: "/q.jsonl", Session: "s2", Cwd: "/c", Model: "m"},
	}
	if err := saveSSHCache(path, want); err != nil {
		t.Fatal(err)
	}
	got, err = loadSSHCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

// sshTestRead returns the content of path.
func sshTestRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// sshTestRecord decodes the chat entry at path.
func sshTestRecord(t *testing.T, path string) map[string]any {
	t.Helper()
	var rec map[string]any
	if err := json.Unmarshal([]byte(sshTestRead(t, path)), &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestWriteSSHChatsRejectsUnsafeSession(t *testing.T) {
	root := t.TempDir()
	chats := []sshChat{
		{Host: "h", Session: "../escape", Cwd: "/w", First: "2026-11-01T10:00:00Z"},
		{Host: "h", Session: "a/b", Cwd: "/w", First: "2026-11-01T10:00:00Z"},
		{Host: "h", Session: "ok-1", Cwd: "/w", First: "2026-11-01T10:00:00Z"},
	}
	n, err := writeSSHChats(root, "acc", "org", chats, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("writeSSHChats = %d, %v; want 1, nil", n, err)
	}
	if _, err := os.Stat(filepath.Join(root, "acc", "local_ok-1.json")); err == nil {
		t.Fatal("unexpected file outside org folder")
	}
	files, _ := filepath.Glob(filepath.Join(root, "*", "*", "*"))
	if len(files) != 1 || filepath.Base(files[0]) != "local_ok-1.json" {
		t.Fatalf("files = %v", files)
	}
}
