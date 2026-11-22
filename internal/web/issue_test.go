// Tests for creating accounts and resetting passwords as an admin.
package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/Schenna43lp1/openchat/internal/auth"
	"github.com/Schenna43lp1/openchat/internal/chat"
	"github.com/Schenna43lp1/openchat/internal/store"
)

var issuedPattern = regexp.MustCompile(`id="issuedPassword">([^<]+)<`)

func issuedPassword(t *testing.T, body string) string {
	t.Helper()
	match := issuedPattern.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("no issued password in %s", body)
	}
	return match[1]
}

func (a *testApp) account(name string) store.User {
	a.t.Helper()
	user, ok := a.users.Find(name)
	if !ok {
		a.t.Fatalf("no account %q", name)
	}
	return user
}

func resetPath(u store.User) string {
	return fmt.Sprintf("/admin/users/%d/password", u.ID)
}

func resetForm(target store.User) url.Values {
	return url.Values{"confirmation": {testPassword}, "state": {auth.ResetState(target)}}
}

func (a *testApp) resetByAdmin(admin, name string) string {
	a.t.Helper()
	target := a.account(name)
	res, body := a.post(resetPath(target), admin, resetForm(target))
	if res.StatusCode != http.StatusOK {
		a.t.Fatalf("reset: %d %s", res.StatusCode, body)
	}
	return issuedPassword(a.t, body)
}

// chooseOwnPassword signs in with an issued password, changes it and returns the new session.
func (a *testApp) chooseOwnPassword(name, issued string) string {
	a.t.Helper()
	res, body := a.post(passwordPath, a.signInWith(name, issued), passwordForm(issued, ownPassword))
	for _, cookie := range sessionCookies(res) {
		return cookie.Name + "=" + cookie.Value
	}
	a.t.Fatalf("change: %d %s", res.StatusCode, body)
	return ""
}

func TestClosedInstanceGetsASecondUser(t *testing.T) {
	a := startTestAppWith(t, t.TempDir(), Options{})
	admin := a.signInAdmin()
	res, body := a.post("/admin/users/new", admin, url.Values{"username": {"Bob"}, "confirmation": {testPassword}})
	if res.StatusCode != http.StatusOK || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("create: %d %s", res.StatusCode, body)
	}
	issued := issuedPassword(t, body)
	if u := a.account("bob"); u.Role != store.RoleUser || !u.MustChangePassword {
		t.Fatalf("account = %+v", u)
	}
	if _, page := a.send(http.MethodGet, "/admin/users", "", map[string]string{"Cookie": admin}); strings.Contains(page, issued) || !strings.Contains(page, "Startpasswort") {
		t.Fatal("the list must show the state and never the password")
	}

	onAdmin, _ := a.connect(admin)
	onBob, _ := a.connect(a.chooseOwnPassword("bob", issued))
	sendRaw(t, onBob, `{"message":"hello from bob"}`)
	readUntil(t, onAdmin, func(e chat.Event) bool { return e.Message == "hello from bob" })

	if _, again := a.post("/admin/users/new", admin, url.Values{"username": {"Bob"}, "confirmation": {testPassword}}); issuedPattern.MatchString(again) || !strings.Contains(again, "bereits vergeben") {
		t.Fatal("sending the form again must not issue another password")
	}
}

func TestIssueRoutePermissions(t *testing.T) {
	for _, tc := range []struct {
		actor string
		want  int
	}{
		{"admin", http.StatusOK},
		{"moderator", http.StatusForbidden},
		{"user", http.StatusForbidden},
		{"banned-admin", http.StatusSeeOther},
		{"anonymous", http.StatusSeeOther},
		{"expired", http.StatusSeeOther},
	} {
		t.Run(tc.actor, func(t *testing.T) {
			f := newAccessFixture(t)
			target := f.account("other-user")
			before := f.users.List()
			for _, path := range []string{"/admin/users/new", resetPath(target)} {
				if rec := f.request(http.MethodGet, path, tc.actor, nil); rec.Code != tc.want {
					t.Errorf("GET %s: status = %d, want %d", path, rec.Code, tc.want)
				}
			}
			if tc.actor == "admin" {
				return
			}
			create := f.request(http.MethodPost, "/admin/users/new", tc.actor, url.Values{"username": {"newcomer"}, "confirmation": {testPassword}})
			reset := f.request(http.MethodPost, resetPath(target), tc.actor, resetForm(target))
			if create.Code != tc.want || reset.Code != tc.want {
				t.Errorf("POST: %d and %d, want %d", create.Code, reset.Code, tc.want)
			}
			if after := f.users.List(); len(after) != len(before) || f.account("other-user") != target {
				t.Fatal("refused request had an effect")
			}
		})
	}
}

