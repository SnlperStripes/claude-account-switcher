package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Chats run over SSH keep their transcripts on the server, in
// ~/.claude/projects. The desktop app only lists chats that have an entry
// in claude-code-sessions, so these chats are indexed from the servers listed
// in the app's SSH configs and in the existing entries.

// sshChat summarizes one transcript on an SSH host.
type sshChat struct {
	Host       string `json:"host"`
	Path       string `json:"path"`
	Session    string `json:"session"`
	First      string `json:"first,omitempty"`
	Last       string `json:"last,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Entrypoint string `json:"entrypoint,omitempty"`
	Title      string `json:"title,omitempty"`
	AITitle    string `json:"aiTitle,omitempty"`
	Model      string `json:"model,omitempty"`
}

// sshRemoteScript runs on the server with python3. It prints one JSON object
// per transcript that is not in KNOWN, which the caller prefixes.
const sshRemoteScript = `import glob, json, os
for path in sorted(glob.glob(os.path.expanduser("~/.claude/projects/*/*.jsonl"))):
    info = {"path": path, "session": os.path.basename(path)[:-6]}
    if info["session"] in KNOWN:
        continue
    try:
        with open(path, encoding="utf-8", errors="replace") as stream:
            for line in stream:
                try:
                    entry = json.loads(line)
                except ValueError:
                    continue
                if not isinstance(entry, dict):
                    continue
                stamp = entry.get("timestamp")
                if isinstance(stamp, str):
                    info.setdefault("first", stamp)
                    info["last"] = stamp
                for key, field in (("cwd", "cwd"), ("gitBranch", "branch"), ("entrypoint", "entrypoint")):
                    if isinstance(entry.get(key), str):
                        info.setdefault(field, entry[key])
                if entry.get("type") == "custom-title" and isinstance(entry.get("customTitle"), str):
                    info["title"] = entry["customTitle"]
                elif entry.get("type") == "ai-title" and isinstance(entry.get("aiTitle"), str):
                    info.setdefault("aiTitle", entry["aiTitle"])
                message = entry.get("message")
                if isinstance(message, dict) and isinstance(message.get("model"), str):
                    info["model"] = message["model"]
    except OSError:
        continue
    print(json.dumps(info))
`

// runSSH runs `ssh -o BatchMode=yes -o ConnectTimeout=5 <host> python3 -` with stdin and returns stdout.
// It is a variable so tests can replace it.
var runSSH = func(ctx context.Context, host, stdin string) ([]byte, error) {
	cmd := hidden(exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", host, "python3", "-"))
	cmd.Stdin = strings.NewReader(stdin)
	return cmd.Output()
}

// sshHosts lists the SSH hosts to scan: the desktop's SSH configs first, then
// the hosts of existing chat entries. Each host appears once.
func sshHosts(userData, sessionsRoot string) []string {
	var hosts []string
	seen := map[string]bool{}
	add := func(host string) {
		// A host starting with "-" would be read by ssh as an option.
		if host == "" || strings.HasPrefix(host, "-") || seen[host] {
			return
		}
		seen[host] = true
		hosts = append(hosts, host)
	}

	if data, err := os.ReadFile(filepath.Join(userData, "ssh_configs.json")); err == nil {
		var cfg struct {
			Configs []struct {
				SSHHost string `json:"sshHost"`
			} `json:"configs"`
		}
		if json.Unmarshal(data, &cfg) == nil {
			for _, c := range cfg.Configs {
				add(c.SSHHost)
			}
		}
	}

	paths, _ := filepath.Glob(filepath.Join(sessionsRoot, "*", "*", "local_*.json"))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var entry struct {
			SSHConfig json.RawMessage `json:"sshConfig"`
		}
		if err := json.Unmarshal(data, &entry); err != nil {
			log.Printf("Skip ssh host of %s: %v", p, err)
			continue
		}
		var cfg struct {
			SSHHost string `json:"sshHost"`
		}
		if json.Unmarshal(entry.SSHConfig, &cfg) == nil {
			add(cfg.SSHHost)
		}
	}
	return hosts
}

// knownSSHSessions returns the CLI session IDs of every chat entry in any
// account, and the ones the user deleted. A deleted_<x> marker is matched
// against entries by local ID, and x itself is also treated as deleted.
func knownSSHSessions(sessionsRoot string) (known, deleted map[string]bool) {
	known = map[string]bool{}
	deleted = map[string]bool{}
	cliByLocal := map[string]string{}
	var deletedLocal []string

	dirs, _ := filepath.Glob(filepath.Join(sessionsRoot, "*", "*"))
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			switch {
			case e.IsDir():
				continue
			case strings.HasPrefix(name, "deleted_"):
				deletedLocal = append(deletedLocal, strings.TrimPrefix(name, "deleted_"))
			case strings.HasPrefix(name, "local_") && strings.HasSuffix(name, ".json"):
				entry, err := readSessionIndex(filepath.Join(dir, name))
				if err != nil {
					log.Printf("Skip session index %s: %v", filepath.Join(dir, name), err)
					continue
				}
				if entry.cli == "" {
					continue
				}
				known[entry.cli] = true
				cliByLocal[entry.localID()] = entry.cli
			}
		}
	}
	for _, local := range deletedLocal {
		deleted[local] = true
		if cli := cliByLocal[local]; cli != "" {
			deleted[cli] = true
		}
	}
	return known, deleted
}

// fetchSSHChats scans every SSH host in parallel for transcripts the desktop
// does not list yet. Failing hosts are logged and skipped. A session seen on
// several hosts comes from the first host in sshHosts order.
func fetchSSHChats(ctx context.Context, userData, sessionsRoot string) []sshChat {
	hosts := sshHosts(userData, sessionsRoot)
	if len(hosts) == 0 {
		return nil
	}
	known, deleted := knownSSHSessions(sessionsRoot)
	ids := make([]string, 0, len(known))
	for id := range known {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	idsJSON, err := json.Marshal(ids)
	if err != nil {
		return nil
	}
	stdin := "KNOWN = set(" + string(idsJSON) + ")\n" + sshRemoteScript

	results := make([][]sshChat, len(hosts))
	var wg sync.WaitGroup
	for i, host := range hosts {
		wg.Add(1)
		go func(i int, host string) {
			defer wg.Done()
			hctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			out, err := runSSH(hctx, host, stdin)
			if err != nil {
				log.Printf("ssh %s: %v", host, err)
				return
			}
			results[i] = parseSSHChats(host, out)
		}(i, host)
	}
	wg.Wait()

	seen := map[string]bool{}
	var chats []sshChat
	for _, group := range results {
		for _, c := range group {
			if c.Entrypoint != "claude-desktop" || c.Session == "" ||
				known[c.Session] || deleted[c.Session] || seen[c.Session] {
				continue
			}
			seen[c.Session] = true
			chats = append(chats, c)
		}
	}
	sort.Slice(chats, func(i, j int) bool { return chats[i].Session < chats[j].Session })
	return chats
}

// parseSSHChats reads the JSON lines printed by sshRemoteScript.
func parseSSHChats(host string, out []byte) []sshChat {
	var chats []sshChat
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var c sshChat
		if err := json.Unmarshal(line, &c); err != nil {
			log.Printf("ssh %s: skip bad line: %v", host, err)
			continue
		}
		c.Host = host
		chats = append(chats, c)
	}
	if err := sc.Err(); err != nil {
		log.Printf("ssh %s: read output: %v", host, err)
	}
	return chats
}

// saveSSHCache stores the fetched chats so the list survives an offline start.
func saveSSHCache(path string, chats []sshChat) error {
	if chats == nil {
		chats = []sshChat{}
	}
	data, err := json.Marshal(chats)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// loadSSHCache reads the chats saved by saveSSHCache. A missing file is not an error.
func loadSSHCache(path string) ([]sshChat, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var chats []sshChat
	if err := json.Unmarshal(data, &chats); err != nil {
		return nil, fmt.Errorf("read ssh cache %s: %w", path, err)
	}
	return chats, nil
}

// writeSSHChats writes a chat entry for each SSH chat into the account's
// folder. Chats that are known or deleted, or whose entry already exists, are
// skipped, and no existing file is overwritten. Returns how many entries it wrote.
func writeSSHChats(sessionsRoot, account, org string, chats []sshChat, now time.Time) (int, error) {
	target := filepath.Join(sessionsRoot, account, org)
	if err := os.MkdirAll(target, 0o700); err != nil {
		return 0, err
	}
	// The cache may be stale, so check again right before writing.
	known, deleted := knownSSHSessions(sessionsRoot)

	written := 0
	for _, c := range chats {
		if !safeSSHSession(c.Session) {
			log.Printf("Skip ssh chat %q: not a session ID", c.Session)
			continue
		}
		if known[c.Session] || deleted[c.Session] {
			continue
		}
		if c.Cwd == "" {
			log.Printf("Skip ssh chat %s: no working directory", c.Session)
			continue
		}
		first, err := time.Parse(time.RFC3339Nano, c.First)
		if err != nil {
			log.Printf("Skip ssh chat %s: bad first timestamp: %v", c.Session, err)
			continue
		}
		path := filepath.Join(target, "local_"+c.Session+".json")
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return written, err
		}

		rec := sshRecord(c, first, now)
		data, err := json.MarshalIndent(rec, "", "\t")
		if err != nil {
			return written, err
		}
		if err := writeFileAtomic(path, data); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// safeSSHSession reports whether id, which comes from the SSH host, can name
// an entry file without leaving the account's folder.
func safeSSHSession(id string) bool {
	if id == "" || id == "." || id == ".." || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// sshRecord builds the chat entry the desktop reads for an SSH chat.
func sshRecord(c sshChat, first time.Time, now time.Time) map[string]any {
	created := first.UnixMilli()
	last := first
	if t, err := time.Parse(time.RFC3339Nano, c.Last); err == nil {
		last = t
	}

	title := c.Title
	titleSource := "user"
	if title == "" {
		titleSource = "auto"
		title = c.AITitle
	}
	if title == "" {
		title = "SSH chat " + c.Session[:min(8, len(c.Session))]
	}

	rec := map[string]any{
		"sessionId":               "local_" + c.Session,
		"cliSessionId":            c.Session,
		"cwd":                     c.Cwd,
		"originCwd":               c.Cwd,
		"createdAt":               created,
		"lastActivityAt":          last.UnixMilli(),
		"title":                   title,
		"titleSource":             titleSource,
		"isArchived":              now.Sub(last) > 7*24*time.Hour,
		"sshConfig":               map[string]string{"sshHost": c.Host},
		"sshRemoteTranscriptPath": c.Path,
	}
	// Worktree chats keep the repository as their origin.
	const marker = "/.claude/worktrees/"
	if i := strings.LastIndex(c.Cwd, marker); i >= 0 {
		if name := c.Cwd[i+len(marker):]; name != "" && !strings.Contains(name, "/") {
			rec["originCwd"] = c.Cwd[:i]
			rec["worktreePath"] = c.Cwd
			rec["worktreeName"] = name
		}
	}
	if c.Branch != "" {
		rec["branch"] = c.Branch
	}
	// Claude marks messages it made up itself with "<synthetic>", not a model.
	if c.Model != "" && !strings.HasPrefix(c.Model, "<") {
		rec["model"] = c.Model
	}
	return rec
}
