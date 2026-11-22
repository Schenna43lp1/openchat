package chat

import (
	"testing"
	"time"

	"github.com/Schenna43lp1/openchat/internal/store"
)

func TestResetHidesMessagesPublishedAfterIt(t *testing.T) {
	f := newFixture(t)
	bob, alice := f.users["bob"], f.users["alice"]
	direct := func(body string) store.Message {
		m, err := f.store.AddMessage(t.Context(), store.Message{
			Kind: store.KindDirect, SenderID: bob.ID, Sender: "bob", RecipientID: alice.ID, Recipient: "alice",
			Body: body, CreatedAt: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	before := direct("before the reset") // stored, but not published yet
	reset, err := f.store.SetPassword(t.Context(), store.PasswordChange{
		TargetID: alice.ID, Hash: "new", HideDirectMessages: true,
		Check: func(store.User, store.User) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.users["alice"] = reset
	c := f.connect("alice", sendBuffer)
	f.hub.post(before)
	f.hub.post(direct("after the reset"))

	timeout := time.After(3 * time.Second)
	for {
		select {
		case event := <-c.send:
			if event.Message == "before the reset" {
				t.Fatal("hidden message was delivered live")
			}
			for _, m := range event.Messages {
				if m.Message == "before the reset" {
					t.Fatal("hidden message is in the history")
				}
			}
			if event.Message == "after the reset" {
				return
			}
		case <-timeout:
			t.Fatal("new message did not arrive")
		}
	}
}
