// Tests for the endpoints that store and hand out the keys for end-to-end encryption.
package web

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Schenna43lp1/openchat/internal/store"
)

var b64url = base64.RawURLEncoding.EncodeToString

// newKeyRequestDoc is what a browser sends to store its first key.
func newKeyRequestDoc(t *testing.T) map[string]string {
	t.Helper()
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"password":   testPassword,
		"key_id":     b64url(randomBytes(t, 16)),
		"public_key": b64url(private.PublicKey().Bytes()),
		"key_salt":   b64url(randomBytes(t, 16)),
		"wrapped":    b64url(randomBytes(t, 166)),
	}
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func readJSON(t *testing.T, body string, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), into); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
}

// sendJSON sends doc as it is if it is a string, otherwise encoded.
func (a *testApp) sendJSON(method, path, cookie string, doc any) (*http.Response, string) {
	a.t.Helper()
	raw, ok := doc.(string)
	if !ok {
		raw = mustJSON(a.t, doc)
	}
	return a.send(method, path, raw, map[string]string{"Cookie": cookie, "Content-Type": "application/json"})
}

func (a *testApp) putKey(cookie string, doc any) (*http.Response, string) {
	a.t.Helper()
	return a.sendJSON(http.MethodPut, keyPath, cookie, doc)
}

func (a *testApp) unlock(cookie, password string) (*http.Response, string) {
	a.t.Helper()
	return a.sendJSON(http.MethodPost, keyUnlockPath, cookie, map[string]string{"password": password})
}

func (a *testApp) ownKey(cookie string) ownKey {
	a.t.Helper()
	res, body := a.send(http.MethodGet, keyPath, "", map[string]string{"Cookie": cookie})
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/json" {
		a.t.Fatalf("own key: %d %q %s", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
	var doc ownKey
	readJSON(a.t, body, &doc)
	return doc
}

// seededCookie creates an account directly, which does not use up the registration budget.
func (a *testApp) seededCookie(name string) string {
	a.t.Helper()
	seedAccounts(a.t, a.users, testAccount(a.t, name, store.RoleUser, false))
	c := newSessionCookie(a.t, a.sessions, name)
	return c.Name + "=" + c.Value
}

func TestOwnKeyIsStoredOnce(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	cookie := a.signIn("register", "alice")

	before := a.ownKey(cookie)
	if before.Key != nil || before.Username != "alice" || before.UserID == 0 || len(before.InstanceID) != 16 {
		t.Fatalf("before: %+v", before)
	}
	if res, body := a.unlock(cookie, testPassword); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unlock without key: %d %s", res.StatusCode, body)
	}

	doc := newKeyRequestDoc(t)
	if res, body := a.putKey(cookie, doc); res.StatusCode != http.StatusCreated || strings.Contains(body, "wrapped") {
		t.Fatalf("put: %d %s", res.StatusCode, body)
	}
	after := a.ownKey(cookie)
	if after.Key == nil || after.Key.KeyID != doc["key_id"] || b64url(after.Key.PublicKey) != doc["public_key"] ||
		after.Key.CreatedAt == 0 || !bytes.Equal(after.InstanceID, before.InstanceID) {
		t.Fatalf("after: %+v", after)
	}

	if res, body := a.putKey(cookie, newKeyRequestDoc(t)); res.StatusCode != http.StatusConflict {
		t.Fatalf("second key: %d %s", res.StatusCode, body)
	}
}

