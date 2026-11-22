// Tests for the list of accounts and the page of one account.
package web

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Schenna43lp1/openchat/internal/store"
)

func TestUsersView(t *testing.T) {
	var users []store.User
	for i := range 60 {
		u := store.User{ID: int64(i + 1), Username: fmt.Sprintf("user%02d", i), Role: store.RoleUser}
		switch {
		case i == 0:
			u.Role = store.RoleAdmin
		case i < 3:
			u.Role = store.RoleModerator
		case i < 7:
			u.Banned = true
		case i < 12:
			u.MustChangePassword = true
		}
		users = append(users, u)
	}
	original := slices.Clone(users)

	for name, tc := range map[string]struct {
		query              string
		found, page, pages int
		first              string
		shown              int
	}{
		"all":              {"", 60, 1, 3, "user00", 25},
		"second page":      {"page=2", 60, 2, 3, "user25", 25},
		"last page":        {"page=3", 60, 3, 3, "user50", 10},
		"after the end":    {"page=99", 60, 3, 3, "user50", 10},
		"before the start": {"page=-4", 60, 1, 3, "user00", 25},
		"no number":        {"page=two", 60, 1, 3, "user00", 25},
		"huge":             {"page=99999999999999999999999", 60, 1, 3, "user00", 25},
		"admins":           {"filter=admin", 1, 1, 1, "user00", 1},
		"moderators":       {"filter=moderator", 2, 1, 1, "user01", 2},
		"banned":           {"filter=banned", 4, 1, 1, "user03", 4},
		"pending":          {"filter=pending", 5, 1, 1, "user07", 5},
		"unknown filter":   {"filter=owner", 60, 1, 3, "user00", 25},
		"search":           {"q=+USER1+", 10, 1, 1, "user10", 10},
		"search, filter":   {"q=user0&filter=banned", 4, 1, 1, "user03", 4},
		"nothing":          {"q=ghost&page=5", 0, 1, 1, "", 0},
	} {
		params, err := url.ParseQuery(tc.query)
		if err != nil {
			t.Fatal(err)
		}
		view := newUsersView(users[0], users, params)
		first := ""
		if len(view.Users) > 0 {
			first = view.Users[0].Username
		}
		if view.Found != tc.found || view.Page != tc.page || view.Pages != tc.pages || first != tc.first || len(view.Users) != tc.shown {
			t.Errorf("%s: found %d, page %d/%d, first %q, shown %d", name, view.Found, view.Page, view.Pages, first, len(view.Users))
		}
	}

	view := newUsersView(users[0], users, url.Values{"q": {"user0"}, "filter": {"banned"}})
	var counts []int
	for _, f := range view.Filters {
		counts = append(counts, f.Count)
	}
	if !slices.Equal(counts, []int{10, 1, 2, 3, 4}) {
		t.Errorf("counts = %v", counts)
	}
	if paged := newUsersView(users[0], users, url.Values{"q": {"user"}, "page": {"2"}}); paged.Previous != "/admin/users?q=user" || paged.Next != "/admin/users?page=3&q=user" {
		t.Errorf("links = %q, %q", paged.Previous, paged.Next)
	}
	if !slices.Equal(users, original) {
		t.Fatal("the view changed its input")
	}
}

func TestSearchIsEscaped(t *testing.T) {
	f := newAccessFixture(t)
	query := `"><script>alert(1)</script>`
	body := f.request(http.MethodGet, "/admin/users?q="+url.QueryEscape(query)+"&filter="+url.QueryEscape(query), "admin", nil).Body.String()
	if strings.Contains(body, "<script>alert(1)") {
		t.Fatal("search was rendered as HTML")
	}
}

func TestUserPagePermissions(t *testing.T) {
	for actor, want := range map[string]int{
		"admin": http.StatusOK, "moderator": http.StatusOK, "user": http.StatusForbidden,
		"banned-admin": http.StatusSeeOther, "anonymous": http.StatusSeeOther, "expired": http.StatusSeeOther,
	} {
		f := newAccessFixture(t)
		if rec := f.request(http.MethodGet, userPath(f.account("other-user")), actor, nil); rec.Code != want {
			t.Errorf("%s: status = %d, want %d", actor, rec.Code, want)
		}
	}
}

func TestUserPageOffersOnlyWhatIsAllowed(t *testing.T) {
	banButton, roleForm, resetLink := `value="ban"`, `value="set_role"`, "/password\""
	for _, tc := range []struct {
		actor, target    string
		ban, role, reset bool
	}{
		{"admin", "other-user", true, true, true},
		{"admin", "other-moderator", true, true, true},
		{"admin", "other-admin", true, true, false},
		{"admin", "admin", false, true, false},
		{"moderator", "other-user", true, false, false},
		{"moderator", "other-admin", false, false, false},
		{"moderator", "other-moderator", false, false, false},
		{"moderator", "moderator", false, false, false},
	} {
		f := newAccessFixture(t)
		body := f.request(http.MethodGet, userPath(f.account(tc.target)), tc.actor, nil).Body.String()
		reset := regexp.MustCompile(`/admin/users/\d+` + resetLink).MatchString(body)
		if strings.Contains(body, banButton) != tc.ban || strings.Contains(body, roleForm) != tc.role || reset != tc.reset {
			t.Errorf("%s on %s offers ban %t, role %t, reset %t", tc.actor, tc.target,
				strings.Contains(body, banButton), strings.Contains(body, roleForm), reset)
		}
	}
}

