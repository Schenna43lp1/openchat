// Tests for the user store, sessions and the login flow.
package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// forEachBackend runs a test against every supported storage format.
func forEachBackend(t *testing.T, test func(t *testing.T, path string)) {
	t.Helper()
	for _, file := range []string{"users.json", "users.sqlite"} {
		t.Run(file, func(t *testing.T) { test(t, filepath.Join(t.TempDir(), file)) })
	}
}

func openTestStore(t *testing.T, path string) *UserStore {
	t.Helper()
	store, err := NewUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// testAccount uses the cheapest bcrypt cost to keep the suite fast under the race detector.
func testAccount(t *testing.T, name string, role UserRole, banned bool) authUser {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return authUser{Username: name, PasswordHash: string(hash), Role: role, Banned: banned, CreatedAt: "2026-01-02T03:04:05Z"}
}

func seedAccounts(t *testing.T, store *UserStore, accounts ...authUser) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, account := range accounts {
		store.users[strings.ToLower(account.Username)] = account
	}
	if err := store.saveLocked(); err != nil {
		t.Fatal(err)
	}
}

func TestDetectUserStoreFormat(t *testing.T) {
	for path, want := range map[string]userStoreFormat{
		"users.json":    userStoreJSON,
		"users":         userStoreJSON,
		"users.db":      userStoreSQLite,
		"users.sqlite":  userStoreSQLite,
		"users.SQLITE3": userStoreSQLite,
	} {
		if got := detectUserStoreFormat(path); got != want {
			t.Errorf("detectUserStoreFormat(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestRegisterPersistsAccount(t *testing.T) {
	forEachBackend(t, func(t *testing.T, path string) {
		store := openTestStore(t, path)
		seedAccounts(t, store, testAccount(t, "root", RoleAdmin, false))
		if err := store.Register("  Alice  ", testPassword); err != nil {
			t.Fatal(err)
		}
		saved, ok := store.Find("ALICE")
		if !ok || saved.Username != "Alice" || saved.Role != RoleUser || saved.Banned {
			t.Fatalf("account = %+v, found = %t", saved, ok)
		}
		if saved.PasswordHash == testPassword || bcrypt.CompareHashAndPassword([]byte(saved.PasswordHash), []byte(testPassword)) != nil {
			t.Fatal("password is not stored as a matching bcrypt hash")
		}
		if _, err := time.Parse(time.RFC3339, saved.CreatedAt); err != nil {
			t.Fatalf("creation time: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if got, _ := openTestStore(t, path).Find("alice"); got != saved {
			t.Fatalf("after reopen = %+v, want %+v", got, saved)
		}
	})
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name, username, password string
		want                     error
	}{
		{"empty name", "", testPassword, errInvalidUsername},
		{"short name", "ab", testPassword, errInvalidUsername},
		{"long name", strings.Repeat("a", 33), testPassword, errInvalidUsername},
		{"inner space", "al ice", testPassword, errInvalidUsername},
		{"line break", "al\nice", testPassword, errInvalidUsername},
		{"markup", "<alice>", testPassword, errInvalidUsername},
		{"lookalike letter", "аlice", testPassword, errInvalidUsername},
		{"short password", "alice", "1234567", errInvalidPassword},
		{"long password", "alice", strings.Repeat("x", 129), errInvalidPassword},
		{"taken name", "existing", testPassword, errUsernameTaken},
		{"taken name in other case", "EXISTING", testPassword, errUsernameTaken},
	}
	store := openTestStore(t, filepath.Join(t.TempDir(), "users.json"))
	seedAccounts(t, store, testAccount(t, "existing", RoleAdmin, false))
	before := store.List()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.Register(tc.username, tc.password); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if got := store.List(); !reflect.DeepEqual(got, before) {
				t.Fatalf("store changed to %+v", got)
			}
		})
	}
}

func TestRegisterAcceptsLimits(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "users.json"))
	for name, password := range map[string]string{
		"a.b":                   "12345678",
		strings.Repeat("z", 32): testPassword,
	} {
		if err := store.Register(name, password); err != nil {
			t.Errorf("Register(%q): %v", name, err)
		}
	}
}

