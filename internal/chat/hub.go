// Package chat moves messages between WebSocket connections and the store.
package chat

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/Schenna43lp1/openchat/internal/store"
)

const (
	storeTimeout = 5 * time.Second
	sendBuffer   = 256
	timeFormat   = "15:04:05"
)

// EventType names an event of the WebSocket protocol.
type EventType string

// The events sent to browsers.
const (
	EventMessage EventType = "message"
	EventDirect  EventType = "direct"
	EventSystem  EventType = "system"
	EventUsers   EventType = "users"
	EventHistory EventType = "history"
	EventError   EventType = "error"
)

// Event is one frame from the server.
type Event struct {
	Type      EventType `json:"type"`
	Username  string    `json:"username,omitempty"`
	To        string    `json:"to,omitempty"`
	Message   string    `json:"message,omitempty"`
	Time      string    `json:"time,omitempty"`
	Users     []string  `json:"users,omitempty"`
	Messages  []Message `json:"messages,omitempty"`
	Timestamp int64     `json:"timestamp,omitempty"`
}

// Message is one entry of a history event.
type Message struct {
	Type      EventType `json:"type"`
	Username  string    `json:"username,omitempty"`
	To        string    `json:"to,omitempty"`
	Message   string    `json:"message"`
	Time      string    `json:"time"`
	Timestamp int64     `json:"timestamp"`
}

func toMessage(m store.Message) Message {
	out := Message{Type: EventMessage, Username: m.Sender, Message: m.Body}
	if m.Kind == store.KindDirect {
		out.Type, out.To = EventDirect, m.Recipient
	}
	if !m.CreatedAt.IsZero() {
		out.Time, out.Timestamp = m.CreatedAt.Format(timeFormat), m.CreatedAt.UnixMilli()
	}
	return out
}

func textEvent(kind EventType, text string) Event {
	now := time.Now()
	return Event{Type: kind, Message: text, Time: now.Format(timeFormat), Timestamp: now.UnixMilli()}
}

type notice struct {
	client *client
	kind   EventType
	text   string
}

// Hub owns the connections. One goroutine changes them, and it never waits for a disk write:
// messages are stored by the connection that sends them and published afterwards.
type Hub struct {
	store  *store.Store
	logger *slog.Logger

	register   chan *client
	unregister chan *client
	publish    chan store.Message
	notices    chan notice
	done       chan struct{} // closed when Run stops accepting work

	clients  map[*client]bool
	departed []store.User

	mu      sync.Mutex // guards closed, so that nothing is added to running while Run waits for it
	closed  bool
	running sync.WaitGroup // connections
}

// NewHub returns a hub that works once Run is called.
func NewHub(st *store.Store, logger *slog.Logger) *Hub {
	return &Hub{
		store:      st,
		logger:     logger,
		register:   make(chan *client),
		unregister: make(chan *client),
		publish:    make(chan store.Message),
		notices:    make(chan notice),
		done:       make(chan struct{}),
		clients:    make(map[*client]bool),
	}
}

// Run works until ctx ends. It then closes every connection and returns when their goroutines have ended,
// so the store may be closed afterwards. It must be called exactly once.
func (h *Hub) Run(ctx context.Context) {
	defer h.shutdown()
	for {
		// Announce outside the send loops, which may drop clients themselves.
		h.announceDeparted()

		select {
		case c := <-h.register:
			h.add(ctx, c)

		case c := <-h.unregister:
			if h.clients[c] {
				h.remove(c)
				h.logger.Info("client left", "user", c.user.Username)
			}

		case m := <-h.publish:
			h.deliver(m)

		case n := <-h.notices:
			// Only the hub may write to send, because it is the one closing that channel.
			if h.clients[n.client] {
				h.send(n.client, textEvent(n.kind, n.text))
			}

		case <-ctx.Done():
			return
		}
	}
}

func (h *Hub) shutdown() {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.logger.Info("hub shutting down", "connections", len(h.clients))
	close(h.done) // every connection closes itself
	h.running.Wait()
}

