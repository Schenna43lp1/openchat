// Tests for the startup write check.
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureWritableCreatesDirectoryAndLeavesNoFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	if err := ensureWritable(filepath.Join(dir, "users.json")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %v, err = %v, want an empty directory", entries, err)
	}
}

func TestEnsureWritableKeepsExistingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureWritable(path); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "existing" {
		t.Fatalf("content = %q, err = %v", raw, err)
	}
}

func TestEnsureWritableRejectsUnusablePaths(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	readOnly := filepath.Join(dir, "read-only")
	if err := os.WriteFile(readOnly, nil, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o600) })

	cases := map[string]string{"parent is a file": filepath.Join(file, "users.json"), "target is a directory": dir}
	if os.Geteuid() != 0 { // root ignores file permissions
		cases["read-only file"] = readOnly
	}
	for name, path := range cases {
		if ensureWritable(path) == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