func TestConcurrentRegistrationCreatesOneAccount(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "users.json"))
	results := make([]error, 4)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() { results[i] = store.Register("alice", testPassword) })
	}
	wg.Wait()

	created := 0
	for _, err := range results {
		if err == nil {
			created++
		} else if !errors.Is(err, errUsernameTaken) {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if created != 1 || len(store.List()) != 1 {
		t.Fatalf("created = %d, stored = %d, want 1", created, len(store.List()))
	}
}

func TestAuthenticate(t *testing.T) {
	forEachBackend(t, func(t *testing.T, path string) {
		store := openTestStore(t, path)
		seedAccounts(t, store, testAccount(t, "Alice", RoleAdmin, false), testAccount(t, "blocked", RoleUser, true))
		cases := []struct {
			name, username, password string
			want                     error
		}{
			{"valid", "Alice", testPassword, nil},
			{"other case and spaces", " aLICE ", testPassword, nil},
			{"wrong password", "Alice", "wrong-password", errInvalidCredentials},
			{"empty password", "Alice", "", errInvalidCredentials},
			{"password is case sensitive", "Alice", strings.ToUpper(testPassword), errInvalidCredentials},
			{"unknown user", "ghost", testPassword, errInvalidCredentials},
			{"banned", "blocked", testPassword, errUserBanned},
			{"banned with wrong password", "blocked", "wrong-password", errInvalidCredentials},
		}
		for _, tc := range cases {
			user, err := store.Authenticate(tc.username, tc.password)
			if !errors.Is(err, tc.want) {
				t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
			}
			if err != nil && user != (authUser{}) {
				t.Errorf("%s: failed login returned account data", tc.name)
			}
			if err == nil && user.Username != "Alice" {
				t.Errorf("%s: username = %q, want Alice", tc.name, user.Username)
			}
		}
	})
}

func TestListIsSortedIgnoringCase(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "users.json"))
	seedAccounts(t, store,
		testAccount(t, "delta", RoleUser, false), testAccount(t, "Charlie", RoleUser, false),
		testAccount(t, "bravo", RoleUser, false), testAccount(t, "Alpha", RoleAdmin, false))
	var names []string
	for _, user := range store.List() {
		names = append(names, user.Username)
	}
	if want := []string{"Alpha", "bravo", "Charlie", "delta"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

func TestAccountChangesPersist(t *testing.T) {
	forEachBackend(t, func(t *testing.T, path string) {
		store := openTestStore(t, path)
		seedAccounts(t, store,
			testAccount(t, "root", RoleAdmin, false),
			testAccount(t, "member", RoleUser, false),
			testAccount(t, "blocked", RoleUser, true))
		for _, err := range []error{
			store.SetRole(" MEMBER ", RoleModerator),
			store.SetBanned("Member", true),
			store.SetBanned("blocked", false),
		} {
			if err != nil {
				t.Fatal(err)
			}
		}
		want := store.List()
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened := openTestStore(t, path)
		if got := reopened.List(); !reflect.DeepEqual(got, want) {
			t.Fatalf("after reopen = %+v, want %+v", got, want)
		}
		if member, _ := reopened.Find("member"); member.Role != RoleModerator || !member.Banned {
			t.Fatalf("member = %+v", member)
		}
		if blocked, _ := reopened.Find("blocked"); blocked.Banned {
			t.Fatal("unban was not stored")
		}
	})
}

func TestAccountChangesRejectInvalidInput(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "users.json"))
	seedAccounts(t, store, testAccount(t, "root", RoleAdmin, false))
	if err := store.SetRole("root", "owner"); !errors.Is(err, errInvalidRole) {
		t.Errorf("invalid role: %v", err)
	}
	if err := store.SetRole("ghost", RoleUser); !errors.Is(err, errUnknownUser) {
		t.Errorf("role of unknown user: %v", err)
	}
	if err := store.SetBanned("ghost", true); !errors.Is(err, errUnknownUser) {
		t.Errorf("ban of unknown user: %v", err)
	}
}

func TestLastActiveAdminIsProtected(t *testing.T) {
	forEachBackend(t, func(t *testing.T, path string) {
		store := openTestStore(t, path)
		// A banned admin must not count as a remaining admin.
		seedAccounts(t, store, testAccount(t, "root", RoleAdmin, false), testAccount(t, "former", RoleAdmin, true))
		if err := store.SetRole("root", RoleModerator); !errors.Is(err, errLastAdmin) {
			t.Errorf("demotion: %v", err)
		}
		if err := store.SetBanned("root", true); !errors.Is(err, errLastAdmin) {
			t.Errorf("ban: %v", err)
		}
		if root, _ := store.Find("root"); root.Role != RoleAdmin || root.Banned {
			t.Fatalf("root changed to %+v", root)
		}
		if err := store.SetRole("root", RoleAdmin); err != nil {
			t.Errorf("unchanged role: %v", err)
		}
		if err := store.SetRole("former", RoleUser); err != nil {
			t.Errorf("demotion of banned admin: %v", err)
		}
	})
}

