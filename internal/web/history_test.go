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

func TestChatPointsToTheHistory(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	seedAccounts(t, a.users, testAccount(t, "victim", store.RoleUser, false), testAccount(t, "bob", store.RoleUser, false))
	victim := a.chooseOwnPassword("victim", a.resetByAdmin(admin, "victim"))

	for _, path := range []string{"/", "/direct"} {
		expectParts(t, a.pageWith(path, victim), "account-notice", "Startpasswort vergeben")
		// Seeing it must not make it go away, the one who sees it first may be the admin.
		expectParts(t, a.pageWith(path, victim), "account-notice")
	}
	expectParts(t, a.pageWith(passwordPath, victim), "1-mal vergeben, 1-mal eingeloest")

	bob := a.signIn("login", "bob")
	for _, path := range []string{"/", passwordPath} {
		if body := a.pageWith(path, bob); strings.Contains(body, "account-notice") || strings.Contains(body, "issue-history") {
			t.Errorf("%s shows a history to an account without one", path)
		}
	}
}

func TestUnreadableHistoryIsNotShownAsEmpty(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	cookie := a.signIn("login", "alice")
	a.exec(`DROP TABLE password_issues`)
	for _, path := range []string{"/", passwordPath} {
		if res, _ := a.send(http.MethodGet, path, "", map[string]string{"Cookie": cookie}); res.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", path, res.StatusCode)
		}
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
