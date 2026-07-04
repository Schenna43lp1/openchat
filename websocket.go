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

func serveWebSocket(hub *Hub, logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		username := sanitizeUsername(r.URL.Query().Get("username"))
		if username == "" {
			http.Error(w, "username is required", http.StatusBadRequest)
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

func sanitizeUsername(username string) string {
	username = strings.TrimSpace(username)
	username = strings.Join(strings.Fields(username), " ")
	if len(username) > 32 {
		username = username[:32]
	}
	return username
}
