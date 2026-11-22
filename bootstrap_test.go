// Tests for the admin bootstrap and the registration switch.
package main

import (
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func envMap(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestConfigDefaultsAreClosed(t *testing.T) {
	cfg, err := loadConfig(envMap(nil))
	if err != nil || cfg.allowRegistration || cfg.adminUsername != "" {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
}

func TestConfigRejectsInvalidValues(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"typo":          {envAllowRegistration: "opne"},
		"name only":     {envAdminUsername: "root"},
		"password only": {envAdminPassword: testPassword},
	} {
		if _, err := loadConfig(envMap(env)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestEnsureAdmin(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing []authUser
		username string
		password string
		want     error
		admin    bool // whether username is an admin afterwards
	}{
		{"fresh instance", nil, "root", testPassword, nil, true},
		{"own account", []authUser{testAccount(t, "root", RoleUser, false)}, "ROOT", testPassword, nil, true},
		{"squatted name", []authUser{testAccount(t, "root", RoleUser, false)}, "root", "operator-password", errAdminConflict, false},
		{"banned account", []authUser{testAccount(t, "root", RoleUser, true)}, "root", testPassword, errAdminConflict, false},
		{"admin exists", []authUser{testAccount(t, "boss", RoleAdmin, false)}, "root", testPassword, nil, false},
		{"only banned admin", []authUser{testAccount(t, "boss", RoleAdmin, true)}, "root", testPassword, nil, true},
		{"invalid name", nil, "r", testPassword, errInvalidUsername, false},
		{"invalid password", nil, "root", "short", errInvalidPassword, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "users.json"))
			seedAccounts(t, store, tc.existing...)
			if err := store.EnsureAdmin(tc.username, tc.password); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if user, _ := store.Find(tc.username); (user.Role == RoleAdmin) != tc.admin {
				t.Fatalf("account = %+v, want admin = %t", user, tc.admin)
			}
		})
	}
}

func TestClosedRegistration(t *testing.T) {
	a := startTestAppWith(t, t.TempDir(), config{})
	res, _ := a.post("/login", "", url.Values{"mode": {"register"}, "username": {"alice"}, "password": {testPassword}})
	if res.StatusCode != http.StatusForbidden || len(a.users.List()) != 0 {
		t.Fatalf("status = %d, users = %v", res.StatusCode, a.users.List())
	}
	if _, page := a.post("/login", "", url.Values{"username": {"ghost"}, "password": {testPassword}}); strings.Contains(page, "Registrieren</button>") {
		t.Fatal("closed registration is still offered")
	}
	if cookie := a.signInAdmin(); cookie == "" {
		t.Fatal("bootstrap admin cannot sign in")
	}
}
