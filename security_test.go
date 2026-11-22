// Regression tests for private message isolation, connection revocation and persistence failures.
package main

import (
	"encoding/json"
	"html/template"
	"io"
	"log"
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
)

const testPassword = "test-only-password"

type testApp struct {
	t        *testing.T
	dir      string
	server   *httptest.Server
	users    *UserStore
	sessions *SessionManager
	hub      *Hub
	hubDone  chan struct{}
	conns    []*websocket.Conn
	stopped  bool
}

func startTestApp(t *testing.T, dir string) *testApp {
	t.Helper()
	return startTestAppWith(t, dir, config{allowRegistration: true})
}

func startTestAppWith(t *testing.T, dir string, cfg config) *testApp {
	t.Helper()
	parse := func(name string) *template.Template {
		return template.Must(template.ParseFiles("templates/" + name + ".html"))
	}
	users, err := NewUserStore(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	l := log.New(io.Discard, "", 0)
	a := &testApp{t: t, dir: dir, users: users, sessions: NewSessionManager(), hubDone: make(chan struct{})}
	a.hub = NewHubWithHistoryPath(l, filepath.Join(dir, "history.json"), users)
	go func() {
		a.hub.Run()
		close(a.hubDone)
	}()
	tmpl := appTemplates{index: parse("index"), direct: parse("direct"), login: parse("login"), admin: parse("admin")}
	a.server = httptest.NewServer(newRouter(tmpl, users, a.sessions, a.hub, cfg, l))
	t.Cleanup(a.stop)
	return a
}

func (a *testApp) stop() {
	if a.stopped {
		return
	}
	a.stopped = true
	a.hub.Close()
	<-a.hubDone
	for _, c := range a.conns {
		_ = c.Close()
	}
	a.server.Close()
	_ = a.users.Close()
}

func (a *testApp) post(path, cookie string, form url.Values) (*http.Response, string) {
	a.t.Helper()
	req, err := http.NewRequest(http.MethodPost, a.server.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		a.t.Fatal(err)
	}
	return res, string(body)
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
func (a *testApp) connect(cookie string) (*websocket.Conn, []Message) {
	a.t.Helper()
	header := http.Header{"Cookie": {cookie}, "Origin": {a.server.URL}}
	conn, res, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(a.server.URL, "http")+"/ws", header)
	if err != nil {
		a.t.Fatalf("dial: %v", err)
	}
	_ = res.Body.Close()
	a.conns = append(a.conns, conn)
	return conn, readUntil(a.t, conn, func(e ChatEvent) bool { return e.Type == EventHistory }).Messages
}

func readUntil(t *testing.T, conn *websocket.Conn, match func(ChatEvent) bool) ChatEvent {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var event ChatEvent
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

func containsText(messages []Message, text string) bool {
	for _, m := range messages {
		if m.Message == text {
			return true
		}
	}
	return false
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

func TestPrivateMessagesStayPrivate(t *testing.T) {
	dir := t.TempDir()
	a := startTestApp(t, dir)
	alice := a.signIn("register", "alice")
	bob := a.signIn("register", "bob")
	mallory := a.signIn("register", "mallory")
	aliceConn, _ := a.connect(alice)
	bobConn, _ := a.connect(bob)
	malloryConn, _ := a.connect(mallory)

	sendRaw(t, aliceConn, `{"message":"secret","to":"bob"}`)
	readUntil(t, bobConn, func(e ChatEvent) bool { return e.Message == "secret" })
	sendRaw(t, aliceConn, `{"message":"public"}`)
	readUntil(t, malloryConn, func(e ChatEvent) bool {
		if e.Message == "secret" {
			t.Fatal("third party received a live direct message")
		}
		return e.Message == "public"
	})

	check := func(a *testApp, alice, bob, mallory, late string) {
		for _, tc := range []struct {
			name, cookie string
			private      bool
		}{{"sender", alice, true}, {"recipient", bob, true}, {"third party", mallory, false}, {"late account", late, false}} {
			_, history := a.connect(tc.cookie)
			if got := containsText(history, "secret"); got != tc.private {
				t.Errorf("%s sees direct message = %t, want %t", tc.name, got, tc.private)
			}
			if !containsText(history, "public") {
				t.Errorf("%s lost the public history", tc.name)
			}
		}
	}
	check(a, alice, bob, mallory, a.signIn("register", "late"))

	a.stop()
	b := startTestApp(t, dir)
	check(b, b.signIn("login", "alice"), b.signIn("login", "bob"), b.signIn("login", "mallory"), b.signIn("login", "late"))
}

func TestReRegisteredNameDoesNotInheritMessages(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour).UnixMilli()
	writeFixture(t, filepath.Join(dir, "history.json"), []Message{
		{Type: EventDirect, Username: "alice", To: "bob", Message: "received before account", Timestamp: old},
		{Type: EventDirect, Username: "bob", To: "alice", Message: "sent before account", Timestamp: old},
		{Type: EventDirect, Username: "alice", To: "bob", Message: "no timestamp"},
		{Type: "unknown", Message: "unknown type", Timestamp: old},
	})
	a := startTestApp(t, dir)
	if _, history := a.connect(a.signIn("register", "BOB")); len(history) != 0 {
		t.Fatalf("new account inherited %+v", history)
	}
}

func TestExistingAccountsKeepTheirMessages(t *testing.T) {
	dir := t.TempDir()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	writeFixture(t, filepath.Join(dir, "users.json"), []authUser{
		{Username: "Bob", PasswordHash: string(hash), Role: RoleAdmin, CreatedAt: created},
		{Username: "broken", PasswordHash: string(hash), Role: RoleUser, CreatedAt: "not a date"},
	})
	old := time.Now().Add(-time.Hour).UnixMilli()
	writeFixture(t, filepath.Join(dir, "history.json"), []Message{
		{Type: EventDirect, Username: "alice", To: "bob", Message: "for bob", Timestamp: old},
		{Type: EventDirect, Username: "alice", To: "broken", Message: "for broken", Timestamp: old},
	})
	a := startTestApp(t, dir)
	if _, history := a.connect(a.signIn("login", "bob")); !containsText(history, "for bob") {
		t.Error("existing account lost its direct message")
	}
	if _, history := a.connect(a.signIn("login", "broken")); containsText(history, "for broken") {
		t.Error("account with unknown creation time must not see direct history")
	}
}

func TestInvalidRecipientIsRejected(t *testing.T) {
	prefix := strings.Repeat("b", 32)
	for name, to := range map[string]string{
		"unknown":                      "ghost",
		"empty":                        "",
		"blank":                        "   ",
		"longer than an existing name": prefix + "x",
	} {
		t.Run(name, func(t *testing.T) {
			a := startTestApp(t, t.TempDir())
			alice := a.signIn("register", "alice")
			a.signIn("register", prefix)
			conn, _ := a.connect(alice)
			raw, err := json.Marshal(map[string]string{"message": "private", "to": to})
			if err != nil {
				t.Fatal(err)
			}
			sendRaw(t, conn, string(raw))
			readUntil(t, conn, func(e ChatEvent) bool {
				if e.Message == "private" {
					t.Fatalf("message was accepted as %q to %q", e.Type, e.To)
				}
				return e.Type == EventSystem && strings.Contains(e.Message, "nicht gesendet")
			})
			if _, history := a.connect(alice); containsText(history, "private") {
				t.Fatal("rejected message was stored")
			}
		})
	}
}

func TestRecipientNameIsCanonical(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	alice := a.signIn("register", "alice")
	a.signIn("register", "Bob")
	conn, _ := a.connect(alice)
	sendRaw(t, conn, `{"message":"hello","to":" bOB "}`)
	if e := readUntil(t, conn, func(e ChatEvent) bool { return e.Message == "hello" }); e.To != "Bob" {
		t.Fatalf("recipient = %q, want Bob", e.To)
	}
}

func TestRevokedConnectionsAreClosed(t *testing.T) {
	cases := map[string]func(a *testApp, admin, victim string){
		"ban": func(a *testApp, admin, _ string) {
			a.post("/admin/users", admin, url.Values{"username": {"victim"}, "action": {"toggle_ban"}})
		},
		"logout": func(a *testApp, _, victim string) {
			a.post("/logout", victim, nil)
		},
		"expiry": func(a *testApp, _, victim string) {
			token := strings.TrimPrefix(victim, authCookieName+"=")
			a.sessions.mu.Lock()
			s := a.sessions.sessions[token]
			s.ExpiresAt = time.Now().Add(-time.Second)
			a.sessions.sessions[token] = s
			a.sessions.mu.Unlock()
		},
	}
	for name, revoke := range cases {
		t.Run(name, func(t *testing.T) {
			a := startTestApp(t, t.TempDir())
			admin := a.signInAdmin()
			victim := a.signIn("register", "victim")
			first, _ := a.connect(victim)
			second, _ := a.connect(victim)

			revoke(a, admin, victim)
			deadline := time.Now().Add(time.Second)
			for _, conn := range []*websocket.Conn{first, second} {
				_ = conn.SetReadDeadline(deadline)
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
							t.Fatalf("socket not revoked within one second: %v", err)
						}
						break
					}
				}
			}
		})
	}
}

