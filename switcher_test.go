package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const sampleConfig = "{\n\t\"locale\": \"en-US\",\n\t\"oauth:tokenCache\": \"old\",\n\t\"nested\": {\n\t\t\"a\": [\n\t\t\t1,\n\t\t\t2\n\t\t]\n\t},\n\t\"lastKnownAccountUuid\": \"acc-a\",\n\t\"oauth:tokenCacheV2\": \"djEwAAAA\",\n\t\"zoom\": 1.5\n}"

func TestObjectRoundTripKeepsFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	write(t, path, sampleConfig)
	o, err := readObject(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.write(path); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != sampleConfig {
		t.Fatalf("round trip changed the file:\n%s", got)
	}
}

// The real app config must survive a round trip byte for byte.
// Set CLAUDE_CONFIG_SAMPLE to a copy of config.json to run it.
func TestRealConfigRoundTrip(t *testing.T) {
	src := os.Getenv("CLAUDE_CONFIG_SAMPLE")
	if src == "" {
		t.Skip("CLAUDE_CONFIG_SAMPLE not set")
	}
	want := read(t, src)
	path := filepath.Join(t.TempDir(), "config.json")
	write(t, path, want)
	o, err := readObject(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.write(path); err != nil {
		t.Fatal(err)
	}
	if read(t, path) != want {
		t.Fatal("real config changed on round trip")
	}
}

func TestProfileSwapKeepsSharedSettings(t *testing.T) {
	d := desktop{dir: t.TempDir()}
	profiles := t.TempDir()
	write(t, d.configPath(), sampleConfig)
	write(t, d.cookieFiles()[0], "cookies-a")

	if err := saveProfile(d, filepath.Join(profiles, "a")); err != nil {
		t.Fatal(err)
	}
	if err := clearSignIn(d); err != nil {
		t.Fatal(err)
	}
	if id, _ := d.activeAccount(); id != "" {
		t.Fatalf("still signed in as %q", id)
	}
	if _, err := os.Stat(d.cookieFiles()[0]); !os.IsNotExist(err) {
		t.Fatal("cookies not cleared")
	}

	// Account b signs in and changes a shared setting.
	cfg, _ := readObject(d.configPath())
	cfg.set("lastKnownAccountUuid", json.RawMessage(`"acc-b"`))
	cfg.set("oauth:tokenCacheV2", json.RawMessage(`"djEwBBBB"`))
	cfg.set("zoom", json.RawMessage(`2`))
	if err := cfg.write(d.configPath()); err != nil {
		t.Fatal(err)
	}
	write(t, d.cookieFiles()[0], "cookies-b")
	write(t, d.cookieFiles()[1], "journal-b")
	if err := saveProfile(d, filepath.Join(profiles, "b")); err != nil {
		t.Fatal(err)
	}

	if err := restoreProfile(d, filepath.Join(profiles, "a")); err != nil {
		t.Fatal(err)
	}
	if id, _ := d.activeAccount(); id != "acc-a" {
		t.Fatalf("active = %q, want acc-a", id)
	}
	if got := read(t, d.cookieFiles()[0]); got != "cookies-a" {
		t.Fatalf("cookies = %q", got)
	}
	if _, err := os.Stat(d.cookieFiles()[1]); !os.IsNotExist(err) {
		t.Fatal("journal of b left behind")
	}
	cfg, _ = readObject(d.configPath())
	if v, _ := cfg.get("zoom"); string(v) != "2" {
		t.Fatalf("shared setting lost: zoom = %s", v)
	}
	if v, _ := cfg.get("oauth:tokenCache"); string(v) != `"old"` {
		t.Fatalf("tokenCache = %s", v)
	}
}

func TestSyncSessions(t *testing.T) {
	root := t.TempDir()
	session := func(acct, org, id string, activity int64) string {
		p := filepath.Join(root, acct, org, "local_"+id+".json")
		write(t, p, `{"sessionId":"local_`+id+`","lastActivityAt":`+jsonInt(activity)+`}`)
		return p
	}
	session("a", "oa", "one", 100)
	session("a", "oa", "two", 300)
	session("a", "oa", "gone", 100)
	write(t, filepath.Join(root, "a", "oa", "deleted_gone"), "1")
	session("a", "oa", "dead", 100)
	write(t, filepath.Join(root, "b", "ob", "deleted_dead"), "1")
	session("b", "ob", "two", 200)
	session("b", "ob", "mine", 50)

	n, err := syncSessions(root, "b", "ob")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("wrote %d entries, want 2 (one, newer two)", n)
	}
	target := filepath.Join(root, "b", "ob")
	for _, name := range []string{"local_one.json", "local_two.json", "local_mine.json"} {
		if _, err := os.Stat(filepath.Join(target, name)); err != nil {
			t.Fatalf("%s missing", name)
		}
	}
	for _, name := range []string{"local_gone.json", "local_dead.json"} {
		if _, err := os.Stat(filepath.Join(target, name)); err == nil {
			t.Fatalf("deleted chat %s came back", name)
		}
	}
	if at, _ := lastActivity(filepath.Join(target, "local_two.json")); at != 300 {
		t.Fatalf("two not refreshed, activity %d", at)
	}
	if n, _ := syncSessions(root, "b", "ob"); n != 0 {
		t.Fatalf("second sync wrote %d, want 0", n)
	}

	// A brand new account gets its folder created.
	if n, _ := syncSessions(root, "c", "oc"); n != 3 {
		t.Fatalf("new account got %d, want 3", n)
	}
}

func jsonInt(v int64) string { b, _ := json.Marshal(v); return string(b) }

func TestAutoTarget(t *testing.T) {
	now := time.Now()
	s := &switcher{st: State{AutoSwitch: true, Threshold: 96}, active: "a"}
	full := &Account{UUID: "a", Usage: &Usage{Session: 97, CheckedAt: now}}
	roomy := &Account{UUID: "b", Usage: &Usage{Session: 20, Weekly: 40, CheckedAt: now}}
	tight := &Account{UUID: "c", Usage: &Usage{Session: 90, CheckedAt: now}}
	reset := &Account{UUID: "d", Usage: &Usage{Session: 100, SessionReset: now.Add(-time.Minute), Weekly: 10, CheckedAt: now}}
	s.st.Accounts = []*Account{full, tight, roomy, reset}

	if got := s.autoTarget(true, now); got != reset {
		t.Fatalf("picked %v, want d (its session window already reset)", got)
	}
	if got := s.autoTarget(false, now); got != nil {
		t.Fatal("switched while Claude is closed")
	}
	full.Usage.CheckedAt = now.Add(-activeFresh - time.Minute)
	if got := s.autoTarget(true, now); got != nil {
		t.Fatal("switched on stale numbers for the active account")
	}
	full.Usage.CheckedAt = now
	reset.Usage.CheckedAt = now.Add(-idleFresh - time.Minute)
	if got := s.autoTarget(true, now); got != roomy {
		t.Fatalf("picked %v, want b once d is stale", got)
	}
	reset.Usage.CheckedAt = now
	s.st.LastSwitch = now.Add(-time.Minute)
	if got := s.autoTarget(true, now); got != nil {
		t.Fatal("switched again during the cooldown")
	}
	s.st.LastSwitch = time.Time{}
	full.Usage.Session = 50
	if got := s.autoTarget(true, now); got != nil {
		t.Fatalf("switched below the threshold to %s", got.UUID)
	}
}
