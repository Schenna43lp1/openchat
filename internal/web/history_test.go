// Tests for what owners see about the passwords that others issued for their account.
package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Schenna43lp1/openchat/internal/store"
)

func (a *testApp) pageWith(path, cookie string) string {
	a.t.Helper()
	res, body := a.send(http.MethodGet, path, "", map[string]string{"Cookie": cookie})
	if res.StatusCode != http.StatusOK {
		a.t.Fatalf("GET %s: status = %d", path, res.StatusCode)
	}
	return body
}

func expectParts(t *testing.T, body string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(body, part) {
			t.Errorf("page lacks %q", part)
		}
	}
}

// An admin resets, uses the password, resets again and hands that over.
func TestCoveredTakeoverIsVisibleToTheOwner(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	seedAccounts(t, a.users, testAccount(t, "victim", store.RoleUser, false))

	a.chooseOwnPassword("victim", a.resetByAdmin(admin, "victim"))
	handedOver := a.resetByAdmin(admin, "victim")

	// The owner cannot get past this page.
	owner := a.signInWith("victim", handedOver)
	body := a.pageWith(passwordPath, owner)
	expectParts(t, body, "2-mal vergeben, 1-mal eingeloest", "Admin <strong>admin</strong>", "eingeloest am", "noch nicht eingeloest", "beim Betreiber")
	if strings.Index(body, "issue-history") > strings.Index(body, `id="current"`) {
		t.Error("history has to come before the form")
	}

	_, failed := a.post(passwordPath, owner, passwordForm("wrong-password", ownPassword))
	expectParts(t, failed, "2-mal vergeben, 1-mal eingeloest")
}

func TestCoveredTakeoverOfANewAccountIsVisible(t *testing.T) {
	a := startTestAppWith(t, t.TempDir(), Options{})
	admin := a.signInAdmin()
	_, created := a.post("/admin/users/new", admin, url.Values{"username": {"newcomer"}, "confirmation": {testPassword}})
	a.chooseOwnPassword("newcomer", issuedPassword(t, created))
	handedOver := a.resetByAdmin(admin, "newcomer")

	body := a.pageWith(passwordPath, a.signInWith("newcomer", handedOver))
	expectParts(t, body, "Konto angelegt", "Passwort zurueckgesetzt", "2-mal vergeben, 1-mal eingeloest")
}

func TestManyResetsCannotPushTheEvidenceOut(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	seedAccounts(t, a.users, testAccount(t, "victim", store.RoleUser, false))
	a.chooseOwnPassword("victim", a.resetByAdmin(admin, "victim"))
	var last string
	for range store.IssueListLimit + 1 {
		last = a.resetByAdmin(admin, "victim")
	}
	expectParts(t, a.pageWith(passwordPath, a.signInWith("victim", last)), "12-mal vergeben, 1-mal eingeloest", "Die 10 neuesten")
}

func TestAdminNamedLikeTheOperatorIsShownAsAdmin(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "Betreiber", store.RoleAdmin, false), testAccount(t, "victim", store.RoleUser, false))
	issued := a.resetByAdmin(a.signIn("login", "Betreiber"), "victim")
	body := a.pageWith(passwordPath, a.signInWith("victim", issued))
	expectParts(t, body, "Admin <strong>Betreiber</strong>")
	if strings.Contains(body, "durch den Betreiber") {
		t.Fatal("an admin can pass as the operator")
	}
}

func TestOperatorResetIsShownAsSuch(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "root", store.RoleAdmin, false))
	issued := a.issue("root")
	expectParts(t, a.pageWith(passwordPath, a.signInWith("root", issued.Password)), "durch den Betreiber", "1-mal vergeben, 0-mal eingeloest")
}

