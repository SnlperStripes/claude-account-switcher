package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Claude Code in a terminal (the `claude` command) keeps its own sign-in,
// apart from the desktop app's: the claudeAiOauth entry of .credentials.json
// and the oauthAccount entry of .claude.json. With "cliFollow" on, every
// switch moves that sign-in to the account the app switched to.
//
// A running claude notices the swap on its own: it re-reads .credentials.json
// when the file changes, and before it refreshes a token it takes the stored
// one instead of its own once they differ. Refresh tokens are used up by a
// refresh, so a saved sign-in is only good while no one refreshes it: the
// switcher never refreshes or sends these tokens, it takes the same locks
// Claude Code takes for a refresh while it swaps, and it keeps the copy of the
// signed-in account current, because `/login` with another account drops the
// old tokens from the file.

// cliCredKeys are the .credentials.json entries that belong to the signed-in
// claude.ai account, the ones Claude Code itself removes when it signs in
// with another one. Everything else, like the tokens of MCP servers, stays.
var cliCredKeys = []string{"claudeAiOauth", "organizationUuid", "trustedDeviceToken", "designOauth"}

// cliCacheKeys are the .claude.json caches that Claude Code clears when the
// account changes. They are filled in again on the next start.
var cliCacheKeys = []string{
	"additionalModelOptionsCache", "additionalModelOptionsAnsweredAt", "additionalModelCostsCache",
	"modelAccessCache", "orgModelDefaultCache", "cachedArtifactRoster", "artifactRosterDenied",
	"lastSeenOrgDefaultUpdatedAt", "clientDataCache", "clientDataCacheSlots", "autoCompactWindowsCache",
	"cachedUsageUtilization", "githubWebConnectionStatusCache", "startupPrefetchedAt",
}

const (
	// Claude Code's refresh locks count as abandoned after this long, its
	// config lock after the proper-lockfile default.
	cliRefreshStale = time.Minute
	cliConfigStale  = 10 * time.Second
	// Saved Claude Code sign-ins in the switcher's folder, and backups of them.
	cliProfileFile = "cli.json"
	cliBackups     = "cli-backups"
)

// errCLIBusy is translated when it is shown, since the language is picked after start.
// How long a swap waits for a refresh or config write of Claude Code to end.
var cliLockWait = 15 * time.Second

var errCLIBusy error = cliBusy{}

type cliBusy struct{}

func (cliBusy) Error() string {
	return tr("Claude Code kept its sign-in locked, try again", "Claude Code 一直锁定着登录信息，请重试")
}

// claudeCode names the files of Claude Code's sign-in.
type claudeCode struct {
	credentials string
	config      string
}

// claudeCodeFiles finds them the way Claude Code does: CLAUDE_CONFIG_DIR
// moves both into that folder, otherwise they are ~/.claude/.credentials.json
// and ~/.claude.json.
func claudeCodeFiles() claudeCode {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return claudeCode{filepath.Join(dir, ".credentials.json"), filepath.Join(dir, ".claude.json")}
	}
	home, _ := os.UserHomeDir()
	return claudeCode{filepath.Join(home, ".claude", ".credentials.json"), filepath.Join(home, ".claude.json")}
}

// lockRefresh takes the two locks Claude Code holds while it refreshes its
// sign-in, in its order: .oauth_refresh.lock in the config folder and the
// older one next to that folder.
func (c claudeCode) lockRefresh(wait time.Duration) (func(), error) {
	dir := filepath.Dir(c.credentials)
	first, err := lockFolder(filepath.Join(dir, ".oauth_refresh.lock"), cliRefreshStale, wait)
	if err != nil {
		return nil, err
	}
	second, err := lockFolder(dir+".lock", cliRefreshStale, wait)
	if err != nil {
		first()
		return nil, err
	}
	return func() { second(); first() }, nil
}