func TestNoWayToResetAdminsInTheApp(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "admin", store.RoleAdmin, false),
		testAccount(t, "other", store.RoleAdmin, false), testAccount(t, "banned", store.RoleAdmin, true))
	admin := a.signIn("login", "admin")

	expectBlocked := func(step, name string) {
		t.Helper()
		target := a.account(name)
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			res, body := a.send(method, resetPath(target), resetForm(target).Encode(), map[string]string{"Cookie": admin})
			if res.StatusCode != http.StatusForbidden || !strings.Contains(body, "Betreiber") || strings.Contains(body, `name="confirmation"`) {
				t.Errorf("%s, %s: %d %s", step, method, res.StatusCode, body)
			}
		}
		if a.account(name) != target {
			t.Fatalf("%s: account was changed", step)
		}
		if _, list := a.send(http.MethodGet, userPath(target), "", map[string]string{"Cookie": admin}); strings.Contains(list, resetPath(target)) {
			t.Errorf("%s: reset is offered", step)
		}
	}
	expectBlocked("self", "admin")
	expectBlocked("other admin", "other")
	expectBlocked("banned admin", "banned")
	for _, role := range []string{"moderator", "user"} {
		other := a.account("other")
		a.post(userPath(other), admin, url.Values{"action": {"set_role"}, "from": {string(other.Role)}, "role": {role}, "confirmation": {testPassword}})
		if string(a.account("other").Role) != role {
			t.Fatalf("setup: role change to %s failed", role)
		}
		expectBlocked("demoted to "+role, "other")
	}
}

func TestResetHidesOlderDirectMessages(t *testing.T) {
	dir := t.TempDir()
	a := startTestApp(t, dir)
	admin := a.signInAdmin()
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false), testAccount(t, "bob", store.RoleUser, false))
	bob, alice := a.signIn("login", "bob"), a.signIn("login", "alice")
	onBob, _ := a.connect(bob)
	onAlice, _ := a.connect(alice)
	for _, message := range []struct{ to, text string }{{"alice", "old to alice"}, {"", "old public"}} {
		frame := fmt.Sprintf(`{"message":%q}`, message.text)
		if message.to != "" {
			frame = fmt.Sprintf(`{"message":%q,"to":%q}`, message.text, message.to)
		}
		sendRaw(t, onBob, frame)
		readUntil(t, onAlice, func(e chat.Event) bool { return e.Message == message.text })
	}
	sendRaw(t, onAlice, `{"message":"old from alice","to":"bob"}`)
	readUntil(t, onBob, func(e chat.Event) bool { return e.Message == "old from alice" })

	issued := a.resetByAdmin(admin, "alice")
	expectRevoked(t, onAlice)
	alice = a.chooseOwnPassword("alice", issued)

	expectHidden := func(step string, history []chat.Message) {
		t.Helper()
		if containsText(history, "old to alice") || containsText(history, "old from alice") {
			t.Fatalf("%s: hidden message is visible", step)
		}
		if !containsText(history, "old public") {
			t.Fatalf("%s: public message is gone", step)
		}
	}
	conn, history := a.connect(alice)
	expectHidden("first connection", history)
	sendRaw(t, onBob, `{"message":"new to alice","to":"alice"}`)
	readUntil(t, conn, func(e chat.Event) bool { return e.Message == "new to alice" })

	_, history = a.connect(alice)
	expectHidden("reconnect", history)
	if !containsText(history, "new to alice") {
		t.Fatal("new message is missing")
	}
	a.stop()

	b := startTestApp(t, dir)
	_, history = b.connect(alice)
	expectHidden("restart", history)
	if _, theirs := b.connect(bob); !containsText(theirs, "old to alice") || !containsText(theirs, "old from alice") {
		t.Fatal("the other side lost messages")
	}
}