func TestChatLinksToAccountSecurity(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	seedAccounts(t, a.users, testAccount(t, "victim", store.RoleUser, false), testAccount(t, "bob", store.RoleUser, false))
	victim := a.chooseOwnPassword("victim", a.resetByAdmin(admin, "victim"))

	bob := a.signIn("login", "bob")
	for _, cookie := range []string{victim, bob} {
		for _, path := range []string{"/", "/direct"} {
			body := a.pageWith(path, cookie)
			expectParts(t, body, `class="header-link" href="/account/password">Kontosicherheit</a>`)
			// Almost every account of a closed instance has a history, so a warning here would mean nothing.
			// The login from a new browser is what shows it.
			if strings.Contains(body, "Startpasswort vergeben") {
				t.Errorf("%s shows a standing warning", path)
			}
		}
	}
	// Seeing it must not make it go away, the one who sees it first may be the admin.
	for range 2 {
		expectParts(t, a.pageWith(passwordPath, victim), "1-mal vergeben, 1-mal eingeloest")
	}
	if body := a.pageWith(passwordPath, bob); strings.Contains(body, "issue-history") {
		t.Error("history shown to an account without one")
	}
}

func TestSecurityPageIsNoForcedChange(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	seedAccounts(t, a.users, testAccount(t, "victim", store.RoleUser, false), testAccount(t, "bob", store.RoleUser, false))
	issued := a.resetByAdmin(admin, "victim")

	if forced := a.pageWith(passwordPath, a.signInWith("victim", issued)); strings.Contains(forced, "Weiter zum Chat") || strings.Contains(forced, "Du musst nichts aendern") {
		t.Fatal("forced page offers a way out")
	}

	body := a.pageWith(passwordPath, a.chooseOwnPassword("victim", issued))
	expectParts(t, body, "<h1>Kontosicherheit</h1>", "Du musst nichts aendern", "Weiter zum Chat")
	last := -1
	// What happened, then the way out, then the details and the form.
	for _, part := range []string{"1-mal vergeben, 1-mal eingeloest", "beim Betreiber", "Weiter zum Chat", `class="issue-list"`, `id="current"`} {
		at := strings.Index(body, part)
		if at <= last {
			t.Fatalf("%q is out of order", part)
		}
		last = at
	}

	expectParts(t, a.pageWith(passwordPath, a.signIn("login", "bob")), "<h1>Kontosicherheit</h1>", "Weiter zum Chat")
}

func TestUnreadableHistoryIsNotShownAsEmpty(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	cookie := a.signIn("login", "alice")
	a.exec(`DROP TABLE password_issues`)
	if res, _ := a.send(http.MethodGet, passwordPath, "", map[string]string{"Cookie": cookie}); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", res.StatusCode)
	}
	// A login must not skip the page only because it cannot be read.
	if res, _ := a.post("/login", "", url.Values{"username": {"alice"}, "password": {testPassword}}); res.Header.Get("Location") != passwordPath {
		t.Errorf("login lands at %q", res.Header.Get("Location"))
	}
}

// No second reset: the admin sets the issued password again as an own one, so the owner is never forced to the page.
func TestTakeoverWithoutASecondResetIsShownAtLogin(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	seedAccounts(t, a.users, testAccount(t, "victim", store.RoleUser, false))

	issued := a.resetByAdmin(admin, "victim")
	asVictim := a.chooseOwnPassword("victim", issued)
	if res, _ := a.post(passwordPath, asVictim, passwordForm(ownPassword, issued)); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("setting the issued password again: status = %d", res.StatusCode)
	}

	res, _ := a.post("/login", "", url.Values{"username": {"victim"}, "password": {issued}})
	if got := res.Header.Get("Location"); got != passwordPath {
		t.Fatalf("owner lands at %q, want the history", got)
	}
	var session string
	var device *http.Cookie
	for _, cookie := range res.Cookies() {
		switch cookie.Name {
		case cookieName:
			session = cookie.Name + "=" + cookie.Value
		case deviceCookieName:
			device = cookie
		}
	}
	expectParts(t, a.pageWith(passwordPath, session), "1-mal vergeben, 1-mal eingeloest", "eingeloest am")

	// A browser that has seen it goes to the chat. The browser of the admin cannot do that for the owner.
	if rec := a.loginFrom(7, "victim", issued, device); rec.Header().Get("Location") != "/" {
		t.Fatalf("known browser lands at %q", rec.Header().Get("Location"))
	}
}