// lockFolder takes a proper-lockfile lock, the kind Claude Code uses: a folder
// that exists while someone holds it. One that has not been touched for
// stale was left behind and is taken over.
func lockFolder(path string, stale, wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	for {
		err := os.Mkdir(path, 0o700)
		if err == nil {
			return func() {
				if err := os.Remove(path); err != nil {
					log.Printf("release %s: %v", path, err)
				}
			}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > stale {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errCLIBusy
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// encode writes an object compact, or indented like Claude Code writes .claude.json.
func (o *object) encode(indent string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, _ := json.Marshal(k)
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(o.vals[k])
	}
	buf.WriteByte('}')
	if indent == "" {
		var out bytes.Buffer
		err := json.Compact(&out, buf.Bytes())
		return out.Bytes(), err
	}
	var out bytes.Buffer
	err := json.Indent(&out, buf.Bytes(), "", indent)
	return out.Bytes(), err
}

func (o *object) save(path, indent string) error {
	data, err := o.encode(indent)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// readObjectOrEmpty reads a JSON object; a missing file is an empty one.
func readObjectOrEmpty(path string) (*object, error) {
	o, err := readObject(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &object{vals: map[string]json.RawMessage{}}, nil
	}
	return o, err
}

// cliLogin is who Claude Code says it is signed in as.
type cliLogin struct {
	AccountUUID string `json:"accountUuid"`
	OrgUUID     string `json:"organizationUuid"`
	Email       string `json:"emailAddress"`
}

// signedIn says whether Claude Code holds claude.ai tokens.
func signedIn(creds *object) bool {
	raw, ok := creds.get("claudeAiOauth")
	return ok && string(raw) != "null"
}

// loginOf reads whose tokens Claude Code holds, or nil when its account entry
// does not say.
func loginOf(cfg *object) *cliLogin {
	raw, ok := cfg.get("oauthAccount")
	if !ok {
		return nil
	}
	var l cliLogin
	if json.Unmarshal(raw, &l) != nil || (l.AccountUUID == "" && l.Email == "") {
		return nil
	}
	return &l
}

type cliOAuth struct {
	RefreshToken          string `json:"refreshToken"`
	RefreshTokenExpiresAt int64  `json:"refreshTokenExpiresAt"`
}

func oauthOf(o *object) cliOAuth {
	var t cliOAuth
	if raw, ok := o.get("claudeAiOauth"); ok {
		_ = json.Unmarshal(raw, &t)
	}
	return t
}

func hasRefreshToken(o *object) bool { return oauthOf(o).RefreshToken != "" }

// expired says whether a saved sign-in can no longer be refreshed.
func expired(o *object, now time.Time) bool {
	t := oauthOf(o)
	return t.RefreshTokenExpiresAt > 0 && now.UnixMilli() >= t.RefreshTokenExpiresAt
}

// matchLogin finds the saved account a Claude Code sign-in belongs to: same
// account id, else same email, and the same organization when both say one.
func matchLogin(known []Account, l *cliLogin) *Account {
	sameOrg := func(a Account) bool { return a.Org == "" || l.OrgUUID == "" || a.Org == l.OrgUUID }
	for i, a := range known {
		if l.AccountUUID != "" && a.UUID == l.AccountUUID && sameOrg(a) {
			return &known[i]
		}
	}
	for i, a := range known {
		if l.Email != "" && strings.EqualFold(a.Email, l.Email) && sameOrg(a) {
			return &known[i]
		}
	}
	return nil
}

// cliProfile is the saved Claude Code sign-in of one account: its
// cliCredKeys and its oauthAccount, in one file next to the app's sign-in.
func cliProfile(creds, cfg *object) *object {
	p := &object{vals: map[string]json.RawMessage{}}
	for _, k := range cliCredKeys {
		if v, ok := creds.get(k); ok {
			p.set(k, v)
		}
	}
	if v, ok := cfg.get("oauthAccount"); ok {
		p.set("oauthAccount", v)
	}
	return p
}

// keepCLIProfile saves the sign-in under dir unless the copy there is the same.
func keepCLIProfile(dir string, p *object) (bool, error) {
	data, err := p.encode("")
	if err != nil {
		return false, err
	}
	path := filepath.Join(dir, cliProfileFile)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	return true, writeFileAtomic(path, data)
}

// switchCLI signs Claude Code in as target with the sign-in saved for it in
// profileDir(target.UUID), after saving the current one under its own
// account. The note tells the user what is left to do when Claude Code could
// not follow; an error means the files are back as they were.
func switchCLI(c claudeCode, known []Account, target Account, profileDir func(string) string, backupRoot string, now time.Time) (note string, err error) {
	if _, err := os.Stat(c.config); errors.Is(err, fs.ErrNotExist) {
		return tr("Claude Code in the terminal is not set up here, run claude once", "此电脑上尚未设置终端中的 Claude Code，请先运行一次 claude"), nil
	}
	unlock, err := c.lockRefresh(cliLockWait)
	if err != nil {
		return "", err
	}
	defer unlock()

	creds, err := readObjectOrEmpty(c.credentials)
	if err != nil {
		return "", err
	}
	cfg, err := readObject(c.config)
	if err != nil {
		return "", err
	}
	var from *Account
	if signedIn(creds) {
		live := loginOf(cfg)
		if live != nil {
			from = matchLogin(known, live)
		}
		if from == nil {
			who := tr("another account", "另一个账号")
			if live != nil && live.Email != "" {
				who = live.Email
			}
			return fmt.Sprintf(tr("Claude Code in the terminal is signed in as %s, which the switcher does not have, so it was left as is",
				"终端中的 Claude Code 登录的是 %s，切换器中没有这个账号，因此未做改动"), who), nil
		}
		// Its latest tokens, which the copy from the last switch may predate.
		if _, err := keepCLIProfile(profileDir(from.UUID), cliProfile(creds, cfg)); err != nil {
			return "", fmt.Errorf(tr("save the current sign-in: %w", "保存当前登录：%w"), err)
		}
		if from.UUID == target.UUID {
			return "", nil
		}
	}

	saved, err := readObject(filepath.Join(profileDir(target.UUID), cliProfileFile))
	switch {
	case errors.Is(err, fs.ErrNotExist) || (err == nil && !hasRefreshToken(saved)):
		if from == nil {
			return fmt.Sprintf(tr("Claude Code in the terminal is signed out, run /login there once as %s",
				"终端中的 Claude Code 未登录，请在其中以 %s 运行一次 /login"), target.label()), nil
		}
		return fmt.Sprintf(tr("Claude Code in the terminal stays on %s until you run /login there once as %s",
			"终端中的 Claude Code 仍使用 %s，请在其中以 %s 运行一次 /login"), from.label(), target.label()), nil
	case err != nil:
		return "", err
	case expired(saved, now):
		return fmt.Sprintf(tr("The Claude Code sign-in of %s has expired, run /login in the terminal once",
			"%s 在 Claude Code 中的登录已过期，请在终端中运行一次 /login"), target.label()), nil
	}

	bk, err := backupCLI(c, backupRoot, now)
	if err != nil {
		return "", fmt.Errorf(tr("backup: %w", "备份：%w"), err)
	}
	if err := applyCLIProfile(c, saved); err != nil {
		if rerr := restoreCLI(c, bk); rerr != nil {
			log.Printf("restore Claude Code backup %s: %v", bk, rerr)
		}
		return "", err
	}
	return "", nil
}

// applyCLIProfile writes a saved sign-in into Claude Code's files. Callers
// hold the refresh locks.
func applyCLIProfile(c claudeCode, saved *object) error {
	creds, err := readObjectOrEmpty(c.credentials)
	if err != nil {
		return err
	}
	for _, k := range cliCredKeys {
		if v, ok := saved.get(k); ok {
			creds.set(k, v)
		} else {
			creds.del(k)
		}
	}
	if err := creds.save(c.credentials, ""); err != nil {
		return err
	}
	account, _ := saved.get("oauthAccount")
	return editConfig(c, func(cfg *object) {
		if account != nil {
			cfg.set("oauthAccount", account)
		} else {
			cfg.del("oauthAccount")
		}
		for _, k := range cliCacheKeys {
			cfg.del(k)
		}
	})
}

// editConfig changes .claude.json under Claude Code's config lock, reading it
// fresh, because every running claude writes it too.
func editConfig(c claudeCode, change func(*object)) error {
	unlock, err := lockFolder(c.config+".lock", cliConfigStale, cliLockWait)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := readObject(c.config)
	if err != nil {
		return err
	}
	change(cfg)
	return cfg.save(c.config, "  ")
}

// backupCLI keeps both files before a swap touches them. They hold the same
// tokens as Claude Code's own file, so the folder is the user's alone.
func backupCLI(c claudeCode, root string, now time.Time) (string, error) {
	dir := filepath.Join(root, now.Format("20060102-150405.000"))
	for _, f := range []string{c.credentials, c.config} {
		if err := copyFile(f, filepath.Join(dir, filepath.Base(f))); err != nil {
			return "", err
		}
	}
	pruneBackups(root)
	return dir, nil
}

// restoreCLI undoes a swap that failed halfway. It puts back only the entries
// the swap changes, since running claude processes keep writing the rest.
func restoreCLI(c claudeCode, dir string) error {
	old, err := readObjectOrEmpty(filepath.Join(dir, filepath.Base(c.credentials)))
	if err != nil {
		return err
	}
	creds, err := readObjectOrEmpty(c.credentials)
	if err != nil {
		return err
	}
	for _, k := range cliCredKeys {
		if v, ok := old.get(k); ok {
			creds.set(k, v)
		} else {
			creds.del(k)
		}
	}
	if err := creds.save(c.credentials, ""); err != nil {
		return err
	}
	oldCfg, err := readObject(filepath.Join(dir, filepath.Base(c.config)))
	if err != nil {
		return err
	}
	return editConfig(c, func(cfg *object) {
		for _, k := range append([]string{"oauthAccount"}, cliCacheKeys...) {
			if v, ok := oldCfg.get(k); ok {
				cfg.set(k, v)
			} else {
				cfg.del(k)
			}
		}
	})
}

// cliSeen is the Claude Code sign-in mirrorCLI found on its last look.
type cliSeen struct {
	account  string
	modified time.Time
}

// mirrorCLI keeps the saved copy of the account Claude Code is signed in with
// current, so a later `/login` with another account, which drops these
// tokens from the file, loses nothing. It only reads Claude Code's files. A
// sign-in is copied once it looks the same on two looks in a row: `/login`
// writes the account entry and the tokens one after the other, and a copy
// taken in between would mix two accounts.
func mirrorCLI(c claudeCode, known []Account, profileDir func(string) string, last cliSeen) (cliSeen, error) {
	info, err := os.Stat(c.credentials)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		return cliSeen{}, err
	}
	creds, err := readObject(c.credentials)
	if err != nil {
		return cliSeen{}, err
	}
	cfg, err := readObject(c.config)
	if err != nil {
		return cliSeen{}, err
	}
	live := loginOf(cfg)
	if !hasRefreshToken(creds) || live == nil {
		return cliSeen{}, nil
	}
	a := matchLogin(known, live)
	if a == nil {
		return cliSeen{}, nil
	}
	now := cliSeen{account: a.UUID, modified: info.ModTime()}
	if now.account != last.account || !now.modified.Equal(last.modified) {
		return now, nil
	}
	changed, err := keepCLIProfile(profileDir(a.UUID), cliProfile(creds, cfg))
	if changed {
		log.Printf("kept the Claude Code sign-in of %s", a.label())
	}
	return now, err
}

// followInCLI moves Claude Code in the terminal to the account the app just
// switched to, when the user turned that on.
func (s *switcher) followInCLI(target Account) {
	s.mu.Lock()
	on := s.st.CLIFollow
	known := make([]Account, 0, len(s.st.Accounts))
	for _, a := range s.st.Accounts {
		known = append(known, *a)
	}
	s.mu.Unlock()
	if !on || !cliSupported {
		return
	}
	note, err := switchCLI(claudeCodeFiles(), known, target, s.profileDir, filepath.Join(s.dir, cliBackups), time.Now())
	if err != nil {
		note = tr("Claude Code in the terminal did not switch: ", "终端中的 Claude Code 未切换：") + err.Error()
	}
	if note != "" {
		log.Print(note)
	} else {
		log.Printf("Claude Code in the terminal is on %s", target.label())
	}
	s.mu.Lock()
	s.cli.note = note
	s.cli.seen = cliSeen{}
	s.mu.Unlock()
	s.onChange()
}

// keepCLI runs mirrorCLI when Claude Code follows the app and no switch is
// running.
func (s *switcher) keepCLI() {
	s.mu.Lock()
	on := s.st.CLIFollow
	last := s.cli.seen
	known := make([]Account, 0, len(s.st.Accounts))
	for _, a := range s.st.Accounts {
		known = append(known, *a)
	}
	s.mu.Unlock()
	if !on || !cliSupported || s.busy.Load() {
		return
	}
	seen, err := mirrorCLI(claudeCodeFiles(), known, s.profileDir, last)
	if err != nil {
		log.Printf("keep the Claude Code sign-in: %v", err)
	}
	s.mu.Lock()
	s.cli.seen = seen
	s.mu.Unlock()
}

func (s *switcher) setCLIFollow(on bool) {
	s.mu.Lock()
	s.st.CLIFollow = on
	s.cli = cliState{}
	s.saveState()
	s.mu.Unlock()
	s.onChange()
}

// cliState is what the switcher knows about Claude Code between switches.
type cliState struct {
	// note says what kept Claude Code from following the last switch.
	note string
	seen cliSeen
}
