package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The app lists Claude Code chats from claude-code-sessions/<account>/<org>/,
// one local_<id>.json per chat. The conversations themselves live in
// ~/.claude/projects and are shared, so making a chat visible in another
// account only needs its entry file. A deleted_<id> file marks a chat the
// user deleted; such chats are never copied back.

// syncSessions copies chat entries from every other account folder into the
// target account's folder. It adds missing chats and refreshes older copies,
// it never deletes anything. Local IDs differ between accounts; cliSessionId
// identifies the actual conversation. An existing target entry keeps its own
// local ID and account-specific settings. Returns how many entries it wrote.
func syncSessions(root, account, org string) (int, error) {
	target := filepath.Join(root, account, org)
	dirs, err := filepath.Glob(filepath.Join(root, "*", "*"))
	if err != nil {
		return 0, err
	}

	deleted := map[string]bool{}
	var all, sources []sessionIndex
	targets := map[string]sessionIndex{}
	protectedTargets := map[string]bool{}
	byIdentity := map[string]string{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			log.Printf("Skip session index folder %s: %v", dir, err)
			if samePath(dir, target) {
				// Without the target's indexes, importing could overwrite its entries.
				return 0, nil
			}
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			switch {
			case strings.HasPrefix(name, "deleted_"):
				deleted[strings.TrimPrefix(name, "deleted_")] = true
			case strings.HasPrefix(name, "local_") && strings.HasSuffix(name, ".json"):
				entry, err := readSessionIndex(filepath.Join(dir, name))
				if err != nil {
					log.Printf("Skip session index %s: %v", filepath.Join(dir, name), err)
					if samePath(dir, target) {
						protectedTargets[name] = true
					}
					continue
				}
				all = append(all, entry)
				if samePath(dir, target) {
					targets[name] = entry
					byIdentity[entry.identity()] = name
				} else {
					sources = append(sources, entry)
				}
			}
		}
	}
	// A deletion marker may name another local alias of the same conversation.
	deletedCLI := map[string]bool{}
	for _, entry := range all {
		if deleted[entry.localID()] && entry.cli != "" {
			deletedCLI[entry.cli] = true
		}
	}
	// A new account imports the freshest copy once, even with many source aliases.
	sort.SliceStable(sources, func(i, j int) bool { return sources[i].activity > sources[j].activity })

	if err := os.MkdirAll(target, 0o700); err != nil {
		return 0, err
	}
	written := 0
	for _, s := range sources {
		if deleted[s.localID()] || deletedCLI[s.cli] {
			continue
		}
		if protectedTargets[s.name] {
			log.Printf("Skip session index %s: target index could not be read", s.path)
			continue
		}
		if name, exists := byIdentity[s.identity()]; exists && name != s.name {
			// Do not replace account-local settings with another account's alias.
			continue
		}
		// The app makes up a local ID once per chat, so the same file name in
		// two accounts is the same chat, copied here earlier. Its CLI session
		// can still differ: /clear moves a chat to a new one. The newer copy
		// wins either way, or the chat would stay apart in the accounts forever.
		old, exists := targets[s.name]
		if exists && s.activity <= old.activity {
			continue
		}
		if err := importEntry(s.path, filepath.Join(target, s.name), exists); err != nil {
			return written, err
		}
		written++
		if exists && old.cli != s.cli {
			log.Printf("%s moved to another Claude Code session, the newer one is kept", s.name)
			if byIdentity[old.identity()] == s.name {
				delete(byIdentity, old.identity())
			}
		}
		targets[s.name] = s
		byIdentity[s.identity()] = s.name
	}
	return written, nil
}

// A chat's Remote Control and cloud sessions live on claude.ai under the
// account that started them. Another account cannot reattach to them, so its
// app starts a new remote session, and an auto named chat then takes that
// session's made up name, like desktop-<pc>-fluttering-lovelace (issue #81).
// These keys stay with each account's own copy of the chat.
var accountKeys = []string{"bridgeSessionIds", "cloudSessionId", "remoteControlKeptServerCopyId"}

