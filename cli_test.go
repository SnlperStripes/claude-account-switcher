package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Fixtures in the shape of Claude Code's files, with made-up tokens.

func cliCreds(account string) string {
	return `{"mcpOAuth":{"plugin:x|1":{"accessToken":"mcp-token"}},"claudeAiOauth":{"accessToken":"access-` + account +
		`","refreshToken":"refresh-` + account + `","expiresAt":1,"refreshTokenExpiresAt":4102444800000,"scopes":["user:inference"]}}`
}

func cliAccount(uuid, org, email string) string {
	return `{"accountUuid":"` + uuid + `","organizationUuid":"` + org + `","emailAddress":"` + email + `","displayName":"x"}`
}

func cliConfig(oauthAccount string) string {
	cfg := `{"numStartups":7,"projects":{"C:/a":{"x":1},"c:/a":{"x":2}},"modelAccessCache":{"m":1},"orgModelDefaultCache":{"o":1}`
	if oauthAccount != "" {
		cfg += `,"oauthAccount":` + oauthAccount
	}
	return cfg + `,"userID":"u"}`
}

var cliKnown = []Account{
	{UUID: "acc-a", Org: "org-a", Name: "Anna", Email: "anna@example.com", Plan: "Pro"},
	{UUID: "acc-b", Org: "org-b", Name: "Bert", Email: "bert@example.com", Plan: "Max"},
}

type cliFixture struct {
	c        claudeCode
	profiles string
	backups  string
}

func newCLIFixture(t *testing.T, creds, cfg string) cliFixture {
	t.Helper()
	dir := t.TempDir()
	f := cliFixture{
		c:        claudeCode{credentials: filepath.Join(dir, ".claude", ".credentials.json"), config: filepath.Join(dir, ".claude.json")},
		profiles: filepath.Join(dir, "switcher", "accounts"),
		backups:  filepath.Join(dir, "switcher", cliBackups),
	}
	if err := os.MkdirAll(filepath.Dir(f.c.credentials), 0o700); err != nil {
		t.Fatal(err)
	}
	if creds != "" {
		write(t, f.c.credentials, creds)
	}
	if cfg != "" {
		write(t, f.c.config, cfg)
	}
	return f
}

func (f cliFixture) profileDir(uuid string) string { return filepath.Join(f.profiles, uuid) }

func (f cliFixture) saveProfile(t *testing.T, uuid, creds, account string) {
	t.Helper()
	var c map[string]json.RawMessage
	if err := json.Unmarshal([]byte(creds), &c); err != nil {
		t.Fatal(err)
	}
	p := `{"claudeAiOauth":` + string(c["claudeAiOauth"]) + `,"oauthAccount":` + account + `}`
	write(t, filepath.Join(f.profileDir(uuid), cliProfileFile), p)
}

// field returns the value of a top-level key of a JSON file, "" when missing.
func field(t *testing.T, path, key string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return string(m[key])
}

func refreshTokenIn(t *testing.T, path string) string {
	t.Helper()
	var o struct{ RefreshToken string }
	if raw := field(t, path, "claudeAiOauth"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &o); err != nil {
			t.Fatal(err)
		}
	}
	return o.RefreshToken
}

func accountIn(t *testing.T, path string) string {
	t.Helper()
	var o struct{ AccountUUID string }
	if raw := field(t, path, "oauthAccount"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &o); err != nil {
			t.Fatal(err)
		}
	}
	return o.AccountUUID
}

