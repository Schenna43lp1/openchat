// Hub component for the Open chat application.
// Manages client registration, message broadcasting, direct messaging, and chat history persistence.
package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const messageHistoryLimit = 100

type EventType string

const (
	EventMessage EventType = "message"
	EventDirect  EventType = "direct"
	EventSystem  EventType = "system"
	EventUsers   EventType = "users"
	EventHistory EventType = "history"
	EventError   EventType = "error"
)

type ChatEvent struct {
	Type      EventType `json:"type"`
	Username  string    `json:"username,omitempty"`
	To        string    `json:"to,omitempty"`
	Message   string    `json:"message,omitempty"`
	Time      string    `json:"time,omitempty"`
	Users     []string  `json:"users,omitempty"`
	Messages  []Message `json:"messages,omitempty"`
	Timestamp int64     `json:"timestamp,omitempty"`
}

type Message struct {
	Type      EventType `json:"type"`
	Username  string    `json:"username,omitempty"`
	To        string    `json:"to,omitempty"`
	Message   string    `json:"message"`
	Time      string    `json:"time"`
	Timestamp int64     `json:"timestamp"`
}

type directMessage struct {
	From string
	To   string
	Text string
}

type clientNotice struct {
	client *Client
	text   string
}

// userDirectory lets the hub resolve accounts for routing and history visibility.
type userDirectory interface {
	Find(username string) (authUser, bool)
}

type Hub struct {
	register   chan *Client
	unregister chan *Client
	broadcast  chan Message
	direct     chan directMessage
	notices    chan clientNotice
	done       chan struct{}

	clients     map[*Client]bool
	departed    []string // removed connections that still need an announcement
	users       userDirectory
	history     []Message
	historyPath string
	logger      *log.Logger

	closeOnce sync.Once
}

// NewHub creates the central chat event broker.
func NewHub(logger *log.Logger, users userDirectory) *Hub {
	return NewHubWithHistoryPath(logger, resolveChatHistoryPath(), users)
}

// NewHubWithHistoryPath creates the hub and loads persisted chat history from disk.
func NewHubWithHistoryPath(logger *log.Logger, historyPath string, users userDirectory) *Hub {
	hub := &Hub{
		register:    make(chan *Client),
		unregister:  make(chan *Client),
		broadcast:   make(chan Message, 256),
		direct:      make(chan directMessage, 256),
		notices:     make(chan clientNotice),
		done:        make(chan struct{}),
		clients:     make(map[*Client]bool),
		historyPath: historyPath,
		users:       users,
		logger:      logger,
	}
	hub.loadHistory()
	return hub
}