// importEntry copies another account's chat entry to dst and keeps the
// accountKeys of the entry already at dst, or leaves them out for a new one.
func importEntry(src, dst string, hasDst bool) error {
	o, err := readObject(src)
	if err != nil {
		return err
	}
	own := &object{vals: map[string]json.RawMessage{}}
	if hasDst {
		if own, err = readObject(dst); err != nil {
			return err
		}
	}
	changed := false
	for _, k := range accountKeys {
		_, inSrc := o.get(k)
		v, inOwn := own.get(k)
		changed = changed || inSrc || inOwn
		if inOwn {
			o.set(k, v)
		} else {
			o.del(k)
		}
	}
	if !changed {
		return copyFile(src, dst)
	}
	return o.write(dst)
}

type sessionIndex struct {
	path, name, cli string
	activity        int64
}

func readSessionIndex(path string) (sessionIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return sessionIndex{}, err
	}
	var entry *struct {
		CLISessionID   string `json:"cliSessionId"`
		LastActivityAt int64  `json:"lastActivityAt"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		return sessionIndex{}, fmt.Errorf("read session index %s: %w", path, err)
	}
	if entry == nil {
		return sessionIndex{}, fmt.Errorf("session index %s is not an object", path)
	}
	return sessionIndex{path: path, name: filepath.Base(path), cli: entry.CLISessionID, activity: entry.LastActivityAt}, nil
}

func (s sessionIndex) identity() string {
	if s.cli != "" {
		return "cli:" + s.cli
	}
	// Legacy entries without a CLI ID remain distinct unless their filenames match.
	return "file:" + s.name
}

func (s sessionIndex) localID() string {
	return strings.TrimSuffix(strings.TrimPrefix(s.name, "local_"), ".json")
}

func lastActivity(path string) (int64, error) {
	entry, err := readSessionIndex(path)
	return entry.activity, err
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// When the app quits it marks the chats it stopped mid-turn, and on its next
// start it sends them a message to go on. These are the keys of that mark.
var cutMarkKeys = []string{"interruptedByQuitAt", "interruptedUnseenResume", "interruptedAwaitingPerson"}

// cutChat is a chat whose turn the app stopped when it quit.
type cutChat struct {
	path, title, folder string
}

// takeCutChats marks the chats stopped mid-turn by the quit at since on
// their entry in the account's folder, where the app looks for the mark on
// its next start. Those are the chats the app marked itself and the running
// ones from runningChats: the app only marks when it quits through its own
// cleanup, which a quit from outside does not always reach. Marks left on
// other accounts' copies would continue the chat a second time after a later
// switch, so they are removed. Chats the account cannot see keep their mark.
func takeCutChats(root, account, org string, since time.Time, running map[string]bool) []cutChat {
	target := filepath.Join(root, account, org)
	paths, _ := filepath.Glob(filepath.Join(root, "*", "*", "local_*.json"))
	type entry struct {
		path string
		obj  *object
	}
	marked := map[string][]entry{}
	inTarget := map[string]entry{}
	for _, p := range paths {
		o, err := readObject(p)
		if err != nil {
			continue
		}
		cli := stringField(o, "cliSessionId")
		if cli == "" {
			continue
		}
		if samePath(filepath.Dir(p), target) {
			inTarget[cli] = entry{p, o}
		}
		var at int64
		if raw, ok := o.get("interruptedByQuitAt"); ok && json.Unmarshal(raw, &at) == nil && at >= since.UnixMilli() {
			marked[cli] = append(marked[cli], entry{p, o})
		}
	}
	for cli := range running {
		if _, ok := marked[cli]; !ok {
			marked[cli] = nil
		}
	}
	var cut []cutChat
	for cli, sources := range marked {
		dst, ok := inTarget[cli]
		if !ok {
			continue
		}
		switch {
		case len(sources) == 0:
			at, _ := json.Marshal(since.UnixMilli())
			dst.obj.set("interruptedByQuitAt", at)
			dst.obj.set("interruptedAwaitingPerson", json.RawMessage("false"))
		case !samePath(sources[0].path, dst.path):
			for _, k := range cutMarkKeys {
				if v, ok := sources[0].obj.get(k); ok {
					dst.obj.set(k, v)
				}
			}
		}
		if len(sources) == 0 || !samePath(sources[0].path, dst.path) {
			if err := dst.obj.write(dst.path); err != nil {
				log.Printf("move quit mark to %s: %v", dst.path, err)
				continue
			}
		}
		for _, src := range sources {
			if !samePath(src.path, dst.path) {
				if err := clearCutMark(src.path); err != nil {
					log.Printf("clear quit mark of %s: %v", src.path, err)
				}
			}
		}
		folder := stringField(dst.obj, "originCwd")
		if folder == "" {
			folder = stringField(dst.obj, "cwd")
		}
		if folder != "" {
			folder = filepath.Base(folder)
		}
		title := stringField(dst.obj, "title")
		if title == "" {
			title = folder
		}
		cut = append(cut, cutChat{path: dst.path, title: title, folder: folder})
	}
	sort.Slice(cut, func(i, j int) bool { return cut[i].title < cut[j].title })
	return cut
}

// A transcript written to within this long can belong to a running chat. A
// chat that sat this long on an open turn is waiting for someone anyway.
const runningWithin = 30 * time.Minute

// runningChats returns the CLI session IDs of the chats at work, from the
// transcripts the CLI keeps in projectsDir/<folder>/<id>.jsonl: the chats
// whose turn is open and the ones that ended their turn to wait for a
// background agent. The quit stops that agent too, and then nothing starts
// the chat's next turn.
func runningChats(projectsDir string, now time.Time) map[string]bool {
	running := map[string]bool{}
	paths, _ := filepath.Glob(filepath.Join(projectsDir, "*", "*.jsonl"))
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil || now.Sub(info.ModTime()) > runningWithin {
			continue
		}
		if turnOpen(readTail(p, 256<<10)) {
			running[strings.TrimSuffix(filepath.Base(p), ".jsonl")] = true
		}
	}
	metas, _ := filepath.Glob(filepath.Join(projectsDir, "*", "*", "subagents", "agent-*.meta.json"))
	for _, m := range metas {
		chat := filepath.Base(filepath.Dir(filepath.Dir(m)))
		if running[chat] {
			continue
		}
		info, err := os.Stat(strings.TrimSuffix(m, ".meta.json") + ".jsonl")
		if err != nil || now.Sub(info.ModTime()) > runningWithin {
			continue
		}
		if agentUnfinished(m) {
			running[chat] = true
		}
	}
	return running
}

// agentUnfinished reads the note the CLI keeps next to a subagent's
// transcript, which gets "finished": true when the agent is done. Only a
// background agent counts: a foreground one cannot outlive its chat's turn,
// and an older CLI never wrote "finished" at all. A note that cannot be read
// counts as finished.
func agentUnfinished(metaPath string) bool {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return false
	}
	var meta struct {
		Shape    string `json:"requestShape"`
		Finished bool   `json:"finished"`
	}
	return json.Unmarshal(data, &meta) == nil && meta.Shape == "background" && !meta.Finished
}

func readTail(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > n {
		_, _ = f.Seek(-n, io.SeekEnd)
	}
	data, _ := io.ReadAll(f)
	return data
}

// turnOpen reads a transcript's last message: the turn is open while Claude
// still has to answer a prompt or a tool result, or has called a tool.
func turnOpen(tail []byte) bool {
	lines := bytes.Split(tail, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var e struct {
			Type        string `json:"type"`
			IsSidechain bool   `json:"isSidechain"`
			Message     struct {
				StopReason *string         `json:"stop_reason"`
				Content    json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(lines[i], &e) != nil || e.IsSidechain {
			continue
		}
		switch e.Type {
		case "assistant":
			if e.Message.StopReason == nil {
				return true
			}
			switch *e.Message.StopReason {
			case "end_turn", "stop_sequence", "max_tokens", "refusal":
				return false
			}
			return true
		case "user":
			return !bytes.Contains(e.Message.Content, []byte("[Request interrupted by user"))
		}
	}
	return false
}

// clearCutMark removes a chat's quit mark, so the app leaves the chat alone.
func clearCutMark(path string) error {
	o, err := readObject(path)
	if err != nil {
		return err
	}
	for _, k := range cutMarkKeys {
		o.del(k)
	}
	return o.write(path)
}

func stringField(o *object, key string) string {
	var s string
	if raw, ok := o.get(key); ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

// lastChatWrite is when any chat entry was last written. The app rewrites
// the entry of a running Claude Code chat as it goes, so this tells whether
// someone is at work in Claude right now.
func lastChatWrite(root string) time.Time {
	var last time.Time
	paths, _ := filepath.Glob(filepath.Join(root, "*", "*", "local_*.json"))
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.ModTime().After(last) {
			last = info.ModTime()
		}
	}
	return last
}

// projectsDir is where the Claude Code CLI keeps its transcripts.
func projectsDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "projects")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}
