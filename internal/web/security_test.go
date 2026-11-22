// Regression tests for private message isolation, connection revocation and persistence failures.
package web

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"

	"github.com/Schenna43lp1/openchat/internal/chat"
	"github.com/Schenna43lp1/openchat/internal/store"
)

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
	readUntil(t, bobConn, func(e chat.Event) bool { return e.Message == "secret" })
	sendRaw(t, aliceConn, `{"message":"public"}`)
	readUntil(t, malloryConn, func(e chat.Event) bool {
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
	writeFixture(t, filepath.Join(dir, "history.json"), []chat.Message{
		{Type: chat.EventDirect, Username: "alice", To: "bob", Message: "received before account", Timestamp: old},
		{Type: chat.EventDirect, Username: "bob", To: "alice", Message: "sent before account", Timestamp: old},
		{Type: chat.EventDirect, Username: "alice", To: "bob", Message: "no timestamp"},
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
	writeFixture(t, filepath.Join(dir, "users.json"), []legacyAccount{
		{Username: "Bob", PasswordHash: string(hash), Role: store.RoleAdmin, CreatedAt: created},
		{Username: "broken", PasswordHash: string(hash), Role: store.RoleUser, CreatedAt: "not a date"},
	})
	old := time.Now().Add(-time.Hour).UnixMilli()
	writeFixture(t, filepath.Join(dir, "history.json"), []chat.Message{
		{Type: chat.EventDirect, Username: "alice", To: "bob", Message: "for bob", Timestamp: old},
		{Type: chat.EventDirect, Username: "alice", To: "broken", Message: "for broken", Timestamp: old},
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
			readUntil(t, conn, func(e chat.Event) bool {
				if e.Message == "private" {
					t.Fatalf("message was accepted as %q to %q", e.Type, e.To)
				}
				return e.Type == chat.EventSystem && strings.Contains(e.Message, "nicht gesendet")
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
	if e := readUntil(t, conn, func(e chat.Event) bool { return e.Message == "hello" }); e.To != "Bob" {
		t.Fatalf("recipient = %q, want Bob", e.To)
	}
}

func TestRevokedConnectionsAreClosed(t *testing.T) {
	const shortSession = 1500 * time.Millisecond
	cases := map[string]func(a *testApp, admin, victim string){
		"ban": func(a *testApp, admin, _ string) {
			a.post(userPath(a.account("victim")), admin, url.Values{"action": {"ban"}})
		},
		"logout": func(a *testApp, _, victim string) {
			a.post("/logout", victim, nil)
		},
		"expiry": func(*testApp, string, string) {
			time.Sleep(shortSession)
		},
	}
	for name, revoke := range cases {
		t.Run(name, func(t *testing.T) {
			a := startTestApp(t, t.TempDir())
			admin := a.signInAdmin()
			victim := a.signIn("register", "victim")
			if name == "expiry" {
				// Time alone has to close the sockets, without any revocation event.
				a.sessions.setExpiry(strings.TrimPrefix(victim, cookieName+"="), time.Now().Add(shortSession))
			}
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
	readUntil(t, adminConn, func(e chat.Event) bool {
		if e.Message == "after ban" {
			t.Fatal("banned user could still send")
		}
		return e.Message == "admins only now"
	})
	_ = victimConn.SetReadDeadline(time.Now().Add(time.Second))
	for {
		var e chat.Event
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
	readUntil(t, conn, func(e chat.Event) bool { return e.Message == "still here" })
}

func TestFailedSaveRollsBack(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	seedAccounts(t, a.users, testAccount(t, "member", store.RoleUser, false))
	a.exec(`CREATE TRIGGER fail_insert BEFORE INSERT ON users BEGIN SELECT RAISE(ABORT, 'disk unavailable'); END`)
	a.exec(`CREATE TRIGGER fail_update BEFORE UPDATE ON users BEGIN SELECT RAISE(ABORT, 'disk unavailable'); END`)

	for name, request := range map[string]struct {
		path string
		form url.Values
	}{
		"registration": {"/login", url.Values{"mode": {"register"}, "username": {"newcomer"}, "password": {testPassword}}},
		"role change": {userPath(a.account("member")), url.Values{"action": {"set_role"}, "from": {"user"}, "role": {"moderator"},
			"confirmation": {testPassword}}},
		"ban": {userPath(a.account("member")), url.Values{"action": {"ban"}}},
	} {
		// Not "wrong password": the user has to learn that saving failed.
		if _, body := a.post(request.path, admin, request.form); !strings.Contains(body, "nicht gespeichert") {
			t.Errorf("%s does not report the failed save", name)
		}
	}
	if _, ok := a.users.Find("newcomer"); ok {
		t.Error("failed registration was stored")
	}
	if member, _ := a.users.Find("member"); member.Role != store.RoleUser || member.Banned {
		t.Errorf("failed change was stored: %+v", member)
	}
}
