// Tests for the own password and for accounts with an issued one.
package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Schenna43lp1/openchat/internal/auth"
	"github.com/Schenna43lp1/openchat/internal/store"
)

const ownPassword = "my-own-new-password"

// issue gives an existing account a password that somebody else has seen.
func (a *testApp) issue(name string) auth.Issued {
	a.t.Helper()
	issued, err := a.users.auth.OperatorReset(a.t.Context(), name)
	if err != nil {
		a.t.Fatal(err)
	}
	return issued
}

func (a *testApp) signInWith(name, password string) string {
	a.t.Helper()
	res, body := a.post("/login", "", url.Values{"username": {name}, "password": {password}})
	for _, cookie := range sessionCookies(res) {
		return cookie.Name + "=" + cookie.Value
	}
	a.t.Fatalf("login %q: status %d: %s", name, res.StatusCode, body)
	return ""
}

func passwordForm(current, next string) url.Values {
	return url.Values{"current": {current}, "password": {next}, "repeat": {next}}
}

func TestIssuedPasswordOnlyAllowsChangingIt(t *testing.T) {
	for _, role := range []store.Role{store.RoleAdmin, store.RoleModerator, store.RoleUser} {
		t.Run(string(role), func(t *testing.T) {
			a := startTestApp(t, t.TempDir())
			seedAccounts(t, a.users, testAccount(t, "member", role, false), testAccount(t, "victim", store.RoleUser, false))
			cookie := a.signInWith("member", a.issue("member").Password)
			headers := map[string]string{"Cookie": cookie}

			for _, path := range []string{"/", "/direct", "/admin/users", "/unknown", "/account/password/"} {
				res, body := a.send(http.MethodGet, path, "", headers)
				if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != passwordPath {
					t.Errorf("GET %s: %d %s", path, res.StatusCode, body)
				}
			}
			for _, path := range []string{"/ws", "/session"} {
				if res, _ := a.send(http.MethodGet, path, "", headers); res.StatusCode != http.StatusUnauthorized {
					t.Errorf("GET %s: status = %d, want 401", path, res.StatusCode)
				}
			}
			a.post(userPath(a.account("victim")), cookie, url.Values{"action": {"ban"}})
			if victim, _ := a.users.Find("victim"); victim.Banned {
				t.Error("an issued password could ban")
			}

			res, body := a.send(http.MethodGet, passwordPath, "", headers)
			if res.StatusCode != http.StatusOK || !strings.Contains(body, "Startpasswort") || strings.Contains(body, `href="/"`) {
				t.Fatalf("password page: %d %s", res.StatusCode, body)
			}
			if res, _ := a.post("/logout", cookie, nil); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
				t.Fatalf("logout: status = %d", res.StatusCode)
			}
		})
	}
}

func TestIssuedPasswordLeadsToTheChatAfterAChange(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	issued := a.issue("alice")
	old := a.signInWith("alice", issued.Password)

	if _, body := a.post(passwordPath, old, passwordForm(issued.Password, issued.Password)); !strings.Contains(body, "unterscheiden") {
		t.Fatalf("issued password could be kept: %s", body)
	}
	res, body := a.post(passwordPath, old, passwordForm(issued.Password, ownPassword))
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != passwordPath+"?changed=1" {
		t.Fatalf("change: %d %s", res.StatusCode, body)
	}
	cookies := sessionCookies(res)
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	current := cookies[0].Name + "=" + cookies[0].Value
	if current == old || a.sessions.Active(strings.TrimPrefix(old, cookieName+"=")) {
		t.Fatal("session of the issued password survived")
	}
	a.connect(current)
}

func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	laptop, phone := a.signIn("login", "alice"), a.signIn("login", "alice")
	onPhone, _ := a.connect(phone)
	onLaptop, _ := a.connect(laptop)

	res, body := a.post(passwordPath, laptop, passwordForm(testPassword, ownPassword))
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("change: %d %s", res.StatusCode, body)
	}
	expectRevoked(t, onPhone)
	expectRevoked(t, onLaptop)
	if a.sessions.Active(strings.TrimPrefix(phone, cookieName+"=")) {
		t.Fatal("other session survived")
	}

	var session, device *http.Cookie
	for _, cookie := range res.Cookies() {
		switch cookie.Name {
		case cookieName:
			session = cookie
		case deviceCookieName:
			device = cookie
		}
	}
	if session == nil || device == nil {
		t.Fatalf("cookies = %v", res.Cookies())
	}
	a.connect(session.Name + "=" + session.Value)

	// The old device cookie ended with the old hash, so the new one has to work.
	a.exhaustGuesses("alice")
	if rec := a.loginFrom(100, "alice", ownPassword, device); rec.Code != http.StatusSeeOther {
		t.Fatalf("device after the change: status = %d", rec.Code)
	}
}