func TestUnknownUserPages(t *testing.T) {
	f := newAccessFixture(t)
	for _, id := range []string{"999", "0", "-1", "abc", "99999999999999999999"} {
		if rec := f.request(http.MethodGet, "/admin/users/"+id, "admin", nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", id, rec.Code)
		}
	}
}

func TestRepeatedBanStaysABan(t *testing.T) {
	f := newAccessFixture(t)
	for _, actor := range []string{"admin", "moderator", "admin"} {
		rec := f.manage(actor, "other-user", url.Values{"action": {"ban"}})
		if rec.Code != http.StatusSeeOther || !f.account("other-user").Banned {
			t.Fatalf("ban by %s: status %d, banned %t", actor, rec.Code, f.account("other-user").Banned)
		}
	}
	// What older pages sent must not switch anything.
	if rec := f.manage("admin", "other-user", url.Values{"action": {"toggle_ban"}}); rec.Code != http.StatusBadRequest || !f.account("other-user").Banned {
		t.Fatalf("toggle: status %d", rec.Code)
	}
}

func TestRoleChangeNeedsThePasswordAndACurrentPage(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(url.Values)
		path   func(string) string
		want   string
	}{
		"no password":    {change: func(v url.Values) { v.Del("confirmation") }, want: "Dein Passwort ist falsch"},
		"wrong password": {change: func(v url.Values) { v.Set("confirmation", "wrong-password") }, want: "Dein Passwort ist falsch"},
		"outdated page":  {change: func(v url.Values) { v.Set("from", "moderator") }, want: "inzwischen geaendert"},
		"no from":        {change: func(v url.Values) { v.Del("from") }, want: "inzwischen geaendert"},
		"password in the URL": {change: func(v url.Values) { v.Del("confirmation") },
			path: func(p string) string { return p + "?confirmation=" + testPassword }, want: "Dein Passwort ist falsch"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newAccessFixture(t)
			target := f.account("other-user")
			form := url.Values{"action": {"set_role"}, "from": {"user"}, "role": {"admin"}, "confirmation": {testPassword}}
			tc.change(form)
			path := userPath(target)
			if tc.path != nil {
				path = tc.path(path)
			}
			rec := f.request(http.MethodPost, path, "admin", form)
			if !strings.Contains(rec.Body.String(), tc.want) || f.account("other-user") != target {
				t.Fatalf("status %d, account %+v", rec.Code, f.account("other-user"))
			}
		})
	}
}

func TestActionIsNotTakenFromTheURL(t *testing.T) {
	f := newAccessFixture(t)
	target := f.account("other-user")
	if rec := f.request(http.MethodPost, userPath(target)+"?action=ban", "admin", url.Values{}); rec.Code != http.StatusBadRequest || f.account("other-user").Banned {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestRoleGuessesShareTheBudget(t *testing.T) {
	f := newAccessFixture(t)
	form := url.Values{"action": {"set_role"}, "from": {"user"}, "role": {"admin"}, "confirmation": {"wrong-password"}}
	for i := range confirmationBurst {
		if rec := f.manage("admin", "other-user", form); rec.Code != http.StatusOK {
			t.Fatalf("guess %d: status %d", i, rec.Code)
		}
	}
	form.Set("confirmation", testPassword)
	if rec := f.manage("admin", "other-user", form); rec.Code != http.StatusTooManyRequests || f.account("other-user").Role != store.RoleUser {
		t.Fatalf("after the guesses: status %d", rec.Code)
	}
}

func TestSelfDemotionLeavesTheManagement(t *testing.T) {
	f := newAccessFixture(t)
	rec := f.manage("admin", "admin", url.Values{"action": {"set_role"}, "from": {"admin"}, "role": {"user"}, "confirmation": {testPassword}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("status %d to %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestOnlyAdminsSeeTheHistoryOfOthers(t *testing.T) {
	f := newAccessFixture(t)
	if _, err := f.users.auth.OperatorReset(t.Context(), "other-user"); err != nil {
		t.Fatal(err)
	}
	for actor, want := range map[string]bool{"admin": true, "moderator": false} {
		body := f.request(http.MethodGet, userPath(f.account("other-user")), actor, nil).Body.String()
		if strings.Contains(body, "1-mal vergeben") != want {
			t.Errorf("%s sees the history = %t", actor, !want)
		}
	}
}
