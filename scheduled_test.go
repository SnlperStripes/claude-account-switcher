package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tasksFile(root, account, org string) string {
	return filepath.Join(root, account, org, "scheduled-tasks.json")
}

func tasksByID(t *testing.T, path string) (map[string]json.RawMessage, map[string]map[string]any) {
	t.Helper()
	var file map[string]json.RawMessage
	if err := json.Unmarshal([]byte(read(t, path)), &file); err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	if err := json.Unmarshal(file["scheduledTasks"], &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, task := range list {
		out[task["id"].(string)] = task
	}
	return file, out
}

func TestSyncTasksSharesOneList(t *testing.T) {
	root := t.TempDir()
	seen := filepath.Join(t.TempDir(), "tasks-seen.json")
	// a is the account the user just left: its edit (enabled false) wins,
	// b ran the slot later, so its run stamps win.
	write(t, tasksFile(root, "a", "oa"), `{"scheduledTasks":[
		{"id":"daily","displayName":"Daily","cronExpression":"10 8 * * *","enabled":false,"filePath":"x","createdAt":1,
		 "lastRunAt":"2026-11-14T07:10:05.000Z","lastScheduledFor":"2026-11-14T07:10:00.000Z"}],"recordedSkips":{}}`)
	write(t, tasksFile(root, "b", "ob"), `{"scheduledTasks":[
		{"id":"daily","displayName":"Daily","cronExpression":"10 8 * * *","enabled":true,"filePath":"x","createdAt":1,
		 "lastRunAt":"2026-11-15T07:10:05.000Z","lastScheduledFor":"2026-11-15T07:10:00.000Z"},
		{"id":"weekly","displayName":"Weekly","cronExpression":"0 9 * * 1","enabled":true,"filePath":"y","createdAt":2}],"recordedSkips":{"own":[]}}`)

	n, err := syncTasks(root, "a", "oa", "c", "oc", seen)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("wrote %d tasks into a new account, want 2", n)
	}
	_, got := tasksByID(t, tasksFile(root, "c", "oc"))
	daily := got["daily"]
	if daily["enabled"] != false {
		t.Fatal("the edit of the account just left was lost")
	}
	if daily["lastScheduledFor"] != "2026-11-15T07:10:00.000Z" || daily["lastRunAt"] != "2026-11-15T07:10:05.000Z" {
		t.Fatalf("today's run in b would run again: %v", daily)
	}
	if got["weekly"] == nil {
		t.Fatal("weekly of b not shared")
	}

	// b gets a's edit and keeps its own other keys.
	if n, err := syncTasks(root, "a", "oa", "b", "ob", seen); err != nil || n != 1 {
		t.Fatalf("sync into b wrote %d (%v), want 1", n, err)
	}
	file, got := tasksByID(t, tasksFile(root, "b", "ob"))
	var skips bytes.Buffer
	if err := json.Compact(&skips, file["recordedSkips"]); err != nil || skips.String() != `{"own":[]}` {
		t.Fatal("other keys of the file were lost")
	}
	if got["daily"]["enabled"] != false || got["daily"]["lastScheduledFor"] != "2026-11-15T07:10:00.000Z" {
		t.Fatalf("b's daily: %v", got["daily"])
	}
	if n, _ := syncTasks(root, "a", "oa", "b", "ob", seen); n != 0 {
		t.Fatalf("second sync wrote %d, want 0", n)
	}
}

func TestSyncTasksKeepsRunsOfAnotherSchedule(t *testing.T) {
	root := t.TempDir()
	seen := filepath.Join(t.TempDir(), "tasks-seen.json")
	// The user moved the task to 9:00 in a. The 8:10 run in b says nothing
	// about the new slot.
	write(t, tasksFile(root, "a", "oa"), `{"scheduledTasks":[
		{"id":"daily","displayName":"Daily","cronExpression":"0 9 * * *","enabled":true,"filePath":"x","createdAt":1,
		 "missedRunScanFloor":"2026-11-15T06:00:00.000Z"}]}`)
	write(t, tasksFile(root, "b", "ob"), `{"scheduledTasks":[
		{"id":"daily","displayName":"Daily","cronExpression":"10 8 * * *","enabled":true,"filePath":"x","createdAt":1,
		 "lastRunAt":"2026-11-15T07:10:05.000Z","lastScheduledFor":"2026-11-15T07:10:00.000Z"}]}`)
	if _, err := syncTasks(root, "a", "oa", "b", "ob", seen); err != nil {
		t.Fatal(err)
	}
	_, got := tasksByID(t, tasksFile(root, "b", "ob"))
	if got["daily"]["cronExpression"] != "0 9 * * *" {
		t.Fatalf("schedule not taken from a: %v", got["daily"])
	}
	if _, ok := got["daily"]["lastScheduledFor"]; ok {
		t.Fatalf("took the run of the old schedule: %v", got["daily"])
	}
}

