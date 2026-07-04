package main

import (
	"log"
	"sort"
	"sync"
	"time"
)

const messageHistoryLimit = 100

type EventType string

const (
	EventMessage EventType = "message"
	EventSystem  EventType = "system"
	EventUsers   EventType = "users"
	EventHistory EventType = "history"
)

type ChatEvent struct {
	Type      EventType `json:"type"`
	Username  string    `json:"username,omitempty"`
	Message   string    `json:"message,omitempty"`
	Time      string    `json:"time,omitempty"`
	Users     []string  `json:"users,omitempty"`
	Messages  []Message `json:"messages,omitempty"`
	Timestamp int64     `json:"timestamp,omitempty"`
}

type Message struct {
	Type      EventType `json:"type"`
	Username  string    `json:"username,omitempty"`
	Message   string    `json:"message"`
	Time      string    `json:"time"`
	Timestamp int64     `json:"timestamp"`
}

type Hub struct {
	register   chan *Client
	unregister chan *Client
	broadcast  chan Message
	done       chan struct{}

	clients map[*Client]bool
	history []Message
	logger  *log.Logger

	closeOnce sync.Once
}

func NewHub(logger *log.Logger) *Hub {
	return &Hub{
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan Message, 256),
		done:       make(chan struct{}),
		clients:    make(map[*Client]bool),
		logger:     logger,
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
			h.logger.Printf("client joined: %s", client.username)

			client.send <- ChatEvent{Type: EventHistory, Messages: h.history}
			h.broadcastSystem(client.username + " hat den Chat betreten.")
			h.broadcastUsers()

		case client := <-h.unregister:
			if h.clients[client] {
				delete(h.clients, client)
				close(client.send)
				h.logger.Printf("client left: %s", client.username)
				h.broadcastSystem(client.username + " hat den Chat verlassen.")
				h.broadcastUsers()
			}

		case message := <-h.broadcast:
			h.addToHistory(message)
			h.sendAll(ChatEvent{
				Type:      message.Type,
				Username:  message.Username,
				Message:   message.Message,
				Time:      message.Time,
				Timestamp: message.Timestamp,
			})

		case <-h.done:
			h.logger.Println("hub shutting down")
			for client := range h.clients {
				delete(h.clients, client)
				close(client.send)
				_ = client.conn.Close()
			}
			return
		}
	}
}

func (h *Hub) Close() {
	h.closeOnce.Do(func() {
		close(h.done)
	})
}

func (h *Hub) BroadcastMessage(username, text string) {
	h.broadcast <- newMessage(EventMessage, username, text)
}

func (h *Hub) broadcastSystem(text string) {
	message := newMessage(EventSystem, "", text)
	h.addToHistory(message)
	h.sendAll(ChatEvent{
		Type:      EventSystem,
		Message:   message.Message,
		Time:      message.Time,
		Timestamp: message.Timestamp,
	})
}

func (h *Hub) broadcastUsers() {
	users := make([]string, 0, len(h.clients))
	for client := range h.clients {
		users = append(users, client.username)
	}
	sort.Strings(users)
	h.sendAll(ChatEvent{Type: EventUsers, Users: users})
}

func (h *Hub) sendAll(event ChatEvent) {
	for client := range h.clients {
		select {
		case client.send <- event:
		default:
			delete(h.clients, client)
			close(client.send)
			_ = client.conn.Close()
			h.logger.Printf("dropped slow client: %s", client.username)
		}
	}
}

func (h *Hub) addToHistory(message Message) {
	h.history = append(h.history, message)
	if len(h.history) > messageHistoryLimit {
		h.history = h.history[len(h.history)-messageHistoryLimit:]
	}
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