// A stolen session alone must neither get the wrapped key, which allows guessing the password offline, nor plant
// a key of its own.
func TestKeyMaterialNeedsThePassword(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	cookie := a.signIn("register", "alice")

	wrong := newKeyRequestDoc(t)
	wrong["password"] = "not-the-password"
	if res, body := a.putKey(cookie, wrong); res.StatusCode != http.StatusForbidden {
		t.Fatalf("put with wrong password: %d %s", res.StatusCode, body)
	}
	if got := a.ownKey(cookie); got.Key != nil {
		t.Fatalf("key stored with wrong password: %+v", got.Key)
	}

	doc := newKeyRequestDoc(t)
	if res, body := a.putKey(cookie, doc); res.StatusCode != http.StatusCreated {
		t.Fatalf("put: %d %s", res.StatusCode, body)
	}
	res, body := a.send(http.MethodGet, keyPath, "", map[string]string{"Cookie": cookie})
	if strings.Contains(body, doc["wrapped"]) || strings.Contains(body, doc["key_salt"]) || strings.Contains(body, "wrapped") {
		t.Fatalf("GET shows the private part: %d %s", res.StatusCode, body)
	}
	if res, body := a.unlock(cookie, "not-the-password"); res.StatusCode != http.StatusForbidden || strings.Contains(body, doc["wrapped"]) {
		t.Fatalf("unlock with wrong password: %d %s", res.StatusCode, body)
	}

	res, body = a.unlock(cookie, testPassword)
	var unlocked keyDoc
	readJSON(t, body, &unlocked)
	if res.StatusCode != http.StatusOK || unlocked.KeyID != doc["key_id"] || b64url(unlocked.Wrapped) != doc["wrapped"] ||
		b64url(unlocked.Salt) != doc["key_salt"] || b64url(unlocked.PublicKey) != doc["public_key"] {
		t.Fatalf("unlock: %d %s", res.StatusCode, body)
	}
}

func TestWrongPasswordsForKeysAreLimited(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	cookie := a.seededCookie("alice")
	for range confirmationBurst {
		if res, _ := a.unlock(cookie, "wrong-password"); res.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", res.StatusCode)
		}
	}
	// The budget is shared with the password form, and the right password does not open it again.
	res, _ := a.unlock(cookie, testPassword)
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Fatalf("status = %d, Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	if res, _ := a.post(passwordPath, cookie, passwordForm(testPassword, ownPassword)); res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("password form after key guesses: %d", res.StatusCode)
	}
}

func TestPublicKeysShowNoPrivatePart(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	alice := a.signIn("register", "alice")
	bob := a.signIn("register", "bob")
	doc := newKeyRequestDoc(t)
	if res, body := a.putKey(alice, doc); res.StatusCode != http.StatusCreated {
		t.Fatalf("put: %d %s", res.StatusCode, body)
	}

	res, body := a.send(http.MethodGet, "/keys/ALICE", "", map[string]string{"Cookie": bob})
	var got map[string]any
	readJSON(t, body, &got)
	if res.StatusCode != http.StatusOK || got["username"] != "alice" || got["key_id"] != doc["key_id"] ||
		got["public_key"] != doc["public_key"] || len(got) != 4 {
		t.Fatalf("public key: %d %s", res.StatusCode, body)
	}

	for _, name := range []string{"bob", "nobody"} {
		if res, body := a.send(http.MethodGet, "/keys/"+name, "", map[string]string{"Cookie": alice}); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d %s", name, res.StatusCode, body)
		}
	}
}

func TestInvalidKeysAreRejected(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	offCurve := append([]byte{4}, bytes.Repeat([]byte{1}, 64)...)
	set := func(field, value string) func(map[string]string) any {
		return func(d map[string]string) any { d[field] = value; return d }
	}

	cases := map[string]func(map[string]string) any{
		"short key id":     set("key_id", b64url(randomBytes(t, 8))),
		"padded key id":    set("key_id", b64url(randomBytes(t, 16))+"=="),
		"point off curve":  set("public_key", b64url(offCurve)),
		"compressed point": set("public_key", b64url(randomBytes(t, 33))),
		"short salt":       set("key_salt", b64url(randomBytes(t, 8))),
		"tiny wrap":        set("wrapped", b64url(randomBytes(t, 20))),
		"huge wrap":        set("wrapped", b64url(randomBytes(t, 2000))),
		"standard base64":  set("key_salt", base64.StdEncoding.EncodeToString(randomBytes(t, 16))),
		"missing field":    func(d map[string]string) any { delete(d, "wrapped"); return d },
		"unknown field":    set("private_key", "x"),
		"created_at set": func(d map[string]string) any {
			return strings.Replace(mustJSON(t, d), "{", `{"created_at":1,`, 1)
		},
		"trailing data": func(d map[string]string) any { return mustJSON(t, d) + "{}" },
		"not json":      func(map[string]string) any { return "key=value" },
	}
	n := 0
	for name, change := range cases {
		n++
		// One account per case: refused writes use up the budget of the account.
		cookie := a.seededCookie(fmt.Sprintf("user%d", n))
		if res, body := a.putKey(cookie, change(newKeyRequestDoc(t))); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, res.StatusCode, body)
		}
		if got := a.ownKey(cookie); got.Key != nil {
			t.Errorf("%s: a rejected key was stored", name)
		}
	}

	cookie := a.seededCookie("plain")
	res, body := a.send(http.MethodPut, keyPath, mustJSON(t, newKeyRequestDoc(t)),
		map[string]string{"Cookie": cookie, "Content-Type": "text/plain"})
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d %s", res.StatusCode, body)
	}
}

