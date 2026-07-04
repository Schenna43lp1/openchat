package main

import "testing"

// By default the application should keep using the legacy JSON location.
func TestResolveUsersStorePathDefault(t *testing.T) {
	t.Setenv("OPENCHAT_USERS_FILE", "")
	if got := resolveUsersStorePath(); got != usersFile {
		t.Fatalf("resolveUsersStorePath() = %q, want %q", got, usersFile)
	}
}

// OPENCHAT_USERS_FILE must override the default store path.
func TestResolveUsersStorePathFromEnv(t *testing.T) {
	t.Setenv("OPENCHAT_USERS_FILE", "data/users.sqlite")
	if got := resolveUsersStorePath(); got != "data/users.sqlite" {
		t.Fatalf("resolveUsersStorePath() = %q, want %q", got, "data/users.sqlite")
	}
}
