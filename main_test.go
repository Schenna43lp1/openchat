package main

import "testing"

func TestResolveUsersStorePathDefault(t *testing.T) {
	t.Setenv("OPENCHAT_USERS_FILE", "")
	if got := resolveUsersStorePath(); got != usersFile {
		t.Fatalf("resolveUsersStorePath() = %q, want %q", got, usersFile)
	}
}

func TestResolveUsersStorePathFromEnv(t *testing.T) {
	t.Setenv("OPENCHAT_USERS_FILE", "data/users.sqlite")
	if got := resolveUsersStorePath(); got != "data/users.sqlite" {
		t.Fatalf("resolveUsersStorePath() = %q, want %q", got, "data/users.sqlite")
	}
}