func TestConcurrentDemotionKeepsOneAdmin(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "users.json"))
	names := []string{"first", "second", "third", "fourth"}
	for _, name := range names {
		seedAccounts(t, store, testAccount(t, name, RoleAdmin, false))
	}
	results := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() { results[i] = store.SetRole(name, RoleUser) })
	}
	wg.Wait()

	refused := 0
	for _, err := range results {
		if errors.Is(err, errLastAdmin) {
			refused++
		} else if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if refused != 1 {
		t.Fatalf("refused demotions = %d, want 1", refused)
	}
}

func TestLoadSanitizesStoredAccounts(t *testing.T) {
	forEachBackend(t, func(t *testing.T, path string) {
		store := openTestStore(t, path)
		noName := testAccount(t, "", RoleUser, false)
		noHash := authUser{Username: "nohash", Role: RoleUser}
		seedAccounts(t, store,
			testAccount(t, "root", RoleAdmin, false), testAccount(t, "odd", "superuser", true), noName, noHash)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened := openTestStore(t, path)
		if got := reopened.List(); len(got) != 2 {
			t.Fatalf("loaded %+v, want only the two complete accounts", got)
		}
		if odd, _ := reopened.Find("odd"); odd.Role != RoleUser || !odd.Banned {
			t.Fatalf("account with unknown role = %+v", odd)
		}
	})
}

func TestEmptyJSONStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := openTestStore(t, path).List(); len(got) != 0 {
		t.Fatalf("users = %+v, want none", got)
	}
}

func TestDamagedStoreIsRejectedAndLeftUntouched(t *testing.T) {
	forEachBackend(t, func(t *testing.T, path string) {
		const damaged = "this is neither JSON nor a SQLite database"
		if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
			t.Fatal(err)
		}
		if store, err := NewUserStore(path); err == nil {
			_ = store.Close()
			t.Fatal("damaged store was accepted")
		}
		if raw, err := os.ReadFile(path); err != nil || string(raw) != damaged {
			t.Fatalf("file changed: %q, %v", raw, err)
		}
	})
}

func TestUnusableStorePathIsRejected(t *testing.T) {
	forEachBackend(t, func(t *testing.T, path string) {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if store, err := NewUserStore(path); err == nil {
			_ = store.Close()
			t.Fatal("directory was accepted as a store")
		}
	})
}

func TestIncompatibleSQLiteSchemaIsRejected(t *testing.T) {
	for name, setup := range map[string]string{
		"missing columns": `CREATE TABLE users (username TEXT)`,
		"null value": `CREATE TABLE users (username TEXT, password_hash TEXT, role TEXT, banned INTEGER, created_at TEXT);
			INSERT INTO users VALUES ('alice', 'hash', 'user', 0, NULL)`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "users.sqlite")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			_, execErr := db.Exec(setup)
			if err := errors.Join(execErr, db.Close()); err != nil {
				t.Fatal(err)
			}
			if store, err := NewUserStore(path); err == nil {
				_ = store.Close()
				t.Fatal("incompatible schema was accepted")
			}
		})
	}
}

func TestClosedSQLiteStoreReportsFailedSave(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "users.sqlite"))
	seedAccounts(t, store, testAccount(t, "root", RoleAdmin, false), testAccount(t, "member", RoleUser, false))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetBanned("member", true); !errors.Is(err, errSaveFailed) {
		t.Fatalf("error = %v, want %v", err, errSaveFailed)
	}
}

func newSessionCookie(t *testing.T, sessions *SessionManager, username string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := sessions.Create(rec, username); err != nil {
		t.Fatal(err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v, want one", cookies)
	}
	return cookies[0]
}

func expireSession(sessions *SessionManager, token string) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	current := sessions.sessions[token]
	current.ExpiresAt = time.Now().Add(-time.Second)
	sessions.sessions[token] = current
}

func TestSessionCookieAttributes(t *testing.T) {
	sessions := NewSessionManager()
	first, second := newSessionCookie(t, sessions, "alice"), newSessionCookie(t, sessions, "alice")
	if first.Value == second.Value || len(first.Value) != 43 {
		t.Fatal("tokens must be unique and encode 32 bytes")
	}
	if !first.HttpOnly || !first.Secure || first.SameSite != http.SameSiteLaxMode || first.Path != "/" {
		t.Fatalf("cookie attributes = %+v", first)
	}
	if first.Name != authCookieName || first.MaxAge != int(sessionDuration.Seconds()) {
		t.Fatalf("cookie = %+v", first)
	}
}

