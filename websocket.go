package main

import (
	"log"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// serveWebSocket upgrades authenticated HTTP requests and registers chat clients at the hub.
func serveWebSocket(hub *Hub, logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		user, _ := r.Context().Value(currentUserContextKey).(currentUser)
		username := sanitizeUsername(user.Username)
		if username == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Printf("upgrade websocket: %v", err)
			return
		}

		client := &Client{
			hub:      hub,
			conn:     conn,
			send:     make(chan ChatEvent, 256),
			username: username,
			logger:   logger,
		}

		hub.register <- client

		go client.writePump()
		go client.readPump()
	}
}

// sanitizeUsername ensures websocket identity is compact and printable.
func sanitizeUsername(username string) string {
	username = strings.TrimSpace(username)
	username = strings.Join(strings.Fields(username), " ")
	if len(username) > 32 {
		username = username[:32]
	}
	return username
}
