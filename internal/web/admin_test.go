// Permission matrix for routes and user management.
package web

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Schenna43lp1/openchat/internal/store"
)

type accessFixture struct {
	*testApp
	cookies map[string]*http.Cookie
}

// newAccessFixture creates one actor and one possible target for each kind of account.
func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	f := &accessFixture{testApp: startTestApp(t, t.TempDir()), cookies: map[string]*http.Cookie{}}
	seedAccounts(t, f.users,
		testAccount(t, "admin", store.RoleAdmin, false),
		testAccount(t, "moderator", store.RoleModerator, false),
		testAccount(t, "user", store.RoleUser, false),
		testAccount(t, "banned", store.RoleUser, true),
		testAccount(t, "banned-admin", store.RoleAdmin, true),
		testAccount(t, "banned-moderator", store.RoleModerator, true),
		testAccount(t, "other-admin", store.RoleAdmin, false),
		testAccount(t, "other-moderator", store.RoleModerator, false),
		testAccount(t, "other-user", store.RoleUser, false),
		testAccount(t, "deleted", store.RoleUser, false))
	for _, name := range []string{"admin", "moderator", "user", "banned", "banned-admin", "banned-moderator", "deleted"} {
		f.cookies[name] = newSessionCookie(t, f.sessions, name)
	}
	f.cookies["expired"] = newSessionCookie(t, f.sessions, "user")
	expireSession(f.sessions, f.cookies["expired"].Value)
	f.exec(`DELETE FROM users WHERE username = 'deleted'`)
	return f
}

// request sends a request through the real router. An actor without a cookie is anonymous.
func (f *accessFixture) request(method, path, actor string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie := f.cookies[actor]; cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	f.server.Config.Handler.ServeHTTP(rec, req)
	return rec
}

func TestRoutePermissions(t *testing.T) {
	const (
		ok        = http.StatusOK
		redirect  = http.StatusSeeOther
		forbidden = http.StatusForbidden
		denied    = http.StatusUnauthorized
		upgrade   = http.StatusBadRequest // access granted, but this is not a WebSocket handshake
	)
	routes := []string{"/", "/direct", "/admin/users", "/ws"}
	for _, tc := range []struct {
		actor string
		want  [4]int
	}{
		{"admin", [4]int{ok, ok, ok, upgrade}},
		{"moderator", [4]int{ok, ok, ok, upgrade}},
		{"user", [4]int{ok, ok, forbidden, upgrade}},
		{"banned", [4]int{redirect, redirect, redirect, forbidden}},
		{"banned-admin", [4]int{redirect, redirect, redirect, forbidden}},
		{"banned-moderator", [4]int{redirect, redirect, redirect, forbidden}},
		{"anonymous", [4]int{redirect, redirect, redirect, denied}},
		{"expired", [4]int{redirect, redirect, redirect, denied}},
		{"deleted", [4]int{redirect, redirect, redirect, denied}},
	} {
		for i, route := range routes {
			t.Run(tc.actor+route, func(t *testing.T) {
				f := newAccessFixture(t)
				rec := f.request(http.MethodGet, route, tc.actor, nil)
				if rec.Code != tc.want[i] {
					t.Fatalf("status = %d, want %d", rec.Code, tc.want[i])
				}
				if rec.Code == redirect && rec.Header().Get("Location") != "/login" {
					t.Fatalf("location = %q, want /login", rec.Header().Get("Location"))
				}
				switch tc.actor {
				case "banned", "banned-admin", "banned-moderator", "deleted":
					if f.sessions.Active(f.cookies[tc.actor].Value) {
						t.Fatal("session of a blocked account is still active")
					}
				}
			})
		}
	}
}

func TestBanPermissions(t *testing.T) {
	targets := []string{"other-admin", "other-moderator", "other-user", "banned", "banned-admin", "banned-moderator"}
	allowed := map[string][]bool{
		"admin":            {true, true, true, true, true, true},
		"moderator":        {false, false, true, true, false, false},
		"user":             {false, false, false, false, false, false},
		"banned-admin":     {false, false, false, false, false, false},
		"banned-moderator": {false, false, false, false, false, false},
		"anonymous":        {false, false, false, false, false, false},
	}
	for actor, permissions := range allowed {
		for i, target := range targets {
			t.Run(actor+"/"+target, func(t *testing.T) {
				f := newAccessFixture(t)
				want := f.users.List()
				if permissions[i] {
					for j := range want {
						if want[j].Username == target {
							want[j].Banned = !want[j].Banned
						}
					}
				}
				f.request(http.MethodPost, "/admin/users", actor, url.Values{"username": {target}, "action": {"toggle_ban"}})
				if got := f.users.List(); !reflect.DeepEqual(got, want) {
					t.Fatalf("accounts = %+v, want %+v", got, want)
				}
			})
		}
	}
}

func TestRolePermissions(t *testing.T) {
	changes := []struct {
		target string
		role   store.Role
	}{
		{"self", store.RoleAdmin},
		{"other-user", store.RoleAdmin},
		{"other-user", store.RoleModerator},
		{"other-moderator", store.RoleUser},
		{"other-admin", store.RoleUser},
	}
	for _, actor := range []string{"admin", "moderator", "user", "banned-admin", "banned-moderator", "anonymous"} {
		// An empty action is handled as a role change and needs the same checks.
		for _, action := range []string{"set_role", ""} {
			for _, change := range changes {
				target := change.target
				if target == "self" {
					target = actor
				}
				t.Run(actor+"/"+action+"/"+target+"/"+string(change.role), func(t *testing.T) {
					f := newAccessFixture(t)
					want := f.users.List()
					if actor == "admin" {
						for i := range want {
							if want[i].Username == target {
								want[i].Role = change.role
							}
						}
					}
					f.request(http.MethodPost, "/admin/users", actor,
						url.Values{"username": {target}, "action": {action}, "role": {string(change.role)}})
					if got := f.users.List(); !reflect.DeepEqual(got, want) {
						t.Fatalf("accounts = %+v, want %+v", got, want)
					}
				})
			}
		}
	}
}

