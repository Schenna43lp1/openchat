package chat

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Schenna43lp1/openchat/internal/store"
)

type fixture struct {
	t     *testing.T
	hub   *Hub
	store *store.Store
	users map[string]store.User
	stop  func()
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "openchat.db"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, store: st, hub: NewHub(st, slog.New(slog.DiscardHandler)), users: map[string]store.User{}}
	for _, name := range []string{"alice", "bob", "carol"} {
		u, err := st.CreateUser(t.Context(), store.User{Username: name, PasswordHash: "hash", Role: store.RoleUser, CreatedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		f.users[name] = u
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		f.hub.Run(ctx)
		close(stopped)
	}()
	f.stop = func() {
		cancel()
		<-stopped
	}
	t.Cleanup(func() {
		f.stop()
		_ = st.Close()
	})
	return f
}

// connect registers a client without a socket.
func (f *fixture) connect(name string, buffer int) *client {
	c := &client{hub: f.hub, user: f.users[name], send: make(chan Event, buffer)}
	f.hub.register <- c
	return c
}

func (f *fixture) message(from, to, text string) store.Message {
	f.t.Helper()
	m := store.Message{Kind: store.KindPublic, SenderID: f.users[from].ID, Sender: from, Body: text, CreatedAt: time.Now()}
	if to != "" {
		m.Kind, m.RecipientID, m.Recipient = store.KindDirect, f.users[to].ID, to
	}
	saved, err := f.store.AddMessage(f.t.Context(), m)
	if err != nil {
		f.t.Fatal(err)
	}
	return saved
}

func waitFor(t *testing.T, c *client, match func(Event) bool) Event {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case event, ok := <-c.send:
			if !ok {
				t.Fatal("client was removed")
			}
			if match(event) {
				return event
			}
		case <-timeout:
			t.Fatal("timed out waiting for an event")
		}
	}
}

func ofType(kind EventType) func(Event) bool {
	return func(e Event) bool { return e.Type == kind }
}

func TestDirectMessageDeliveredOnlyToParticipants(t *testing.T) {
	f := newFixture(t)
	alice, bob, carol := f.connect("alice", 32), f.connect("bob", 32), f.connect("carol", 32)
	f.hub.post(f.message("alice", "bob", "hallo bob"))
	f.hub.post(f.message("alice", "", "marker"))

	for _, c := range []*client{alice, bob} {
		if e := waitFor(t, c, ofType(EventDirect)); e.Username != "alice" || e.To != "bob" {
			t.Fatalf("direct event = %+v", e)
		}
	}
	waitFor(t, carol, func(e Event) bool {
		if e.Type == EventDirect {
			t.Fatal("third party received a direct message")
		}
		return e.Message == "marker"
	})
}

func TestDirectMessageToOfflineUserSendsNotice(t *testing.T) {
	f := newFixture(t)
	alice := f.connect("alice", 32)
	waitFor(t, alice, ofType(EventUsers))
	f.hub.post(f.message("alice", "bob", "hallo"))
	if e := waitFor(t, alice, ofType(EventSystem)); e.Message == "" {
		t.Fatal("notice is empty")
	}
}

func TestStoredMessageIsNotShownTwice(t *testing.T) {
	f := newFixture(t)
	stored := f.message("alice", "", "stored first")
	bob := f.connect("bob", 32) // gets it as history
	f.hub.post(stored)
	f.hub.post(f.message("alice", "", "later"))

	if history := waitFor(t, bob, ofType(EventHistory)).Messages; len(history) != 1 {
		t.Fatalf("history = %+v", history)
	}
	waitFor(t, bob, func(e Event) bool {
		if e.Message == "stored first" {
			t.Fatal("message was delivered twice")
		}
		return e.Message == "later"
	})
}

func TestReconnectsDoNotDisplaceMessages(t *testing.T) {
	f := newFixture(t)
	for i := range store.HistoryLimit {
		f.message("alice", "", fmt.Sprint("message ", i))
	}
	for range 55 {
		c := f.connect("alice", 32)
		f.hub.unregister <- c
	}
	history := waitFor(t, f.connect("bob", 32), ofType(EventHistory)).Messages
	if len(history) != store.HistoryLimit || history[0].Message != "message 0" {
		t.Fatalf("history has %d entries, first = %+v", len(history), history[0])
	}
}

func TestDroppedSlowClientLeavesTheOnlineList(t *testing.T) {
	f := newFixture(t)
	bob := f.connect("bob", 64)
	f.connect("alice", 3) // full after joining
	f.hub.post(f.message("bob", "", "overflow"))

	waitFor(t, bob, ofType(EventMessage))
	if list := waitFor(t, bob, ofType(EventUsers)); !slices.Equal(list.Users, []string{"bob"}) {
		t.Fatalf("users = %v", list.Users)
	}
}

func TestNothingBlocksAfterShutdown(t *testing.T) {
	f := newFixture(t)
	c := f.connect("alice", 32)
	f.stop()

	returned := make(chan struct{})
	go func() {
		f.hub.notify(c, EventError, "late")
		f.hub.post(store.Message{Kind: store.KindPublic})
		f.hub.leave(c)
		if f.hub.join(&client{hub: f.hub}) {
			t.Error("stopped hub accepted a client")
		}
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("call blocked after shutdown")
	}
}