func TestKeyRoutesNeedALoginAndAnswerWithStatus(t *testing.T) {
	f := newAccessFixture(t)
	for actor, want := range map[string]int{
		"user":      http.StatusOK,
		"anonymous": http.StatusUnauthorized,
		"expired":   http.StatusUnauthorized,
		"deleted":   http.StatusUnauthorized,
		"banned":    http.StatusForbidden,
	} {
		if rec := f.request(http.MethodGet, keyPath, actor, nil); rec.Code != want {
			t.Errorf("%s %s: status = %d, want %d", actor, keyPath, rec.Code, want)
		}
		wantPublic := want
		switch actor {
		case "user":
			wantPublic = http.StatusNotFound // admin has no key
		case "banned":
			wantPublic = http.StatusUnauthorized // the first request ended the session
		}
		if rec := f.request(http.MethodGet, "/keys/admin", actor, nil); rec.Code != wantPublic {
			t.Errorf("%s /keys/admin: status = %d, want %d", actor, rec.Code, wantPublic)
		}
	}
}

func TestIssuedPasswordCannotUseKeys(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	a.signIn("register", "carol")
	issued := a.issue("carol")
	cookie := a.signInWith("carol", issued.Password)
	doc := newKeyRequestDoc(t)
	doc["password"] = issued.Password
	if res, body := a.putKey(cookie, doc); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("put with issued password: %d %s", res.StatusCode, body)
	}
	if res, body := a.unlock(cookie, issued.Password); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unlock with issued password: %d %s", res.StatusCode, body)
	}
}

func TestKeyWritesFromOtherSitesAreRejected(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	cookie := a.signIn("register", "alice")
	for _, req := range []struct{ method, path string }{{http.MethodPut, keyPath}, {http.MethodPost, keyUnlockPath}} {
		res, body := a.send(req.method, req.path, mustJSON(t, newKeyRequestDoc(t)), map[string]string{
			"Cookie": cookie, "Content-Type": "application/json", "Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site",
		})
		if res.StatusCode != http.StatusForbidden || strings.Contains(body, "wrapped") {
			t.Errorf("cross-site %s %s: %d %s", req.method, req.path, res.StatusCode, body)
		}
	}
}

func TestKeyWritesAreLimited(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	cookie := a.signIn("register", "alice")
	for range keyWriteBurst {
		a.putKey(cookie, "not json")
	}
	res, _ := a.putKey(cookie, newKeyRequestDoc(t))
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Fatalf("status = %d, Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
}

func TestKeyRouteMethods(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	cookie := a.signIn("register", "alice")
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		if res, _ := a.send(method, keyPath, "", map[string]string{"Cookie": cookie}); res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d", method, keyPath, res.StatusCode)
		}
	}
	// The catch-all route of the chat page answers other methods on these paths.
	for _, req := range []struct{ method, path string }{{http.MethodGet, keyUnlockPath}, {http.MethodPut, "/keys/alice"}} {
		if res, body := a.send(req.method, req.path, "", map[string]string{"Cookie": cookie}); res.StatusCode != http.StatusNotFound || strings.Contains(body, "wrapped") {
			t.Errorf("%s %s: %d", req.method, req.path, res.StatusCode)
		}
	}
}

func TestHandlerNeedsAKeyStore(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New accepted options without a key store")
	}
}
