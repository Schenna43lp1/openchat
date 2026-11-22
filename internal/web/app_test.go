// Shared fixture: the real handler, hub and database on a loopback port.
package web

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"

	"github.com/Schenna43lp1/openchat/internal/auth"
	"github.com/Schenna43lp1/openchat/internal/chat"
	"github.com/Schenna43lp1/openchat/internal/store"
)

const testPassword = "test-only-password"

type testApp struct {
	t        *testing.T
	dir      string
	server   *httptest.Server
	db       *store.Store
	users    *testUsers
	sessions *testSessions
	stopHub  func()
	conns    []*websocket.Conn
	stopped  bool
}

func startTestApp(t *testing.T, dir string) *testApp {
	t.Helper()
	return startTestAppWith(t, dir, Options{AllowRegistration: true})
}

func startTestAppWith(t *testing.T, dir string, options Options) *testApp {
	t.Helper()
	return launchTestApp(t, dir, options, httptest.NewServer)
}

// launchTestApp also imports users.json and history.json from dir, like a first start after an upgrade.
func launchTestApp(t *testing.T, dir string, options Options, serve func(http.Handler) *httptest.Server) *testApp {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(dir, "openchat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	src := store.LegacySources{Users: filepath.Join(dir, "users.json"), History: filepath.Join(dir, "history.json")}
	if _, err := db.ImportLegacy(t.Context(), src, dir); err != nil {
		t.Fatal(err)
	}
	if options.Logger == nil {
		options.Logger = slog.New(slog.DiscardHandler)
	}
	service, err := auth.New(db, options.Logger)
	if err != nil {
		t.Fatal(err)
	}
	hub := chat.NewHub(db, options.Logger)
	options.Auth, options.Hub, options.Health = service, hub, db.Ping
	handler, err := New(options)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		hub.Run(ctx)
		close(stopped)
	}()
	a := &testApp{
		t:        t,
		dir:      dir,
		db:       db,
		server:   serve(handler),
		users:    &testUsers{t: t, db: db, auth: service},
		sessions: &testSessions{t: t, db: db, auth: service},
		stopHub: func() {
			cancel()
			<-stopped
		},
	}
	t.Cleanup(a.stop)
	return a
}

func (a *testApp) stop() {
	if a.stopped {
		return
	}
	a.stopped = true
	a.stopHub()
	for _, c := range a.conns {
		_ = c.Close()
	}
	a.server.Close()
	_ = a.db.Close()
}

// exec changes the database behind the application's back.
func (a *testApp) exec(statement string) {
	a.t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(a.dir, "openchat.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		a.t.Fatal(err)
	}
	_, execErr := db.ExecContext(a.t.Context(), statement)
	if err := errors.Join(execErr, db.Close()); err != nil {
		a.t.Fatal(err)
	}
}

func (a *testApp) send(method, path, body string, headers map[string]string) (*http.Response, string) {
	a.t.Helper()
	req, err := http.NewRequest(method, a.server.URL+path, strings.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for key, value := range headers {
		if value != "" {
			req.Header.Set(key, value)
		}
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		a.t.Fatal(err)
	}
	return res, string(raw)
}

func (a *testApp) post(path, cookie string, form url.Values) (*http.Response, string) {
	a.t.Helper()
	return a.send(http.MethodPost, path, form.Encode(), map[string]string{"Cookie": cookie})
}

func (a *testApp) signIn(mode, name string) string {
	a.t.Helper()
	res, body := a.post("/login", "", url.Values{"mode": {mode}, "username": {name}, "password": {testPassword}})
	if res.StatusCode != http.StatusSeeOther {
		a.t.Fatalf("%s %q: status %d: %s", mode, name, res.StatusCode, body)
	}
	cookie, _, _ := strings.Cut(res.Header.Get("Set-Cookie"), ";")
	return cookie
}

func (a *testApp) signInAdmin() string {
	a.t.Helper()
	if err := a.users.EnsureAdmin("admin", testPassword); err != nil {
		a.t.Fatal(err)
	}
	return a.signIn("login", "admin")
}

// connect returns the socket and the history it was given.
func (a *testApp) connect(cookie string) (*websocket.Conn, []chat.Message) {
	a.t.Helper()
	header := http.Header{"Cookie": {cookie}, "Origin": {a.server.URL}}
	conn, res, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(a.server.URL, "http")+"/ws", header)
	if err != nil {
		a.t.Fatalf("dial: %v", err)
	}
	_ = res.Body.Close()
	a.conns = append(a.conns, conn)
	return conn, readUntil(a.t, conn, func(e chat.Event) bool { return e.Type == chat.EventHistory }).Messages
}

