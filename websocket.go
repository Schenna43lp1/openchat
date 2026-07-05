package main

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     checkWebSocketOrigin,
}

// checkWebSocketOrigin allows same-host browser origins by default and supports
// additional explicit origins via OPENCHAT_ALLOWED_ORIGINS (comma-separated).
func checkWebSocketOrigin(r *http.Request) bool {
	origin, ok := normalizeOrigin(r.Header.Get("Origin"))
	if !ok {
		return false
	}

	requestHost := strings.TrimSpace(r.Host)
	if requestHost != "" && strings.EqualFold(origin.Host, requestHost) {
		return true
	}

	for _, allowed := range configuredAllowedOrigins() {
		if strings.EqualFold(origin.String(), allowed) {
			return true
		}
	}

	return false
}

func configuredAllowedOrigins() []string {
	raw := strings.TrimSpace(os.Getenv("OPENCHAT_ALLOWED_ORIGINS"))
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	allowed := make([]string, 0, len(parts))
	for _, part := range parts {
		normalized, ok := normalizeOrigin(part)
		if !ok {
			continue
		}
		allowed = append(allowed, normalized.String())
	}
	return allowed
}

func normalizeOrigin(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, false
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, false
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed, true
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
