// Tests for message limits and error events.
package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func sendText(t *testing.T, conn *websocket.Conn, text string) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"message": text})
	if err != nil {
		t.Fatal(err)
	}
	sendRaw(t, conn, string(raw))
}

func TestMessageLimitCountsCharacters(t *testing.T) {
	for name, text := range map[string]string{
		"umlauts":       strings.Repeat("ä", 4096),
		"emoji":         strings.Repeat("😀", 4096),
		"escaped ascii": strings.Repeat("<", 4096),
	} {
		t.Run(name, func(t *testing.T) {
			a := startTestApp(t, t.TempDir())
			conn, _ := a.connect(a.signIn("register", "alice"))
			sendText(t, conn, text)
			readUntil(t, conn, func(e ChatEvent) bool { return e.Type == EventMessage && e.Message == text })
		})
	}
}

func TestRejectedMessageKeepsTheConnection(t *testing.T) {
	long, err := json.Marshal(map[string]string{"message": strings.Repeat("ä", 4097)})
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{"too long": string(long), "not JSON": "hello"} {
		t.Run(name, func(t *testing.T) {
			a := startTestApp(t, t.TempDir())
			conn, _ := a.connect(a.signIn("register", "alice"))
			sendRaw(t, conn, raw)
			if e := readUntil(t, conn, func(e ChatEvent) bool { return e.Type == "error" }); e.Message == "" {
				t.Fatal("error is not explained")
			}
			sendText(t, conn, "still connected")
			readUntil(t, conn, func(e ChatEvent) bool { return e.Message == "still connected" })
		})
	}
}
