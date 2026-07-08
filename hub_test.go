package main

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDirectMessageDeliveredOnlyToParticipants(t *testing.T) {
	hub := NewHub(log.New(io.Discard, "", 0))
	go hub.Run()
	defer hub.Close()

	alice := &Client{hub: hub, send: make(chan ChatEvent, 32), username: "alice"}
	bob := &Client{hub: hub, send: make(chan ChatEvent, 32), username: "bob"}
	charlie := &Client{hub: hub, send: make(chan ChatEvent, 32), username: "charlie"}

	hub.register <- alice
	hub.register <- bob
	hub.register <- charlie

	drainEvents(alice.send)
	drainEvents(bob.send)
	drainEvents(charlie.send)

	hub.SendDirectMessage("alice", "bob", "hallo bob")

	aliceEvent := waitForEventType(t, alice.send, EventDirect)
	if aliceEvent.To != "bob" || aliceEvent.Username != "alice" {
		t.Fatalf("unexpected direct event for sender: %+v", aliceEvent)
	}

	bobEvent := waitForEventType(t, bob.send, EventDirect)
	if bobEvent.To != "bob" || bobEvent.Username != "alice" {
		t.Fatalf("unexpected direct event for recipient: %+v", bobEvent)
	}

	if hasEventType(charlie.send, EventDirect, 250*time.Millisecond) {
		t.Fatal("non-participant should not receive direct message")
	}
}

func TestDirectMessageToOfflineUserSendsSystemNotice(t *testing.T) {
	hub := NewHub(log.New(io.Discard, "", 0))
	go hub.Run()
	defer hub.Close()

	alice := &Client{hub: hub, send: make(chan ChatEvent, 32), username: "alice"}
	hub.register <- alice
	drainEvents(alice.send)

	hub.SendDirectMessage("alice", "nobody", "hallo")

	event := waitForEventType(t, alice.send, EventSystem)
	if event.Message == "" {
		t.Fatal("expected system notice for failed direct delivery")
	}
}

func TestChatHistoryIsPersistedToDisk(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "chat-history.json")
	hub := NewHubWithHistoryPath(log.New(io.Discard, "", 0), historyPath)
	hub.addToHistory(Message{Type: EventMessage, Username: "alice", Message: "hello", Time: "10:00:00", Timestamp: 1})
	hub.addToHistory(Message{Type: EventDirect, Username: "alice", To: "bob", Message: "secret", Time: "10:01:00", Timestamp: 2})
	hub.saveHistory()

	raw, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatalf("read persisted history: %v", err)
	}

	var history []Message
	if err := json.Unmarshal(raw, &history); err != nil {
		t.Fatalf("decode persisted history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("persisted history length = %d, want %d", len(history), 2)
	}
	if history[0].Message != "hello" || history[1].Type != EventDirect {
		t.Fatalf("persisted history mismatch: %+v", history)
	}
}

func drainEvents(ch chan ChatEvent) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func waitForEventType(t *testing.T, ch chan ChatEvent, eventType EventType) ChatEvent {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case event := <-ch:
			if event.Type == eventType {
				return event
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s event", eventType)
		}
	}
}

func hasEventType(ch chan ChatEvent, eventType EventType, duration time.Duration) bool {
	timeout := time.After(duration)
	for {
		select {
		case event := <-ch:
			if event.Type == eventType {
				return true
			}
		case <-timeout:
			return false
		}
	}
}
