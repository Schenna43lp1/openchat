// Tests for the online list.
package web

import (
	"slices"
	"testing"

	"github.com/Schenna43lp1/openchat/internal/chat"
)

func TestSeveralTabsCountAsOnePresence(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	alice, bob := a.signIn("register", "alice"), a.signIn("register", "bob")
	observer, _ := a.connect(bob)
	first, _ := a.connect(alice)
	readUntil(t, observer, func(e chat.Event) bool { return slices.Contains(e.Users, "alice") })

	second, _ := a.connect(alice)
	list := readUntil(t, second, func(e chat.Event) bool { return e.Type == chat.EventUsers })
	if !slices.Equal(list.Users, []string{"alice", "bob"}) {
		t.Fatalf("users = %v", list.Users)
	}

	_ = second.Close()
	sendRaw(t, first, `{"message":"still online"}`)
	readUntil(t, observer, func(e chat.Event) bool {
		if e.Type == chat.EventSystem {
			t.Fatalf("second tab was announced: %q", e.Message)
		}
		return e.Message == "still online"
	})
}