func TestSwitchCLI(t *testing.T) {
	now := time.Date(2026, 11, 12, 20, 0, 0, 0, time.UTC)
	accA := cliAccount("acc-a", "org-a", "anna@example.com")
	accB := cliAccount("acc-b", "org-b", "bert@example.com")
	expiredB := strings.Replace(cliCreds("b"), "4102444800000", "1700000000000", 1)

	tests := []struct {
		name  string
		creds string
		cfg   string
		// saved is the copy of account b's Claude Code sign-in, "" for none.
		saved    string
		target   string
		wantNote string
		// The account and refresh token Claude Code ends up with.
		wantAccount, wantToken string
		// Whether the live sign-in of a was kept in the switcher's folder.
		wantKeptA bool
	}{
		{name: "switches to the saved sign-in", creds: cliCreds("a"), cfg: cliConfig(accA), saved: cliCreds("b"),
			target: "acc-b", wantAccount: "acc-b", wantToken: "refresh-b", wantKeptA: true},
		{name: "no saved sign-in asks for /login", creds: cliCreds("a"), cfg: cliConfig(accA),
			target: "acc-b", wantNote: "/login there once as Bert (Max)", wantAccount: "acc-a", wantToken: "refresh-a", wantKeptA: true},
		{name: "signed out takes the saved sign-in", creds: `{"mcpOAuth":{}}`, cfg: cliConfig(""), saved: cliCreds("b"),
			target: "acc-b", wantAccount: "acc-b", wantToken: "refresh-b"},
		{name: "signed out without a saved sign-in", cfg: cliConfig(""),
			target: "acc-b", wantNote: "is signed out"},
		{name: "unknown account is left alone", creds: cliCreds("c"), cfg: cliConfig(cliAccount("acc-c", "org-c", "carl@example.com")), saved: cliCreds("b"),
			target: "acc-b", wantNote: "carl@example.com", wantAccount: "acc-c", wantToken: "refresh-c"},
		{name: "tokens without an account entry are left alone", creds: cliCreds("c"), cfg: cliConfig(""), saved: cliCreds("b"),
			target: "acc-b", wantNote: "another account", wantToken: "refresh-c"},
		{name: "other organization of a known account is left alone", creds: cliCreds("a"), cfg: cliConfig(cliAccount("acc-a", "org-team", "anna@example.com")), saved: cliCreds("b"),
			target: "acc-b", wantNote: "anna@example.com", wantAccount: "acc-a", wantToken: "refresh-a"},
		{name: "matched by email", creds: cliCreds("a"), cfg: cliConfig(cliAccount("acc-other-id", "org-a", "ANNA@example.com")), saved: cliCreds("b"),
			target: "acc-b", wantAccount: "acc-b", wantToken: "refresh-b", wantKeptA: true},
		{name: "expired saved sign-in asks for /login", creds: cliCreds("a"), cfg: cliConfig(accA), saved: expiredB,
			target: "acc-b", wantNote: "has expired", wantAccount: "acc-a", wantToken: "refresh-a", wantKeptA: true},
		{name: "already on the target", creds: cliCreds("a"), cfg: cliConfig(accA),
			target: "acc-a", wantAccount: "acc-a", wantToken: "refresh-a", wantKeptA: true},
		{name: "Claude Code not set up", target: "acc-b", wantNote: "not set up"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCLIFixture(t, tt.creds, tt.cfg)
			if tt.saved != "" {
				f.saveProfile(t, "acc-b", tt.saved, accB)
			}
			var target Account
			for _, a := range cliKnown {
				if a.UUID == tt.target {
					target = a
				}
			}
			note, err := switchCLI(f.c, cliKnown, target, f.profileDir, f.backups, now)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantNote == "" && note != "" || !strings.Contains(note, tt.wantNote) {
				t.Fatalf("note = %q, want it to contain %q", note, tt.wantNote)
			}
			if got := accountIn(t, f.c.config); got != tt.wantAccount {
				t.Errorf("account = %q, want %q", got, tt.wantAccount)
			}
			if got := refreshTokenIn(t, f.c.credentials); got != tt.wantToken {
				t.Errorf("refresh token = %q, want %q", got, tt.wantToken)
			}
			keptA := refreshTokenIn(t, filepath.Join(f.profileDir("acc-a"), cliProfileFile)) == "refresh-a"
			if keptA != tt.wantKeptA {
				t.Errorf("kept a = %v, want %v", keptA, tt.wantKeptA)
			}
			if _, err := os.Stat(filepath.Join(f.profileDir("acc-c"), cliProfileFile)); err == nil {
				t.Error("saved the sign-in of an unknown account")
			}
			// The locks are released.
			for _, l := range []string{filepath.Join(filepath.Dir(f.c.credentials), ".oauth_refresh.lock"), filepath.Dir(f.c.credentials) + ".lock", f.c.config + ".lock"} {
				if _, err := os.Stat(l); err == nil {
					t.Errorf("%s still held", filepath.Base(l))
				}
			}
		})
	}
}

