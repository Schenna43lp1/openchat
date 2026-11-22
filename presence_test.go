// Tests for the online list and for what the history keeps.
package main

import (
	"fmt"
	"io"
	"log"
	"path/filepath"
	"slices"
	"testing"
)

func TestSeveralTabsCountAsOnePresence(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	alice, bob := a.signIn("register", "alice"), a.signIn("register", "bob")
	observer, _ := a.connect(bob)
	first, _ := a.connect(alice)
	readUntil(t, observer, func(e ChatEvent) bool { return slices.Contains(e.Users, "alice") })

	second, _ := a.connect(alice)
	list := readUntil(t, second, func(e ChatEvent) bool { return e.Type == EventUsers })
	if !slices.Equal(list.Users, []string{"alice", "bob"}) {
		t.Fatalf("users = %v", list.Users)
	}

	_ = second.Close()
	sendRaw(t, first, `{"message":"still online"}`)
	readUntil(t, observer, func(e ChatEvent) bool {
		if e.Type == EventSystem {
			t.Fatalf("second tab was announced: %q", e.Message)
		}
		return e.Message == "still online"
	})
}

func TestReconnectsDoNotDisplaceMessages(t *testing.T) {
	hub := NewHubWithHistoryPath(log.New(io.Discard, "", 0), filepath.Join(t.TempDir(), "history.json"), testDirectory)
	for i := range messageHistoryLimit {
		hub.addToHistory(newMessage(EventMessage, "alice", fmt.Sprint("message ", i)))
	}
	done := make(chan struct{})
	go func() {
		hub.Run()
		close(done)
	}()
	t.Cleanup(func() {
		hub.Close()
		<-done
	})

	for range 55 {
		client := &Client{hub: hub, send: make(chan ChatEvent, 32), username: "alice"}
		hub.register <- client
		hub.unregister <- client
	}
	last := &Client{hub: hub, send: make(chan ChatEvent, 32), username: "bob"}
	hub.register <- last
	history := waitForEventType(t, last.send, EventHistory).Messages
	if len(history) != messageHistoryLimit || history[0].Message != "message 0" {
		t.Fatalf("history has %d entries, first = %+v", len(history), history[0])
	}
}

func TestStoredJoinNoticesAreNotLoaded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	stored := []Message{{Type: EventMessage, Message: "keep me"}}
	for range messageHistoryLimit {
		stored = append(stored, Message{Type: EventSystem, Message: "joined"})
	}
	writeFixture(t, path, stored)
	hub := NewHubWithHistoryPath(log.New(io.Discard, "", 0), path, testDirectory)
	if len(hub.history) != 1 || hub.history[0].Message != "keep me" {
		t.Fatalf("history = %+v", hub.history)
	}
}

func TestDroppedSlowClientLeavesTheOnlineList(t *testing.T) {
	hub := startTestHub(t)
	observer := &Client{hub: hub, send: make(chan ChatEvent, 64), username: "bob"}
	hub.register <- observer
	slow := &Client{hub: hub, send: make(chan ChatEvent, 3), username: "alice"} // full after joining
	hub.register <- slow
	hub.BroadcastMessage("bob", "overflow")

	waitForEventType(t, observer.send, EventMessage)
	if list := waitForEventType(t, observer.send, EventUsers); !slices.Equal(list.Users, []string{"bob"}) {
		t.Fatalf("users = %v", list.Users)
	}
}