func TestRevokedConnectionCannotSendOrReceive(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	victim := a.signIn("register", "victim")
	adminConn, _ := a.connect(admin)
	victimConn, _ := a.connect(victim)

	if err := a.users.SetBanned("victim", true); err != nil {
		t.Fatal(err)
	}
	// No waiting: both directions must be blocked before the periodic check fires.
	_ = victimConn.WriteMessage(websocket.TextMessage, []byte(`{"message":"after ban"}`))
	sendRaw(t, adminConn, `{"message":"admins only now"}`)
	readUntil(t, adminConn, func(e ChatEvent) bool {
		if e.Message == "after ban" {
			t.Fatal("banned user could still send")
		}
		return e.Message == "admins only now"
	})
	_ = victimConn.SetReadDeadline(time.Now().Add(time.Second))
	for {
		var e ChatEvent
		if err := victimConn.ReadJSON(&e); err != nil {
			break
		}
		if e.Message == "admins only now" {
			t.Fatal("banned user could still receive")
		}
	}
}

func TestLogoutKeepsOtherSessionsConnected(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	laptop := a.signIn("register", "alice")
	phone := a.signIn("login", "alice")
	conn, _ := a.connect(phone)
	a.post("/logout", laptop, nil)
	time.Sleep(700 * time.Millisecond)
	sendRaw(t, conn, `{"message":"still here"}`)
	readUntil(t, conn, func(e ChatEvent) bool { return e.Message == "still here" })
}

