package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The app lists Claude Code chats from claude-code-sessions/<account>/<org>/,
// one local_<id>.json per chat. The conversations themselves live in
// ~/.claude/projects and are shared, so making a chat visible in another
// account only needs its entry file. A deleted_<id> file marks a chat the
// user deleted; such chats are never copied back.

// syncSessions copies chat entries from every other account folder into the
// target account's folder. It adds missing chats and refreshes older copies,
// it never deletes anything. Returns how many entries it wrote.
func syncSessions(root, account, org string) (int, error) {
	target := filepath.Join(root, account, org)
	dirs, err := filepath.Glob(filepath.Join(root, "*", "*"))
	if err != nil {
		return 0, err
	}

	deleted := map[string]bool{}
	type source struct{ path, name string }
	var sources []source
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			switch {
			case strings.HasPrefix(name, "deleted_"):
				deleted[strings.TrimPrefix(name, "deleted_")] = true
			case strings.HasPrefix(name, "local_") && strings.HasSuffix(name, ".json") && !samePath(dir, target):
				sources = append(sources, source{filepath.Join(dir, name), name})
			}
		}
	}

	if err := os.MkdirAll(target, 0o700); err != nil {
		return 0, err
	}
	written := 0
	for _, s := range sources {
		id := strings.TrimSuffix(strings.TrimPrefix(s.name, "local_"), ".json")
		if deleted[id] {
			continue
		}
		dst := filepath.Join(target, s.name)
		if !newer(s.path, dst) {
			continue
		}
		if err := copyFile(s.path, dst); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// newer reports whether chat entry a has later activity than b, or b is missing.
func newer(a, b string) bool {
	tb, err := lastActivity(b)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	ta, errA := lastActivity(a)
	return errA == nil && err == nil && ta > tb
}

func lastActivity(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var entry struct {
		LastActivityAt int64 `json:"lastActivityAt"`
	}
	err = json.Unmarshal(data, &entry)
	return entry.LastActivityAt, err
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