func readUntil(t *testing.T, conn *websocket.Conn, match func(chat.Event) bool) chat.Event {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var event chat.Event
		if err := conn.ReadJSON(&event); err != nil {
			t.Fatalf("waiting for event: %v", err)
		}
		if match(event) {
			return event
		}
	}
}

func sendRaw(t *testing.T, conn *websocket.Conn, raw string) {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
		t.Fatal(err)
	}
}

func containsText(messages []chat.Message, text string) bool {
	for _, m := range messages {
		if m.Message == text {
			return true
		}
	}
	return false
}

// legacyAccount is an entry of users.json in v1.0.x.
type legacyAccount struct {
	Username     string     `json:"username"`
	PasswordHash string     `json:"passwordHash"`
	Role         store.Role `json:"role"`
	CreatedAt    string     `json:"createdAt"`
}

func writeFixture(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// testAccount uses the cheapest bcrypt cost to keep the suite fast under the race detector.
func testAccount(t *testing.T, name string, role store.Role, banned bool) store.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return store.User{Username: name, PasswordHash: string(hash), Role: role, Banned: banned, CreatedAt: time.UnixMilli(1_767_323_045_000)}
}

func seedAccounts(t *testing.T, users *testUsers, accounts ...store.User) {
	t.Helper()
	for _, account := range accounts {
		if _, err := users.db.CreateUser(t.Context(), account); err != nil {
			t.Fatal(err)
		}
	}
}

// testUsers changes accounts as the operator would.
type testUsers struct {
	t    *testing.T
	db   *store.Store
	auth *auth.Service
}

var operator = store.User{Username: "operator", Role: store.RoleAdmin}

func (u *testUsers) EnsureAdmin(name, password string) error {
	return u.auth.EnsureAdmin(u.t.Context(), name, password)
}

func (u *testUsers) Find(name string) (store.User, bool) {
	u.t.Helper()
	user, err := u.auth.Find(u.t.Context(), name)
	if errors.Is(err, auth.ErrUnknownUser) {
		return store.User{}, false
	}
	if err != nil {
		u.t.Fatal(err)
	}
	return user, true
}

func (u *testUsers) List() []store.User {
	u.t.Helper()
	users, err := u.auth.List(u.t.Context())
	if err != nil {
		u.t.Fatal(err)
	}
	return users
}

func (u *testUsers) SetRole(name string, role store.Role) error {
	return u.auth.SetRole(u.t.Context(), operator, name, role)
}

func (u *testUsers) SetBanned(name string, banned bool) error {
	if user, ok := u.Find(name); ok && user.Banned == banned {
		return nil
	}
	_, err := u.auth.ToggleBan(u.t.Context(), operator, name)
	return err
}

type testSessions struct {
	t    *testing.T
	db   *store.Store
	auth *auth.Service
}

func newSessionCookie(t *testing.T, sessions *testSessions, name string) *http.Cookie {
	t.Helper()
	user, err := sessions.db.UserByName(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := sessions.auth.StartSession(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: cookieName, Value: token}
}

func (s *testSessions) setExpiry(token string, expires time.Time) {
	s.t.Helper()
	hash := sha256.Sum256([]byte(token))
	user, _, err := s.db.Session(s.t.Context(), hash[:])
	if err != nil {
		s.t.Fatal(err)
	}
	if err := s.db.DeleteSession(s.t.Context(), hash[:]); err != nil {
		s.t.Fatal(err)
	}
	if err := s.db.CreateSession(s.t.Context(), hash[:], user, expires); err != nil {
		s.t.Fatal(err)
	}
}

func expireSession(sessions *testSessions, token string) {
	sessions.t.Helper()
	sessions.setExpiry(token, time.Now().Add(-time.Second))
}

func (s *testSessions) Active(token string) bool {
	s.t.Helper()
	_, err := s.auth.Session(s.t.Context(), token)
	if err != nil && !errors.Is(err, auth.ErrNoSession) {
		s.t.Fatal(err)
	}
	return err == nil
}

// Username resolves the session cookie of a request.
func (s *testSessions) Username(r *http.Request) (string, bool) {
	s.t.Helper()
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	user, err := s.auth.Session(s.t.Context(), cookie.Value)
	return user.Username, err == nil
}