func TestResetFormSentAgainDoesNothing(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	target := a.account("alice")
	form := resetForm(target)
	_, body := a.post(resetPath(target), admin, form)
	issued := issuedPassword(t, body)
	after := a.account("alice")

	if _, body := a.post(resetPath(target), admin, form); !strings.Contains(body, "inzwischen geaendert") || issuedPattern.MatchString(body) {
		t.Fatalf("second submit: %s", body)
	}
	if a.account("alice") != after {
		t.Fatal("second submit reset again")
	}
	a.signInWith("alice", issued)
}

func TestResetPageDoesNotRevealTheHash(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	target := a.account("alice")
	res, body := a.send(http.MethodGet, resetPath(target), "", map[string]string{"Cookie": admin})
	if res.StatusCode != http.StatusOK || strings.Contains(body, target.PasswordHash) || !strings.Contains(body, "dauerhaft ausgeblendet") {
		t.Fatalf("page: %d %s", res.StatusCode, body)
	}
}

func TestIssueRequiresTheAdminPassword(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	target := a.account("alice")
	if _, body := a.post(resetPath(target), admin, url.Values{"state": {auth.ResetState(target)}}); !strings.Contains(body, "Dein Passwort ist falsch") {
		t.Fatalf("reset without confirmation: %s", body)
	}
	wrong := url.Values{"username": {"newcomer"}, "confirmation": {"wrong-password"}}
	for i := range confirmationBurst - 1 {
		if res, _ := a.post("/admin/users/new", admin, wrong); res.StatusCode != http.StatusOK {
			t.Fatalf("guess %d: status = %d", i, res.StatusCode)
		}
	}
	// Both forms share the budget of the admin.
	if res, _ := a.post(resetPath(target), admin, resetForm(target)); res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", res.StatusCode)
	}
	if _, created := a.users.Find("newcomer"); created || a.account("alice") != target {
		t.Fatal("unconfirmed request had an effect")
	}
}

func TestConfirmationIsNotTakenFromTheURL(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	target := a.account("alice")
	a.post(resetPath(target)+"?"+resetForm(target).Encode(), admin, nil)
	if a.account("alice") != target {
		t.Fatal("confirmation was read from the URL")
	}
}

func TestResetClearsTheBudgetsOfTheAccount(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	alice := a.signIn("login", "alice")
	for range confirmationBurst {
		a.post(passwordPath, alice, passwordForm("wrong-password", ownPassword))
	}
	a.exhaustGuesses("alice")
	issued := a.resetByAdmin(admin, "alice")
	if rec := a.loginFrom(100, "alice", issued); rec.Code != http.StatusSeeOther {
		t.Fatalf("login with the issued password: status = %d", rec.Code)
	}
	a.chooseOwnPassword("alice", issued)
}

func TestUnknownResetTargets(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	for _, id := range []string{"999", "0", "-1", "abc", "99999999999999999999"} {
		if res, _ := a.post("/admin/users/"+id+"/password", admin, url.Values{"confirmation": {testPassword}}); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", id, res.StatusCode)
		}
	}
}

func TestServerLogHasNoPasswords(t *testing.T) {
	var log lockedBuffer
	a := startTestAppWith(t, t.TempDir(), Options{Logger: slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	admin := a.signInAdmin()
	_, body := a.post("/admin/users/new", admin, url.Values{"username": {"bob"}, "confirmation": {testPassword}})
	created := issuedPassword(t, body)
	a.chooseOwnPassword("bob", created)
	again := a.resetByAdmin(admin, "bob")
	a.stop() // the buffer is only locked for writing
	for _, secret := range []string{testPassword, ownPassword, created, again} {
		if strings.Contains(log.String(), secret) {
			t.Fatal("a password is in the server log")
		}
	}
}
