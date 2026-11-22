// Package-wide isolation from the developer's environment and data.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain points all default storage paths at a temporary directory, so no test can touch ./data.
func TestMain(m *testing.M) {
	os.Exit(runIsolated(m))
}

func runIsolated(m *testing.M) int {
	dir, err := os.MkdirTemp("", "openchat-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create test directory:", err)
		return 1
	}
	defer os.RemoveAll(dir)

	for key, value := range map[string]string{
		"OPENCHAT_USERS_FILE":        filepath.Join(dir, "users.json"),
		"OPENCHAT_CHAT_HISTORY_FILE": filepath.Join(dir, "chat-history.json"),
		"OPENCHAT_ALLOWED_ORIGINS":   "",
	} {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintln(os.Stderr, "isolate test environment:", err)
			return 1
		}
	}
	return m.Run()
}