func TestFailedSaveRollsBack(t *testing.T) {
	for _, file := range []string{"users.json", "users.sqlite"} {
		t.Run(file, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), file)
			store, err := NewUserStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			for _, name := range []string{"admin", "member"} {
				if err := store.Register(name, testPassword); err != nil {
					t.Fatal(err)
				}
			}
			if store.db != nil {
				_, err = store.db.Exec(`CREATE TRIGGER fail_write BEFORE INSERT ON users BEGIN SELECT RAISE(ABORT, 'disk unavailable'); END`)
			} else {
				err = os.Mkdir(path+".tmp", 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}

			if store.Register("newcomer", testPassword) == nil {
				t.Error("Register must report the failed save")
			}
			if _, ok := store.Find("newcomer"); ok {
				t.Error("failed registration stayed in memory")
			}
			if store.SetRole("member", RoleModerator) == nil {
				t.Error("SetRole must report the failed save")
			}
			if user, _ := store.Find("member"); user.Role != RoleUser {
				t.Error("failed role change stayed in memory")
			}
			if store.SetBanned("member", true) == nil {
				t.Error("SetBanned must report the failed save")
			}
			if user, _ := store.Find("member"); user.Banned {
				t.Error("failed ban stayed in memory")
			}
		})
	}
}

func TestFailedSaveIsNotReportedAsWrongPassword(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	if err := os.Mkdir(filepath.Join(a.dir, "users.json.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, body := a.post("/login", "", url.Values{"mode": {"register"}, "username": {"alice"}, "password": {testPassword}})
	if !strings.Contains(body, "nicht gespeichert") {
		t.Fatal("storage failure is not explained to the user")
	}
}
