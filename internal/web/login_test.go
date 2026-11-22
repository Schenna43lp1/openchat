// Tests for login, logout, cookies and their error messages.
package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Schenna43lp1/openchat/internal/store"
)

func TestTooLongPasswordIsExplained(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	_, body := a.post("/login", "", url.Values{"mode": {"register"}, "username": {"alice"}, "password": {strings.Repeat("x", 100)}})
	if !strings.Contains(body, "72 Byte") {
		t.Fatal("limit is not explained")
	}
}

func TestUnexpectedErrorIsNotReportedAsWrongPassword(t *testing.T) {
	if got, known := errorMessage(errors.New("boom")); known || strings.Contains(got, "Passwort ist falsch") {
		t.Fatalf("message = %q", got)
	}
}

func TestClosedRegistration(t *testing.T) {
	a := startTestAppWith(t, t.TempDir(), Options{})
	res, _ := a.post("/login", "", url.Values{"mode": {"register"}, "username": {"alice"}, "password": {testPassword}})
	if res.StatusCode != http.StatusForbidden || len(a.users.List()) != 0 {
		t.Fatalf("status = %d, users = %v", res.StatusCode, a.users.List())
	}
	if _, page := a.post("/login", "", url.Values{"username": {"ghost"}, "password": {testPassword}}); strings.Contains(page, "Registrieren</button>") {
		t.Fatal("closed registration is still offered")
	}
	a.signInAdmin()
}

// sessionCookies leaves out the cookie that recognizes the browser.
func sessionCookies(res *http.Response) []*http.Cookie {
	var found []*http.Cookie
	for _, cookie := range res.Cookies() {
		if cookie.Name == cookieName {
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
			seedAccounts(t, a.users, testAccount(t, "Alice", store.RoleAdmin, false), testAccount(t, "blocked", store.RoleUser, true))
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
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
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

func TestFailedLogoutCanBeRetried(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	cookie := a.signIn("register", "alice")
	a.exec(`CREATE TRIGGER fail_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'disk unavailable'); END`)
	if res, _ := a.post("/logout", cookie, nil); res.StatusCode != http.StatusInternalServerError || len(res.Cookies()) != 0 {
		t.Fatalf("status = %d, cookies = %v", res.StatusCode, res.Cookies())
	}
}

func TestCookieAttributes(t *testing.T) {
	for _, insecure := range []bool{false, true} {
		a := startTestAppWith(t, t.TempDir(), Options{AllowRegistration: true, InsecureCookie: insecure})
		res, _ := a.post("/login", "", url.Values{"mode": {"register"}, "username": {"alice"}, "password": {testPassword}})
		cookies := sessionCookies(res)
		if len(cookies) != 1 {
			t.Fatalf("cookies = %v", cookies)
		}
		c := cookies[0]
		if c.Name != cookieName || len(c.Value) != 43 || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 24*60*60 {
			t.Fatalf("cookie = %+v", c)
		}
		if c.Secure == insecure {
			t.Fatalf("Secure = %t with InsecureCookie = %t", c.Secure, insecure)
		}
	}
}

func TestHealth(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	if res, body := a.send(http.MethodGet, "/healthz", "", nil); res.StatusCode != http.StatusOK || body != "ok\n" {
		t.Fatalf("healthy: %d %q", res.StatusCode, body)
	}
	if err := a.db.Close(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.server.Config.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed database: status = %d, want 503", rec.Code)
	}
}
