package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Scheduled tasks of Claude Code are kept per account in
// claude-code-sessions/<account>/<org>/scheduled-tasks.json. Their prompts
// are in ~/.claude/scheduled-tasks/<id>/SKILL.md, which all accounts share.
// On its start the app runs every slot a task missed since lastScheduledFor,
// so a task in two accounts ran again after a switch, and the same task set
// up in two accounts ran twice a day. syncTasks gives all accounts one list.

type schedTask = map[string]json.RawMessage

// runStamps tell which slot a task last ran for. A slot that ran in one
// account is done in all of them.
var runStamps = []string{"lastRunAt", "lastScheduledFor", "missedRunScanFloor"}

type tasksSeen struct {
	// Accounts holds the task ids each account/org had at the last sync.
	Accounts map[string][]string `json:"accounts"`
	// Deleted holds the tasks the user deleted in any account.
	Deleted []string `json:"deleted"`
}

type taskCopy struct {
	key  string
	task schedTask
}

// syncTasks writes the scheduled tasks of all accounts into the target
// account's list. from is the account Claude ran with until now: its version
// of a task has the user's latest edits and wins. The run stamps of the copies
// are merged, so a slot does not run again in the next account. A task the
// user deleted in one account goes away in all of them, and the same task set
// up again in another account is kept once. seenPath remembers the lists of
// the last sync to tell deletions apart. Returns how many tasks of the target
// it added, changed or removed.
func syncTasks(root, fromAccount, fromOrg, account, org, seenPath string) (int, error) {
	targetKey := account + "/" + org
	fromKey := fromAccount + "/" + fromOrg
	targetPath := filepath.Join(root, account, org, "scheduled-tasks.json")

	file, own, err := readTasks(targetPath)
	present := map[string]bool{}
	switch {
	case errors.Is(err, os.ErrNotExist):
		file = map[string]json.RawMessage{}
	case err != nil:
		// Leave a file the app cannot read either to the app's own recovery.
		return 0, err
	default:
		present[targetKey] = true
	}
	lists := map[string][]schedTask{targetKey: own}
	paths, err := filepath.Glob(filepath.Join(root, "*", "*", "scheduled-tasks.json"))
	if err != nil {
		return 0, err
	}
	for _, p := range paths {
		if samePath(p, targetPath) {
			continue
		}
		_, tasks, err := readTasks(p)
		if err != nil {
			log.Printf("Skip scheduled tasks %s: %v", p, err)
			continue
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			continue
		}
		key := filepath.ToSlash(rel)
		lists[key] = tasks
		present[key] = true
	}
	keys := make([]string, 0, len(lists))
	for key := range lists {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	// A task gone from a list since the last sync was deleted there, one that
	// came up in it was set up anew. Both are the user's doing.
	seen := readTasksSeen(seenPath)
	deleted := map[string]bool{}
	for _, id := range seen.Deleted {
		deleted[id] = true
	}
	for _, key := range keys {
		if prev, ok := seen.Accounts[key]; ok && present[key] {
			now := taskIDs(lists[key])
			for _, id := range prev {
				if !now[id] {
					deleted[id] = true
				}
			}
		}
	}
	for _, key := range keys {
		if prev, ok := seen.Accounts[key]; ok && present[key] {
			had := map[string]bool{}
			for _, id := range prev {
				had[id] = true
			}
			for id := range taskIDs(lists[key]) {
				if !had[id] {
					delete(deleted, id)
				}
			}
		}
	}

	copies := map[string][]taskCopy{}
	var ids []string
	for _, key := range keys {
		for _, t := range lists[key] {
			id := taskID(t)
			if id == "" {
				continue
			}
			if _, ok := copies[id]; !ok {
				ids = append(ids, id)
			}
			copies[id] = append(copies[id], taskCopy{key, t})
		}
	}
	chosen := map[string]schedTask{}
	for _, id := range ids {
		if !deleted[id] {
			chosen[id] = pickTask(copies[id], fromKey, targetKey)
		}
	}

	// The same task set up again in another account has a new id but the same
	// name and schedule. It runs once from now on, as the copy whose prompt
	// was edited last. Two such tasks in one account are the user's choice.
	groups := map[string][]string{}
	var groupKeys []string
	for _, id := range ids {
		t, ok := chosen[id]
		if !ok || stringOf(t, "displayName") == "" {
			continue
		}
		k := stringOf(t, "displayName") + "\x00" + schedule(t)
		if _, ok := groups[k]; !ok {
			groupKeys = append(groupKeys, k)
		}
		groups[k] = append(groups[k], id)
	}
	for _, k := range groupKeys {
		group := groups[k]
		if len(group) < 2 || inOneList(group, lists) {
			continue
		}
		keep := group[0]
		for _, id := range group[1:] {
			if newerTask(chosen[id], chosen[keep]) {
				keep = id
			}
		}
		for _, id := range group {
			if id == keep {
				continue
			}
			mergeRuns(chosen[keep], chosen[id])
			delete(chosen, id)
			deleted[id] = true
			log.Printf("Scheduled tasks %s and %s are the same task, only %s is kept", keep, id, keep)
		}
	}

	var out []schedTask
	placed := map[string]bool{}
	for _, t := range own {
		id := taskID(t)
		if id == "" {
			out = append(out, t)
		} else if c, ok := chosen[id]; ok && !placed[id] {
			out = append(out, c)
			placed[id] = true
		}
	}
	for _, id := range ids {
		if c, ok := chosen[id]; ok && !placed[id] {
			out = append(out, c)
			placed[id] = true
		}
	}

	before := map[string]string{}
	for _, t := range own {
		if id := taskID(t); id != "" {
			before[id] = canonTask(t)
		}
	}
	changed := 0
	for _, t := range out {
		if id := taskID(t); id != "" && before[id] != canonTask(t) {
			changed++
		}
	}
	for id := range before {
		if !placed[id] {
			changed++
		}
	}

	if changed > 0 {
		if out == nil {
			out = []schedTask{}
		}
		list, err := json.Marshal(out)
		if err != nil {
			return 0, err
		}
		file["scheduledTasks"] = list
		data, err := json.MarshalIndent(file, "", "  ")
		if err != nil {
			return 0, err
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
			return 0, err
		}
		if err := writeFileAtomic(targetPath, data); err != nil {
			return 0, err
		}
		present[targetKey] = true
	}

	if seen.Accounts == nil {
		seen.Accounts = map[string][]string{}
	}
	lists[targetKey] = out
	for _, key := range keys {
		if present[key] {
			list := []string{}
			for _, t := range lists[key] {
				if id := taskID(t); id != "" {
					list = append(list, id)
				}
			}
			seen.Accounts[key] = list
		}
	}
	seen.Deleted = []string{}
	for id := range deleted {
		seen.Deleted = append(seen.Deleted, id)
	}
	sort.Strings(seen.Deleted)
	return changed, writeTasksSeen(seenPath, seen)
}

// pickTask is the copy of the account just left, else the one that ran
// last, with the run stamps of all copies on the same schedule.
func pickTask(copies []taskCopy, fromKey, targetKey string) schedTask {
	best := copies[0]
	for _, c := range copies[1:] {
		switch {
		case best.key == fromKey:
		case c.key == fromKey:
			best = c
		case later(stringOf(c.task, "lastRunAt"), stringOf(best.task, "lastRunAt")):
			best = c
		case c.key == targetKey && !later(stringOf(best.task, "lastRunAt"), stringOf(c.task, "lastRunAt")):
			best = c
		}
	}
	out := schedTask{}
	for k, v := range best.task {
		out[k] = v
	}
	for _, c := range copies {
		mergeRuns(out, c.task)
	}
	return out
}

// mergeRuns takes the later run stamps of src. A run on another schedule
// says nothing about the slots of this one.
func mergeRuns(dst, src schedTask) {
	if schedule(dst) != schedule(src) {
		return
	}
	for _, k := range runStamps {
		if later(stringOf(src, k), stringOf(dst, k)) {
			dst[k] = src[k]
		}
	}
}

func schedule(t schedTask) string {
	return string(t["cronExpression"]) + "|" + string(t["fireAt"])
}

// newerTask tells whether a's prompt was edited after b's. Without a
// difference the one that ran last is newer.
func newerTask(a, b schedTask) bool {
	ta, tb := promptTime(a), promptTime(b)
	if !ta.Equal(tb) {
		return ta.After(tb)
	}
	return later(stringOf(a, "lastRunAt"), stringOf(b, "lastRunAt"))
}

func promptTime(t schedTask) time.Time {
	info, err := os.Stat(stringOf(t, "filePath"))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// inOneList tells whether one account has two of the ids.
func inOneList(ids []string, lists map[string][]schedTask) bool {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	for _, list := range lists {
		n := 0
		for _, t := range list {
			if want[taskID(t)] {
				n++
			}
		}
		if n > 1 {
			return true
		}
	}
	return false
}

// later tells whether the ISO time a is after b. An empty time is never later.
func later(a, b string) bool {
	if a == "" {
		return false
	}
	if b == "" {
		return true
	}
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errA == nil && errB == nil {
		return ta.After(tb)
	}
	return a > b
}

func readTasks(path string) (map[string]json.RawMessage, []schedTask, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, nil, err
	}
	if file == nil {
		return nil, nil, errors.New("scheduled tasks file is not an object")
	}
	var tasks []schedTask
	if raw, ok := file["scheduledTasks"]; ok {
		if err := json.Unmarshal(raw, &tasks); err != nil {
			return nil, nil, err
		}
	}
	return file, tasks, nil
}

func taskID(t schedTask) string { return stringOf(t, "id") }

func taskIDs(tasks []schedTask) map[string]bool {
	ids := map[string]bool{}
	for _, t := range tasks {
		if id := taskID(t); id != "" {
			ids[id] = true
		}
	}
	return ids
}

func stringOf(t schedTask, key string) string {
	var s string
	_ = json.Unmarshal(t[key], &s)
	return s
}

func canonTask(t schedTask) string {
	data, _ := json.Marshal(t)
	return string(data)
}

func readTasksSeen(path string) tasksSeen {
	var seen tasksSeen
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &seen)
	}
	return seen
}

func writeTasksSeen(path string, seen tasksSeen) error {
	data, err := json.MarshalIndent(seen, "", "  ")
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}