func TestSessionLookup(t *testing.T) {
	sessions := NewSessionManager()
	valid, expired := newSessionCookie(t, sessions, "alice"), newSessionCookie(t, sessions, "bob")
	expireSession(sessions, expired.Value)

	for _, tc := range []struct {
		name, header, want string
	}{
		{"none", "", ""},
		{"empty", authCookieName + "=", ""},
		{"unknown token", authCookieName + "=unknown", ""},
		{"other cookie name", "session=" + valid.Value, ""},
		{"expired", authCookieName + "=" + expired.Value, ""},
		{"valid", authCookieName + "=" + valid.Value, "alice"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Cookie", tc.header)
		if got, ok := sessions.Username(req); got != tc.want || ok != (tc.want != "") {
			t.Errorf("%s: Username = %q, %t", tc.name, got, ok)
		}
	}
	sessions.mu.RLock()
	defer sessions.mu.RUnlock()
	if _, ok := sessions.sessions[expired.Value]; ok {
		t.Fatal("expired session was not removed")
	}
}

// sessionCookies leaves out the cookie that recognizes the browser.
func sessionCookies(res *http.Response) []*http.Cookie {
	var found []*http.Cookie
	for _, cookie := range res.Cookies() {
		if cookie.Name == authCookieName {
			found = append(found, cookie)
		}
	}
	return found
}

func TestLoginFlow(t *testing.T) {
	cases := []struct {
		name, mode, username, password string
		status                         int
		message                        string
		user                           string // account of the new session
	}{
		{"login", "login", "alice", testPassword, http.StatusSeeOther, "", "Alice"},
		{"default mode", "", "ALICE", testPassword, http.StatusSeeOther, "", "Alice"},
		{"registration", "register", "Newcomer", testPassword, http.StatusSeeOther, "", "Newcomer"},
		{"wrong password", "login", "alice", "wrong-password", http.StatusOK, "Passwort ist falsch", ""},
		{"unknown user", "login", "ghost", testPassword, http.StatusOK, "Passwort ist falsch", ""},
		{"banned", "login", "blocked", testPassword, http.StatusOK, "wurde gesperrt", ""},
		{"taken name", "register", "alice", testPassword, http.StatusOK, "bereits vergeben", ""},
		{"invalid name", "register", "no", testPassword, http.StatusOK, "3 bis 32 Zeichen", ""},
		{"invalid password", "register", "newcomer", "short", http.StatusOK, "mindestens 8 Zeichen", ""},
		{"unknown mode", "reset", "alice", testPassword, http.StatusBadRequest, "bad request", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := startTestApp(t, t.TempDir())
			seedAccounts(t, a.users, testAccount(t, "Alice", RoleAdmin, false), testAccount(t, "blocked", RoleUser, true))
			res, body := a.post("/login", "", url.Values{"mode": {tc.mode}, "username": {tc.username}, "password": {tc.password}})
			if res.StatusCode != tc.status || !strings.Contains(body, tc.message) {
				t.Fatalf("status = %d, body = %s", res.StatusCode, body)
			}
			cookies := sessionCookies(res)
			if tc.user == "" {
				if len(cookies) != 0 {
					t.Fatal("rejected request received a cookie")
				}
				return
			}
			if res.Header.Get("Location") != "/" || len(cookies) != 1 {
				t.Fatalf("location = %q, cookies = %v", res.Header.Get("Location"), cookies)
			}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(cookies[0])
			if got, _ := a.sessions.Username(req); got != tc.user {
				t.Fatalf("session belongs to %q, want %q", got, tc.user)
			}
		})
	}
}

func TestLogoutEndsSessionAndClearsCookie(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	cookie := newSessionCookie(t, a.sessions, "alice")
	res, _ := a.post("/logout", cookie.Name+"="+cookie.Value, nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
		t.Fatalf("status = %d, location = %q", res.StatusCode, res.Header.Get("Location"))
	}
	if cleared := res.Cookies(); len(cleared) != 1 || cleared[0].Value != "" || cleared[0].MaxAge >= 0 {
		t.Fatalf("cookies = %v", cleared)
	}
	if a.sessions.Active(cookie.Value) {
		t.Fatal("session survived logout")
	}
}