// Run processes register/unregister/broadcast events in a single goroutine.
func (h *Hub) Run() {
	for {
		// Announce outside the send loops, which may drop clients themselves.
		h.announceDeparted()

		select {
		case client := <-h.register:
			arrived := !h.online(client.username)
			h.clients[client] = true
			h.logger.Printf("client joined: %s", client.username)

			client.send <- ChatEvent{Type: EventHistory, Messages: h.historyFor(client.username)}
			if arrived {
				h.broadcastSystem(client.username + " hat den Chat betreten.")
			}
			h.broadcastUsers()

		case client := <-h.unregister:
			if h.clients[client] {
				h.remove(client)
				h.logger.Printf("client left: %s", client.username)
			}

		case message := <-h.broadcast:
			// Chat messages are persisted in in-memory history and fanned out to all clients.
			h.logger.Printf("broadcasting message user=%q type=%q recipients=%d", message.Username, message.Type, len(h.clients))
			h.addToHistory(message)
			h.saveHistory()
			h.sendAll(ChatEvent{
				Type:      message.Type,
				Username:  message.Username,
				To:        message.To,
				Message:   message.Message,
				Time:      message.Time,
				Timestamp: message.Timestamp,
			})

		case message := <-h.direct:
			// Only existing accounts may be addressed, otherwise whoever registers the name later could be targeted.
			recipient, ok := h.users.Find(message.To)
			if !ok {
				h.logger.Printf("direct message rejected: unknown recipient from %q", message.From)
				notice := newMessage(EventSystem, "", "Direktnachricht wurde nicht gesendet (Empfaenger unbekannt).")
				h.sendToUsername(message.From, ChatEvent{
					Type:      EventSystem,
					Message:   notice.Message,
					Time:      notice.Time,
					Timestamp: notice.Timestamp,
				})
				continue
			}

			direct := newMessage(EventDirect, message.From, message.Text)
			direct.To = recipient.Username
			h.logger.Printf("routing direct message from %q to %q", message.From, direct.To)
			h.addToHistory(direct)
			h.saveHistory()
			delivered := h.sendDirect(direct)
			if !delivered {
				h.logger.Printf("direct message could not be delivered: recipient %q offline", direct.To)
				h.sendToUsername(message.From, ChatEvent{
					Type:      EventSystem,
					Message:   "Benutzer ist offline. Die Direktnachricht steht im Verlauf, bis neuere Nachrichten sie verdraengen.",
					Time:      direct.Time,
					Timestamp: direct.Timestamp,
				})
			}

		case notice := <-h.notices:
			// Only the hub may write to send, because it is the one closing that channel.
			if h.clients[notice.client] {
				event := newMessage(EventError, "", notice.text)
				h.sendOne(notice.client, ChatEvent{Type: event.Type, Message: event.Message, Time: event.Time, Timestamp: event.Timestamp})
			}

		case <-h.done:
			h.logger.Println("hub shutting down")
			for client := range h.clients {
				delete(h.clients, client)
				close(client.send)
				if client.conn != nil {
					_ = client.conn.Close()
				}
			}
			return
		}
	}
}

// Close signals the hub loop to stop exactly once.
func (h *Hub) Close() {
	h.closeOnce.Do(func() {
		close(h.done)
	})
}

// BroadcastMessage submits a user message to the hub event loop.
func (h *Hub) BroadcastMessage(username, text string) {
	h.broadcast <- newMessage(EventMessage, username, text)
}

// SendDirectMessage queues a direct message from sender to recipient.
func (h *Hub) SendDirectMessage(from, to, text string) {
	h.direct <- directMessage{From: from, To: to, Text: text}
}

// Notify reports a problem to one connection.
func (h *Hub) Notify(client *Client, text string) {
	select {
	case h.notices <- clientNotice{client: client, text: text}:
	case <-h.done:
	}
}

func (h *Hub) online(username string) bool {
	for client := range h.clients {
		if strings.EqualFold(client.username, username) {
			return true
		}
	}
	return false
}

func (h *Hub) remove(client *Client) {
	delete(h.clients, client)
	close(client.send)
	h.departed = append(h.departed, client.username)
}

// announceDeparted reports users whose last connection is gone.
func (h *Hub) announceDeparted() {
	for len(h.departed) > 0 {
		username := h.departed[0]
		h.departed = h.departed[1:]
		if h.online(username) {
			continue
		}
		h.broadcastSystem(username + " hat den Chat verlassen.")
		h.broadcastUsers()
	}
}

// broadcastSystem emits system messages (join/leave). They are live only and never enter the history.
func (h *Hub) broadcastSystem(text string) {
	message := newMessage(EventSystem, "", text)
	h.sendAll(ChatEvent{
		Type:      EventSystem,
		Message:   message.Message,
		Time:      message.Time,
		Timestamp: message.Timestamp,
	})
}

// broadcastUsers sends the current online user list.
func (h *Hub) broadcastUsers() {
	users := make([]string, 0, len(h.clients))
	for client := range h.clients {
		users = append(users, client.username)
	}
	slices.Sort(users)
	users = slices.Compact(users) // one entry for several tabs
	h.sendAll(ChatEvent{Type: EventUsers, Users: users})
}

// sendAll performs non-blocking delivery and drops slow clients defensively.
func (h *Hub) sendAll(event ChatEvent) {
	for client := range h.clients {
		select {
		case client.send <- event:
		default:
			h.remove(client)
			if client.conn != nil {
				_ = client.conn.Close()
			}
			h.logger.Printf("dropped slow client: %s", client.username)
		}
	}
}