func TestSyncTasksMergesTheSameTaskSetUpTwice(t *testing.T) {
	root := t.TempDir()
	seen := filepath.Join(t.TempDir(), "tasks-seen.json")
	prompts := t.TempDir()
	older := filepath.Join(prompts, "jobs-2", "SKILL.md")
	newer := filepath.Join(prompts, "jobs", "SKILL.md")
	write(t, older, "old prompt")
	write(t, newer, "new prompt")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	write(t, tasksFile(root, "a", "oa"), `{"scheduledTasks":[
		{"id":"jobs","displayName":"Jobs","cronExpression":"10 8 * * *","enabled":true,"filePath":`+q(newer)+`,"createdAt":1,
		 "lastRunAt":"2026-11-13T07:10:05.000Z","lastScheduledFor":"2026-11-13T07:10:00.000Z"}]}`)
	write(t, tasksFile(root, "b", "ob"), `{"scheduledTasks":[
		{"id":"jobs-2","displayName":"Jobs","cronExpression":"10 8 * * *","enabled":true,"filePath":`+q(older)+`,"createdAt":2,
		 "lastRunAt":"2026-11-15T07:10:05.000Z","lastScheduledFor":"2026-11-15T07:10:00.000Z"},
		{"id":"other","displayName":"Other","cronExpression":"10 8 * * *","enabled":true,"filePath":"z","createdAt":3}]}`)

	if _, err := syncTasks(root, "b", "ob", "c", "oc", seen); err != nil {
		t.Fatal(err)
	}
	_, got := tasksByID(t, tasksFile(root, "c", "oc"))
	if got["jobs-2"] != nil || got["jobs"] == nil || got["other"] == nil {
		t.Fatalf("want jobs (newer prompt) and other, got %v", got)
	}
	if got["jobs"]["lastScheduledFor"] != "2026-11-15T07:10:00.000Z" {
		t.Fatalf("today's run of the copy would run again: %v", got["jobs"])
	}

	// Entering b later drops its copy too.
	if _, err := syncTasks(root, "c", "oc", "b", "ob", seen); err != nil {
		t.Fatal(err)
	}
	if _, got := tasksByID(t, tasksFile(root, "b", "ob")); got["jobs-2"] != nil || got["jobs"] == nil {
		t.Fatalf("b still has both copies: %v", got)
	}

	// Two tasks of one account are the user's own choice and both stay.
	write(t, tasksFile(root, "d", "od"), `{"scheduledTasks":[
		{"id":"twin-1","displayName":"Twin","cronExpression":"0 7 * * *","enabled":true,"filePath":"t1","createdAt":4},
		{"id":"twin-2","displayName":"Twin","cronExpression":"0 7 * * *","enabled":true,"filePath":"t2","createdAt":5}]}`)
	if _, err := syncTasks(root, "d", "od", "e", "oe", seen); err != nil {
		t.Fatal(err)
	}
	if _, got := tasksByID(t, tasksFile(root, "e", "oe")); got["twin-1"] == nil || got["twin-2"] == nil {
		t.Fatalf("twins of one account were merged: %v", got)
	}
}

func TestSyncTasksDeletesEverywhere(t *testing.T) {
	root := t.TempDir()
	seen := filepath.Join(t.TempDir(), "tasks-seen.json")
	task := `{"id":"daily","displayName":"Daily","cronExpression":"10 8 * * *","enabled":true,"filePath":"x","createdAt":1}`
	write(t, tasksFile(root, "a", "oa"), `{"scheduledTasks":[`+task+`]}`)
	if _, err := syncTasks(root, "a", "oa", "b", "ob", seen); err != nil {
		t.Fatal(err)
	}

	// Deleted in b, then switched from b to a: a loses it too.
	write(t, tasksFile(root, "b", "ob"), `{"scheduledTasks":[]}`)
	if n, err := syncTasks(root, "b", "ob", "a", "oa", seen); err != nil || n != 1 {
		t.Fatalf("sync into a wrote %d (%v), want 1", n, err)
	}
	if _, got := tasksByID(t, tasksFile(root, "a", "oa")); got["daily"] != nil {
		t.Fatal("a task deleted in b still runs in a")
	}
	if n, _ := syncTasks(root, "a", "oa", "c", "oc", seen); n != 0 {
		t.Fatalf("a deleted task reached a new account, wrote %d", n)
	}

	// Set up again with the same id, it is shared again.
	write(t, tasksFile(root, "a", "oa"), `{"scheduledTasks":[`+task+`]}`)
	if n, _ := syncTasks(root, "a", "oa", "b", "ob", seen); n != 1 {
		t.Fatalf("a task set up again was not shared, wrote %d", n)
	}
}

func TestSyncTasksLeavesAnUnreadableFile(t *testing.T) {
	root := t.TempDir()
	seen := filepath.Join(t.TempDir(), "tasks-seen.json")
	write(t, tasksFile(root, "a", "oa"), `{"scheduledTasks":[{"id":"daily","cronExpression":"10 8 * * *","enabled":true,"filePath":"x","createdAt":1}]}`)
	bad := tasksFile(root, "b", "ob")
	write(t, bad, "{not json")
	if _, err := syncTasks(root, "a", "oa", "b", "ob", seen); err == nil {
		t.Fatal("no error for an unreadable task file")
	}
	if read(t, bad) != "{not json" {
		t.Fatal("unreadable task file was overwritten")
	}
	// An unreadable other account does not stop the sync.
	if n, err := syncTasks(root, "b", "ob", "c", "oc", seen); err != nil || n != 1 {
		t.Fatalf("sync next to an unreadable file wrote %d (%v), want 1", n, err)
	}
}
