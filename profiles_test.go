package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPruneBackupsKeepsNewestTen(t *testing.T) {
	root := t.TempDir()
	for day := 1; day <= 12; day++ {
		name := fmt.Sprintf("202611%02d-120000", day)
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(file, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	pruneBackups(root)

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	want := []string{
		"20261103-120000", "20261104-120000", "20261105-120000",
		"20261106-120000", "20261107-120000", "20261108-120000",
		"20261109-120000", "20261110-120000", "20261111-120000",
		"20261112-120000",
	}
	if len(dirs) != len(want) {
		t.Fatalf("kept %d backup folders, want %d: %v", len(dirs), len(want), dirs)
	}
	for i, name := range want {
		if dirs[i] != name {
			t.Errorf("backup %d = %q, want %q", i, dirs[i], name)
		}
	}
	for _, gone := range []string{"20261101-120000", "20261102-120000"} {
		if _, err := os.Stat(filepath.Join(root, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("oldest backup %s still exists (err=%v)", gone, err)
		}
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("plain file was removed: %v", err)
	}
	if string(data) != "keep me" {
		t.Errorf("plain file content = %q, want %q", data, "keep me")
	}
}
