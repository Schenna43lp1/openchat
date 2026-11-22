// Tests for the limit of open connections per account.
package web

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Schenna43lp1/openchat/internal/chat"
	"github.com/Schenna43lp1/openchat/internal/store"
)

const (
	wantConnectionsPerAccount = 10
	wantCloseTooMany          = 4008
)

// dialChat does not fail the test, because some callers expect a refusal.
func (a *testApp) dialChat(cookie string) (*websocket.Conn, error) {
	header := http.Header{"Cookie": {cookie}, "Origin": {a.server.URL}}
	conn, res, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(a.server.URL, "http")+"/ws", header)
	if err != nil {
		return nil, err
	}
	_ = res.Body.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var first chat.Event
	if err := conn.ReadJSON(&first); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func TestConnectionsPerAccountAreLimited(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false), testAccount(t, "bob", store.RoleUser, false))
	laptop, phone, bob := a.signIn("login", "alice"), a.signIn("login", "ALICE"), a.signIn("login", "bob")

	var open []*websocket.Conn
	for i := range wantConnectionsPerAccount {
		cookie := laptop
		if i%2 == 1 {
			cookie = phone // the limit is per account, not per session
		}
		conn, err := a.dialChat(cookie)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		a.conns = append(a.conns, conn)
		open = append(open, conn)
	}
	if conn, err := a.dialChat(laptop); err == nil {
		_ = conn.Close()
		t.Fatal("connection over the limit was accepted")
	} else if !websocket.IsCloseError(err, wantCloseTooMany) {
		t.Fatalf("over the limit: %v, want close code %d", err, wantCloseTooMany)
	}
	a.connect(bob)

	_ = open[0].Close()
	a.expectFreeSlot(laptop)
}

func TestParallelConnectionsShareTheLimit(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	cookie := a.signIn("login", "alice")

	conns := make([]*websocket.Conn, 3*wantConnectionsPerAccount)
	var wg sync.WaitGroup
	for i := range conns {
		wg.Go(func() {
			if conn, err := a.dialChat(cookie); err == nil {
				conns[i] = conn
			}
		})
	}
	wg.Wait()

	accepted := 0
	for _, conn := range conns {
		if conn != nil {
			accepted++
			a.conns = append(a.conns, conn)
		}
	}
	if accepted != wantConnectionsPerAccount {
		t.Fatalf("accepted = %d, want %d", accepted, wantConnectionsPerAccount)
	}
}

func TestRevokedConnectionsFreeTheirSlots(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	old, current := a.signIn("login", "alice"), a.signIn("login", "alice")
	for range wantConnectionsPerAccount {
		a.connect(old)
	}
	a.post("/logout", old, nil)
	a.expectFreeSlot(current)
}

func TestRejectedHandshakeUsesNoSlot(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	cookie := a.signIn("login", "alice")
	for range 2 * wantConnectionsPerAccount {
		if res, _ := a.send(http.MethodGet, "/ws", "", map[string]string{"Cookie": cookie}); res.StatusCode != http.StatusBadRequest {
			t.Fatalf("plain GET: status = %d, want 400", res.StatusCode)
		}
	}
	a.connect(cookie)
}

// expectFreeSlot polls, because the server notices a closed socket with a delay.
func (a *testApp) expectFreeSlot(cookie string) {
	a.t.Helper()
	var last error
	for range 100 {
		conn, err := a.dialChat(cookie)
		if err == nil {
			a.conns = append(a.conns, conn)
			return
		}
		last = err
		time.Sleep(50 * time.Millisecond)
	}
	a.t.Fatalf("no slot became free: %v", last)
}