// A swap changes only the account's entries and leaves the rest of both files.
func TestSwitchCLIKeepsTheRest(t *testing.T) {
	accA := cliAccount("acc-a", "org-a", "anna@example.com")
	accB := cliAccount("acc-b", "org-b", "bert@example.com")
	f := newCLIFixture(t, strings.Replace(cliCreds("a"), `{"mcpOAuth"`, `{"organizationUuid":"org-a","mcpOAuth"`, 1), cliConfig(accA))
	f.saveProfile(t, "acc-b", cliCreds("b"), accB)
	if _, err := switchCLI(f.c, cliKnown, cliKnown[1], f.profileDir, f.backups, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := field(t, f.c.credentials, "mcpOAuth"); got != `{"plugin:x|1":{"accessToken":"mcp-token"}}` {
		t.Errorf("mcpOAuth = %s", got)
	}
	if got := field(t, f.c.credentials, "organizationUuid"); got != "" {
		t.Errorf("organizationUuid of a is still there: %s", got)
	}
	cfg := read(t, f.c.config)
	for _, want := range []string{`"numStartups": 7`, `"C:/a": {`, `"c:/a": {`, `"userID": "u"`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lost %s:\n%s", want, cfg)
		}
	}
	for _, gone := range []string{"modelAccessCache", "orgModelDefaultCache"} {
		if strings.Contains(cfg, gone) {
			t.Errorf("config still has the cache %s of a", gone)
		}
	}
	// The backup has both files as they were.
	dirs, _ := os.ReadDir(f.backups)
	if len(dirs) != 1 {
		t.Fatalf("%d backups, want 1", len(dirs))
	}
	bk := filepath.Join(f.backups, dirs[0].Name())
	if refreshTokenIn(t, filepath.Join(bk, ".credentials.json")) != "refresh-a" || accountIn(t, filepath.Join(bk, ".claude.json")) != "acc-a" {
		t.Error("backup does not hold the sign-in of a")
	}
}

// A refresh in progress keeps the swap waiting; a lock left behind is taken over.
func TestSwitchCLILocks(t *testing.T) {
	defer func(w time.Duration) { cliLockWait = w }(cliLockWait)
	cliLockWait = 300 * time.Millisecond
	accA := cliAccount("acc-a", "org-a", "anna@example.com")
	for _, tt := range []struct {
		name    string
		age     time.Duration
		wantErr bool
	}{
		{"held", 0, true},
		{"left behind", 2 * cliRefreshStale, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newCLIFixture(t, cliCreds("a"), cliConfig(accA))
			f.saveProfile(t, "acc-b", cliCreds("b"), cliAccount("acc-b", "org-b", "bert@example.com"))
			lock := filepath.Join(filepath.Dir(f.c.credentials), ".oauth_refresh.lock")
			if err := os.Mkdir(lock, 0o700); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-tt.age)
			if err := os.Chtimes(lock, old, old); err != nil {
				t.Fatal(err)
			}
			_, err := switchCLI(f.c, cliKnown, cliKnown[1], f.profileDir, f.backups, time.Now())
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}
			want := "refresh-b"
			if tt.wantErr {
				want = "refresh-a"
			}
			if got := refreshTokenIn(t, f.c.credentials); got != want {
				t.Errorf("refresh token = %q, want %q", got, want)
			}
		})
	}
}

// When .claude.json cannot be changed, the tokens are put back too.
func TestSwitchCLIRollsBack(t *testing.T) {
	defer func(w time.Duration) { cliLockWait = w }(cliLockWait)
	cliLockWait = 300 * time.Millisecond
	accA := cliAccount("acc-a", "org-a", "anna@example.com")
	f := newCLIFixture(t, cliCreds("a"), cliConfig(accA))
	f.saveProfile(t, "acc-b", cliCreds("b"), cliAccount("acc-b", "org-b", "bert@example.com"))
	// A claude writing .claude.json the whole time.
	if err := os.Mkdir(f.c.config+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	before := read(t, f.c.config)
	if _, err := switchCLI(f.c, cliKnown, cliKnown[1], f.profileDir, f.backups, time.Now()); err == nil {
		t.Fatal("no error")
	}
	if got := refreshTokenIn(t, f.c.credentials); got != "refresh-a" {
		t.Errorf("refresh token = %q, want the one of a back", got)
	}
	if read(t, f.c.config) != before {
		t.Error(".claude.json changed")
	}
}

func TestMirrorCLI(t *testing.T) {
	accA := cliAccount("acc-a", "org-a", "anna@example.com")
	f := newCLIFixture(t, cliCreds("a"), cliConfig(accA))
	kept := filepath.Join(f.profileDir("acc-a"), cliProfileFile)

	seen, err := mirrorCLI(f.c, cliKnown, f.profileDir, cliSeen{})
	if err != nil || seen.account != "acc-a" {
		t.Fatalf("first look: %+v, %v", seen, err)
	}
	if _, err := os.Stat(kept); err == nil {
		t.Fatal("copied on the first look")
	}
	if seen, err = mirrorCLI(f.c, cliKnown, f.profileDir, seen); err != nil {
		t.Fatal(err)
	}
	if refreshTokenIn(t, kept) != "refresh-a" || accountIn(t, kept) != "acc-a" {
		t.Fatal("not copied on the second look")
	}

	// `/login` as b has written the account entry but not yet the tokens.
	write(t, f.c.config, cliConfig(cliAccount("acc-b", "org-b", "bert@example.com")))
	if seen, err = mirrorCLI(f.c, cliKnown, f.profileDir, seen); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.profileDir("acc-b"), cliProfileFile)); err == nil {
		t.Fatal("copied the tokens of a as b")
	}
	// Now it has, and they stay the same until the next look.
	write(t, f.c.credentials, cliCreds("b"))
	for range 2 {
		if seen, err = mirrorCLI(f.c, cliKnown, f.profileDir, seen); err != nil {
			t.Fatal(err)
		}
	}
	if got := refreshTokenIn(t, filepath.Join(f.profileDir("acc-b"), cliProfileFile)); got != "refresh-b" {
		t.Fatalf("b's copy has %q", got)
	}
	if refreshTokenIn(t, kept) != "refresh-a" {
		t.Fatal("a's copy changed")
	}
}