func (h *Hub) sendDirect(message Message) bool {
	event := ChatEvent{
		Type:      EventDirect,
		Username:  message.Username,
		To:        message.To,
		Message:   message.Message,
		Time:      message.Time,
		Timestamp: message.Timestamp,
	}

	recipientDelivered := false
	for client := range h.clients {
		if strings.EqualFold(client.username, message.Username) {
			h.sendOne(client, event)
			continue
		}
		if strings.EqualFold(client.username, message.To) {
			recipientDelivered = true
			h.sendOne(client, event)
		}
	}
	return recipientDelivered
}

func (h *Hub) sendToUsername(username string, event ChatEvent) {
	for client := range h.clients {
		if strings.EqualFold(client.username, username) {
			h.sendOne(client, event)
		}
	}
}

func (h *Hub) sendOne(client *Client, event ChatEvent) {
	select {
	case client.send <- event:
	default:
		h.remove(client)
		if client.conn != nil {
			_ = client.conn.Close()
		}
		h.logger.Printf("dropped slow client: %s", client.username)
	}
}

// historyFor returns the part of the shared history that username may read.
// Direct messages are bound to the account: one created after a message was sent
// never sees it, even if it carries the same name. Unknown event types are withheld.
func (h *Hub) historyFor(username string) []Message {
	var accountSince int64
	canReadDirect := false
	if user, ok := h.users.Find(username); ok {
		if created, err := time.Parse(time.RFC3339, user.CreatedAt); err == nil {
			accountSince, canReadDirect = created.UnixMilli(), true
		}
	}

	visible := make([]Message, 0, len(h.history))
	for _, message := range h.history {
		switch message.Type {
		case EventMessage, EventSystem:
			visible = append(visible, message)
		case EventDirect:
			participant := strings.EqualFold(message.Username, username) || strings.EqualFold(message.To, username)
			if participant && canReadDirect && message.Timestamp >= accountSince {
				visible = append(visible, message)
			}
		}
	}
	return visible
}

// addToHistory keeps only the newest messages up to configured limit.
func (h *Hub) addToHistory(message Message) {
	h.history = append(h.history, message)
	if len(h.history) > messageHistoryLimit {
		h.history = h.history[len(h.history)-messageHistoryLimit:]
	}
}

func (h *Hub) loadHistory() {
	if h.historyPath == "" {
		return
	}
	raw, err := os.ReadFile(h.historyPath)
	if err != nil {
		if !os.IsNotExist(err) {
			h.logger.Printf("load chat history: %v", err)
		}
		return
	}
	var history []Message
	if err := json.Unmarshal(raw, &history); err != nil {
		h.logger.Printf("decode chat history: %v", err)
		return
	}
	// Files written by older versions also contain join and leave notices.
	history = slices.DeleteFunc(history, func(m Message) bool { return m.Type == EventSystem })
	if len(history) > messageHistoryLimit {
		history = history[len(history)-messageHistoryLimit:]
	}
	h.history = history
}

func (h *Hub) saveHistory() {
	if h.historyPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(h.historyPath), 0o755); err != nil {
		h.logger.Printf("create chat history dir: %v", err)
		return
	}
	raw, err := json.MarshalIndent(h.history, "", "  ")
	if err != nil {
		h.logger.Printf("encode chat history: %v", err)
		return
	}
	if err := os.WriteFile(h.historyPath, raw, 0o644); err != nil {
		h.logger.Printf("save chat history: %v", err)
	}
}

func resolveChatHistoryPath() string {
	if path := os.Getenv("OPENCHAT_CHAT_HISTORY_FILE"); path != "" {
		return path
	}
	return filepath.Join("data", "chat-history.json")
}

func newMessage(eventType EventType, username, text string) Message {
	now := time.Now()
	return Message{
		Type:      eventType,
		Username:  username,
		Message:   text,
		Time:      now.Format("15:04:05"),
		Timestamp: now.UnixMilli(),
	}
}