func TestPasswordChangeFeedback(t *testing.T) {
	for name, tc := range map[string]struct {
		form url.Values
		want string
	}{
		"wrong password": {passwordForm("wrong-password", ownPassword), "Dein Passwort ist falsch"},
		"same password":  {passwordForm(testPassword, testPassword), "unterscheiden"},
		"too short":      {passwordForm(testPassword, "short"), "mindestens 8 Zeichen"},
		"no match":       {url.Values{"current": {testPassword}, "password": {ownPassword}, "repeat": {ownPassword + "x"}}, "stimmen nicht ueberein"},
		"empty":          {url.Values{}, "mindestens 8 Zeichen"},
	} {
		t.Run(name, func(t *testing.T) {
			a := startTestApp(t, t.TempDir())
			seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
			before, _ := a.users.Find("alice")
			res, body := a.post(passwordPath, a.signIn("login", "alice"), tc.form)
			if res.StatusCode != http.StatusOK || !strings.Contains(body, tc.want) {
				t.Fatalf("status = %d, body = %s", res.StatusCode, body)
			}
			if after, _ := a.users.Find("alice"); after != before {
				t.Fatal("refused change was stored")
			}
			if tc.form.Get("current") != "" && strings.Contains(body, tc.form.Get("current")) {
				t.Fatal("page echoes the password")
			}
		})
	}
}

func TestPasswordIsNotTakenFromTheURL(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	before, _ := a.users.Find("alice")
	a.post(passwordPath+"?"+passwordForm(testPassword, ownPassword).Encode(), a.signIn("login", "alice"), nil)
	if after, _ := a.users.Find("alice"); after != before {
		t.Fatal("password was read from the URL")
	}
}

func TestPasswordPageNeedsALogin(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	if res, _ := a.post(passwordPath, "", passwordForm(testPassword, ownPassword)); res.Header.Get("Location") != "/login" {
		t.Fatalf("anonymous: status = %d", res.StatusCode)
	}
	if res, _ := a.send(http.MethodPost, passwordPath, "", map[string]string{"Origin": "https://evil.example"}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: status = %d", res.StatusCode)
	}
}

func TestPasswordGuessesWithASessionAreLimited(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false), testAccount(t, "bob", store.RoleUser, false))
	alice, bob := a.signIn("login", "alice"), a.signIn("login", "bob")
	for i := range confirmationBurst {
		if res, _ := a.post(passwordPath, alice, passwordForm("wrong-password", ownPassword)); res.StatusCode != http.StatusOK {
			t.Fatalf("guess %d: status = %d", i, res.StatusCode)
		}
	}
	if res, _ := a.post(passwordPath, alice, passwordForm(testPassword, ownPassword)); res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Fatalf("after the guesses: status = %d", res.StatusCode)
	}
	if res, _ := a.post(passwordPath, bob, passwordForm(testPassword, ownPassword)); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("other account: status = %d", res.StatusCode)
	}
}

func TestRightPasswordsAreLimitedForTheirCost(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	cookie := a.signIn("login", "alice")
	for i := range passwordWorkBurst {
		if res, _ := a.post(passwordPath, cookie, passwordForm(testPassword, testPassword)); res.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d", i, res.StatusCode)
		}
	}
	if res, _ := a.post(passwordPath, cookie, passwordForm(testPassword, testPassword)); res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", res.StatusCode)
	}
}

func TestEscapedPathIsNotThePasswordPage(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	cookie := a.signInWith("alice", a.issue("alice").Password)
	for _, path := range []string{"/account%2Fpassword", "/%61ccount%2Fpassword"} {
		res, _ := a.send(http.MethodGet, path, "", map[string]string{"Cookie": cookie})
		if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != passwordPath {
			t.Errorf("%s passed the gate: status = %d", path, res.StatusCode)
		}
	}
}