func (h *Hub) add(ctx context.Context, c *client) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	history, err := h.store.History(ctx, c.user.ID)
	if err != nil {
		h.logger.Error("load history", "user", c.user.Username, "err", err)
		close(c.send) // the browser reconnects
		return
	}

	event := Event{Type: EventHistory, Messages: make([]Message, 0, len(history))}
	for _, m := range history {
		event.Messages = append(event.Messages, toMessage(m))
		// A message can be stored before this query and published after it. This keeps it from showing twice.
		c.after = max(c.after, m.ID)
	}

	arrived := !h.online(c.user.ID)
	h.clients[c] = true
	h.logger.Info("client joined", "user", c.user.Username)
	h.send(c, event)
	if arrived {
		h.sendAll(textEvent(EventSystem, c.user.Username+" hat den Chat betreten."))
	}
	h.broadcastUsers()
}

func (h *Hub) deliver(m store.Message) {
	out := toMessage(m)
	event := Event{Type: out.Type, Username: out.Username, To: out.To, Message: out.Message, Time: out.Time, Timestamp: out.Timestamp}

	recipientOnline := false
	for c := range h.clients {
		if m.Kind == store.KindDirect {
			if c.user.ID == m.RecipientID {
				recipientOnline = true
			} else if c.user.ID != m.SenderID {
				continue
			}
		}
		if m.ID > c.after {
			h.send(c, event)
		}
	}
	if m.Kind != store.KindDirect || recipientOnline {
		return
	}
	offline := textEvent(EventSystem, "Benutzer ist offline. Die Direktnachricht steht im Verlauf, bis neuere Nachrichten sie verdraengen.")
	for c := range h.clients {
		if c.user.ID == m.SenderID {
			h.send(c, offline)
		}
	}
}

func (h *Hub) online(userID int64) bool {
	for c := range h.clients {
		if c.user.ID == userID {
			return true
		}
	}
	return false
}

func (h *Hub) remove(c *client) {
	delete(h.clients, c)
	close(c.send)
	h.departed = append(h.departed, c.user)
}

// announceDeparted reports users whose last connection is gone. Join and leave notices are live only.
func (h *Hub) announceDeparted() {
	for len(h.departed) > 0 {
		user := h.departed[0]
		h.departed = h.departed[1:]
		if h.online(user.ID) {
			continue
		}
		h.sendAll(textEvent(EventSystem, user.Username+" hat den Chat verlassen."))
		h.broadcastUsers()
	}
}

func (h *Hub) broadcastUsers() {
	users := make([]string, 0, len(h.clients))
	for c := range h.clients {
		users = append(users, c.user.Username)
	}
	slices.Sort(users)
	h.sendAll(Event{Type: EventUsers, Users: slices.Compact(users)}) // one entry for several tabs
}

func (h *Hub) sendAll(event Event) {
	for c := range h.clients {
		h.send(c, event)
	}
}

// send never blocks: a client that cannot keep up is dropped.
func (h *Hub) send(c *client, event Event) {
	select {
	case c.send <- event:
	default:
		h.remove(c)
		if c.conn != nil {
			_ = c.conn.Close()
		}
		h.logger.Warn("dropped slow client", "user", c.user.Username)
	}
}

// The methods below are called by connections and return once the hub has stopped.

func (h *Hub) join(c *client) bool {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return false
	}
	h.running.Add(1)
	h.mu.Unlock()

	select {
	case h.register <- c:
		return true
	case <-h.done:
		h.running.Done()
		return false
	}
}

func (h *Hub) leave(c *client) {
	select {
	case h.unregister <- c:
	case <-h.done:
	}
}

func (h *Hub) post(m store.Message) {
	select {
	case h.publish <- m:
	case <-h.done:
	}
}

func (h *Hub) notify(c *client, kind EventType, text string) {
	select {
	case h.notices <- notice{client: c, kind: kind, text: text}:
	case <-h.done:
	}
}
