// Tests for the protection of the HTTP and WebSocket entry points.
package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Schenna43lp1/openchat/internal/chat"
	"github.com/Schenna43lp1/openchat/internal/store"
)

func TestFirstRegistrationIsNotAdmin(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	a.signIn("register", "first")
	if user, _ := a.users.Find("first"); user.Role != store.RoleUser {
		t.Fatalf("role = %q, want user", user.Role)
	}
}

func TestRestartPromotesNobody(t *testing.T) {
	dir := t.TempDir()
	a := startTestApp(t, dir)
	seedAccounts(t, a.users, testAccount(t, "member", store.RoleUser, false))
	a.stop()
	if user, _ := startTestApp(t, dir).users.Find("member"); user.Role != store.RoleUser {
		t.Fatalf("role = %q, want user", user.Role)
	}
}

func TestForeignOriginIsRejectedOnEveryPost(t *testing.T) {
	for _, path := range []string{"/login", "/logout", "/admin/users", "/", "/direct"} {
		for name, headers := range map[string]map[string]string{
			"origin":       {"Origin": "https://evil.example"},
			"opaque":       {"Origin": "null"},
			"same site":    {"Sec-Fetch-Site": "same-site"},
			"cross site":   {"Sec-Fetch-Site": "cross-site"},
			"other scheme": {"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"},
		} {
			t.Run(path+" "+name, func(t *testing.T) {
				a := startTestApp(t, t.TempDir())
				if res, _ := a.send(http.MethodPost, path, "mode=login", headers); res.StatusCode != http.StatusForbidden {
					t.Fatalf("status = %d, want 403", res.StatusCode)
				}
			})
		}
	}
}

func TestLoginFloodIsLimited(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	form := url.Values{"mode": {"login"}, "username": {"ghost"}, "password": {"wrong-password"}}.Encode()
	for range 60 {
		// A client that is no trusted proxy cannot choose its own address.
		res, _ := a.send(http.MethodPost, "/login", form, map[string]string{"X-Forwarded-For": "198.51.100.7"})
		if res.StatusCode == http.StatusTooManyRequests {
			if res.Header.Get("Retry-After") == "" {
				t.Fatal("Retry-After is missing")
			}
			return
		}
	}
	t.Fatal("60 attempts were accepted")
}

func TestRegistrationFloodIsLimited(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	// Invalid names keep the test fast and must count as attempts, too.
	form := url.Values{"mode": {"register"}, "username": {"x"}, "password": {testPassword}}.Encode()
	for range 10 {
		if res, _ := a.send(http.MethodPost, "/login", form, nil); res.StatusCode == http.StatusTooManyRequests {
			return
		}
	}
	t.Fatal("10 registrations were accepted")
}

func TestMessageFloodIsLimited(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	conn, _ := a.connect(a.signIn("register", "alice"))
	for range 25 {
		sendRaw(t, conn, `{"message":"flood"}`)
	}
	readUntil(t, conn, func(e chat.Event) bool { return e.Type == chat.EventError })
}

func TestLargeBodyIsRejected(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	if res, _ := a.send(http.MethodPost, "/login", "username="+strings.Repeat("a", 100<<10), nil); res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", res.StatusCode)
	}
}

func TestSecurityHeaders(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	for _, path := range []string{"/login", "/static/app.js", "/missing"} {
		res, _ := a.send(http.MethodGet, path, "", nil)
		policy := res.Header.Get("Content-Security-Policy")
		for _, part := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'", "form-action 'self'"} {
			if !strings.Contains(policy, part) {
				t.Errorf("%s: policy lacks %q", path, part)
			}
		}
		if strings.Contains(policy, "unsafe-") {
			t.Errorf("%s: policy = %q", path, policy)
		}
		for key, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			// no-referrer would make browsers send "Origin: null" with every form.
			"Referrer-Policy": "same-origin",
		} {
			if got := res.Header.Get(key); got != want {
				t.Errorf("%s: %s = %q, want %q", path, key, got, want)
			}
		}
	}
}

func TestStaticDirectoriesAreNotListed(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	for _, path := range []string{"/static/", "/static/css/"} {
		if res, _ := a.send(http.MethodGet, path, "", nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, res.StatusCode)
		}
	}
}

func TestPagesLoadNothingExternalOrInline(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	if _, page := a.send(http.MethodGet, "/login", "", nil); strings.Contains(page, "<script>") || strings.Contains(page, "googleapis") {
		t.Error("login page has an inline script or an external resource")
	}
	for _, path := range []string{"/static/css/base.css", "/static/css/root.css"} {
		if _, css := a.send(http.MethodGet, path, "", nil); strings.Contains(css, "https://") {
			t.Errorf("%s loads an external resource", path)
		}
	}
}
