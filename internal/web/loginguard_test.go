// Tests for the limit of guessed passwords per account.
package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Schenna43lp1/openchat/internal/store"
)

const (
	wantGuessesPerAccount = 20
)

// loginFrom sends a login from its own address, like one node of a botnet.
func (a *testApp) loginFrom(node int, name, password string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	form := url.Values{"mode": {"login"}, "username": {name}, "password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = fmt.Sprintf("198.18.%d.%d:40000", node/250, node%250+1)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	a.server.Config.Handler.ServeHTTP(rec, req)
	return rec
}

func (a *testApp) exhaustGuesses(name string) {
	a.t.Helper()
	for node := range wantGuessesPerAccount {
		if rec := a.loginFrom(node, name, "wrong-password"); rec.Code != http.StatusOK {
			a.t.Fatalf("guess %d: status = %d, want 200", node, rec.Code)
		}
	}
}

// rememberDevice signs in and returns the cookie that recognizes the browser.
func (a *testApp) rememberDevice(node int, name string) *http.Cookie {
	a.t.Helper()
	rec := a.loginFrom(node, name, testPassword)
	if rec.Code != http.StatusSeeOther {
		a.t.Fatalf("login: status = %d", rec.Code)
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "openchat_device" {
			return cookie
		}
	}
	a.t.Fatal("login set no device cookie")
	return nil
}

func TestDistributedGuessingIsLimited(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false), testAccount(t, "bob", store.RoleUser, false))
	a.exhaustGuesses("alice")

	for i, name := range []string{"alice", "ALICE", "  Alice "} {
		rec := a.loginFrom(100+i, name, testPassword)
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
			t.Errorf("%q from a new address: status = %d, want 429 with Retry-After", name, rec.Code)
		}
	}
	if rec := a.loginFrom(200, "bob", testPassword); rec.Code != http.StatusSeeOther {
		t.Fatalf("other account: status = %d, want 303", rec.Code)
	}
}

func TestParallelGuessesShareTheBudget(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	codes := make([]int, 3*wantGuessesPerAccount)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Go(func() { codes[i] = a.loginFrom(i, "alice", "wrong-password").Code })
	}
	wg.Wait()

	checked := 0
	for _, code := range codes {
		if code == http.StatusOK {
			checked++
		}
	}
	if checked != wantGuessesPerAccount {
		t.Fatalf("checked passwords = %d, want %d", checked, wantGuessesPerAccount)
	}
}

func TestUnknownNamesAreLimitedAlike(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	a.exhaustGuesses("ghost")
	if rec := a.loginFrom(100, "ghost", "wrong-password"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429, otherwise the limit reveals which accounts exist", rec.Code)
	}
}

func TestSuccessfulLoginsCostNoBudget(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	for node := range 2 * wantGuessesPerAccount {
		if rec := a.loginFrom(node, "alice", testPassword); rec.Code != http.StatusSeeOther {
			t.Fatalf("login %d: status = %d, want 303", node, rec.Code)
		}
	}
}

func TestKnownDeviceIsNotLockedOut(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false), testAccount(t, "mallory", store.RoleUser, false))
	own, foreign := a.rememberDevice(300, "alice"), a.rememberDevice(301, "mallory")
	a.exhaustGuesses("alice")

	for name, cookie := range map[string]*http.Cookie{
		"cookie of another account": foreign,
		"forged":                    {Name: own.Name, Value: strings.Repeat("A", len(own.Value))},
		"empty":                     {Name: own.Name},
	} {
		if rec := a.loginFrom(302, "alice", testPassword, cookie); rec.Code != http.StatusTooManyRequests {
			t.Errorf("%s: status = %d, want 429", name, rec.Code)
		}
	}
	if rec := a.loginFrom(303, "alice", testPassword, own); rec.Code != http.StatusSeeOther {
		t.Fatalf("known device: status = %d, want 303", rec.Code)
	}

	// A stolen cookie must not allow unlimited guessing either.
	for node := range wantGuessesPerAccount {
		a.loginFrom(400+node, "alice", "wrong-password", own)
	}
	if rec := a.loginFrom(500, "alice", testPassword, own); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("known device after a flood: status = %d, want 429", rec.Code)
	}
}

func TestDeviceCookie(t *testing.T) {
	dir := t.TempDir()
	a := startTestApp(t, dir)
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	device := a.rememberDevice(1, "alice")
	if !device.HttpOnly || !device.Secure || device.SameSite != http.SameSiteLaxMode || device.Path != "/login" || device.MaxAge <= 0 {
		t.Fatalf("cookie = %+v", device)
	}
	a.stop()

	b := startTestApp(t, dir)
	b.exhaustGuesses("alice")
	if rec := b.loginFrom(100, "alice", testPassword, device); rec.Code != http.StatusSeeOther {
		t.Fatalf("device after a restart: status = %d, want 303", rec.Code)
	}
}