func TestUserManagementFeedback(t *testing.T) {
	for _, tc := range []struct {
		name, actor, target, action, role string
		message                           string
	}{
		{"ban", "admin", "other-user", "toggle_ban", "", "Account wurde gesperrt."},
		{"unban", "admin", "banned", "toggle_ban", "", "Account wurde entsperrt."},
		{"role change", "admin", "other-user", "set_role", "moderator", "Rolle wurde aktualisiert."},
		{"self ban", "admin", "ADMIN", "toggle_ban", "", "eigenen Account nicht sperren"},
		{"self ban by moderator", "moderator", " Moderator ", "toggle_ban", "", "eigenen Account nicht sperren"},
		{"moderator bans staff", "moderator", "other-admin", "toggle_ban", "", "nur Benutzer mit Rolle user"},
		{"moderator changes role", "moderator", "user", "set_role", "admin", "Nur Admins"},
		{"invalid role", "admin", "user", "set_role", "owner", "Diese Rolle ist nicht erlaubt."},
		{"role of unknown user", "admin", "ghost", "set_role", "user", "wurde nicht gefunden"},
		{"ban of unknown user", "admin", "ghost", "toggle_ban", "", "wurde nicht gefunden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAccessFixture(t)
			rec := f.request(http.MethodPost, "/admin/users", tc.actor,
				url.Values{"username": {tc.target}, "action": {tc.action}, "role": {tc.role}})
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.message) {
				t.Fatalf("status = %d, want message %q", rec.Code, tc.message)
			}
		})
	}
}

func TestLastAdminCannotDemoteThemselves(t *testing.T) {
	f := newAccessFixture(t)
	if err := f.users.SetBanned("other-admin", true); err != nil {
		t.Fatal(err)
	}
	rec := f.request(http.MethodPost, "/admin/users", "admin",
		url.Values{"username": {"admin"}, "action": {"set_role"}, "role": {"user"}})
	if !strings.Contains(rec.Body.String(), "letzte aktive Admin") {
		t.Fatal("refusal is not explained")
	}
	if admin, _ := f.users.Find("admin"); admin.Role != store.RoleAdmin {
		t.Fatal("last active admin was demoted")
	}
}

func TestRoleChangeAppliesToExistingSession(t *testing.T) {
	f := newAccessFixture(t)
	if err := f.users.SetRole("moderator", store.RoleUser); err != nil {
		t.Fatal(err)
	}
	if rec := f.request(http.MethodGet, "/admin/users", "moderator", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("demoted moderator: status = %d, want 403", rec.Code)
	}
}

func TestOnlyAdminsSeeRoleControls(t *testing.T) {
	f := newAccessFixture(t)
	for actor, want := range map[string]bool{"admin": true, "moderator": false} {
		body := f.request(http.MethodGet, "/admin/users", actor, nil).Body.String()
		if got := strings.Contains(body, `value="set_role"`); got != want {
			t.Errorf("%s sees role controls = %t, want %t", actor, got, want)
		}
	}
}

func TestAdminPageEscapesStoredNames(t *testing.T) {
	f := newAccessFixture(t)
	const name = "<script>alert(1)</script>"
	seedAccounts(t, f.users, testAccount(t, name, store.RoleUser, false))
	if body := f.request(http.MethodGet, "/admin/users", "admin", nil).Body.String(); strings.Contains(body, name) {
		t.Fatal("stored name was rendered as HTML")
	}
}

func TestMethodsAndPaths(t *testing.T) {
	f := newAccessFixture(t)
	for _, tc := range []struct {
		method, path, actor string
		status              int
	}{
		{http.MethodGet, "/login", "anonymous", http.StatusOK},
		{http.MethodGet, "/login", "user", http.StatusSeeOther},
		{http.MethodPut, "/login", "anonymous", http.StatusMethodNotAllowed},
		{http.MethodGet, "/logout", "user", http.StatusMethodNotAllowed},
		{http.MethodPost, "/", "user", http.StatusMethodNotAllowed},
		{http.MethodGet, "/missing", "user", http.StatusNotFound},
		{http.MethodDelete, "/admin/users", "admin", http.StatusMethodNotAllowed},
	} {
		if rec := f.request(tc.method, tc.path, tc.actor, nil); rec.Code != tc.status {
			t.Errorf("%s %s as %s: status = %d, want %d", tc.method, tc.path, tc.actor, rec.Code, tc.status)
		}
	}
}

func TestInvalidFormsAreRejected(t *testing.T) {
	f := newAccessFixture(t)
	for _, path := range []string{"/login", "/admin/users"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("%zz"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(f.cookies["admin"])
		rec := httptest.NewRecorder()
		f.server.Config.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s with malformed form: status = %d, want 400", path, rec.Code)
		}
	}
	if rec := f.request(http.MethodPost, "/admin/users", "admin", url.Values{"action": {"delete"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown action: status = %d, want 400", rec.Code)
	}
}

func TestTemplateFailureReturnsServerError(t *testing.T) {
	tmpl := template.Must(template.New("layout").Funcs(template.FuncMap{
		"fail": func() (string, error) { return "", errors.New("render failed") },
	}).Parse("half a page {{fail}}"))
	rec := httptest.NewRecorder()
	render(rec, http.StatusOK, tmpl, page{}, slog.New(slog.DiscardHandler))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "half a page") {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body)
	}
}