// A swap there and back on copies of real Claude Code files leaves every
// entry it does not own as it was and brings the original sign-in back.
// Set CLAUDE_CODE_SAMPLE_HOME to a home folder to run it; its files are only
// read, the test works on copies in a temporary folder.
func TestSwitchCLIRealSample(t *testing.T) {
	home := os.Getenv("CLAUDE_CODE_SAMPLE_HOME")
	if home == "" {
		t.Skip("CLAUDE_CODE_SAMPLE_HOME not set")
	}
	f := newCLIFixture(t, read(t, filepath.Join(home, ".claude", ".credentials.json")), read(t, filepath.Join(home, ".claude.json")))
	origCreds, err := readObject(f.c.credentials)
	if err != nil {
		t.Fatal(err)
	}
	origCfg, err := readObject(f.c.config)
	if err != nil {
		t.Fatal(err)
	}
	live := loginOf(origCfg)
	if live == nil || !signedIn(origCreds) {
		t.Skip("the sample is not signed in")
	}
	me := Account{UUID: live.AccountUUID, Org: live.OrgUUID, Email: live.Email, Name: "me"}
	other := Account{UUID: "acc-sample-other", Org: "org-other", Email: "other@example.com", Name: "other"}
	f.saveProfile(t, other.UUID, cliCreds("other"), cliAccount(other.UUID, other.Org, other.Email))
	known := []Account{me, other}

	same := func(a, b json.RawMessage) bool {
		ca, err1 := (&object{keys: []string{"v"}, vals: map[string]json.RawMessage{"v": a}}).encode("")
		cb, err2 := (&object{keys: []string{"v"}, vals: map[string]json.RawMessage{"v": b}}).encode("")
		return err1 == nil && err2 == nil && string(ca) == string(cb)
	}
	check := func(stage string, wantOwn bool) {
		creds, err := readObject(f.c.credentials)
		if err != nil {
			t.Fatalf("%s: credentials: %v", stage, err)
		}
		cfg, err := readObject(f.c.config)
		if err != nil {
			t.Fatalf("%s: config: %v", stage, err)
		}
		for _, k := range origCreds.keys {
			if !slices.Contains(cliCredKeys, k) && !same(creds.vals[k], origCreds.vals[k]) {
				t.Errorf("%s: credentials entry %s changed", stage, k)
			}
		}
		for _, k := range origCfg.keys {
			if k != "oauthAccount" && !slices.Contains(cliCacheKeys, k) && !same(cfg.vals[k], origCfg.vals[k]) {
				t.Errorf("%s: config entry %s changed", stage, k)
			}
		}
		own := same(creds.vals["claudeAiOauth"], origCreds.vals["claudeAiOauth"]) && same(cfg.vals["oauthAccount"], origCfg.vals["oauthAccount"])
		if own != wantOwn {
			t.Errorf("%s: own sign-in in place = %v, want %v", stage, own, wantOwn)
		}
	}
	if note, err := switchCLI(f.c, known, other, f.profileDir, f.backups, time.Now()); err != nil || note != "" {
		t.Fatalf("switch away: %q, %v", note, err)
	}
	check("away", false)
	if note, err := switchCLI(f.c, known, me, f.profileDir, f.backups, time.Now()); err != nil || note != "" {
		t.Fatalf("switch back: %q, %v", note, err)
	}
	check("back", true)
}
