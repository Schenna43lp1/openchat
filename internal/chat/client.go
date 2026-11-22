package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/Schenna43lp1/openchat/internal/store"
)

// MaxMessageRunes is the limit the browser shows, in characters.
const MaxMessageRunes = 4096

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10

	// JSON needs up to 12 bytes for one escaped character. This frame limit leaves room for that and
	// for moderately oversized texts, which get an error event. Anything larger ends the connection.
	maxFrameBytes = 64 * 1024

	// A revoked client is closed within authCheckPeriod + closeWriteWait, even if it is idle or not reading.
	authCheckPeriod = 500 * time.Millisecond
	closeWriteWait  = 200 * time.Millisecond

	// After a rejected frame the socket is not read for a moment, which slows a flood down at the source.
	floodPause = 200 * time.Millisecond
)

// Access is what a connection is allowed to do.
type Access struct {
	// Authorized is called before every event in both directions and periodically while idle.
	// An error says nothing about the session.
	Authorized func(context.Context) (bool, error)
	// AllowMessage consumes the message budget of the account.
	AllowMessage func() bool
}

type client struct {
	hub     *Hub
	conn    *websocket.Conn
	user    store.User
	access  Access
	send    chan Event
	after   int64         // newest message in the history this client got, owned by the hub
	stopped chan struct{} // closed when reading ends
}

type inboundMessage struct {
	Message string `json:"message"`
	// To is a pointer so that a blank recipient is rejected instead of becoming a public message.
	To *string `json:"to,omitempty"`
}

// Serve takes over conn and returns at once.
func (h *Hub) Serve(conn *websocket.Conn, user store.User, access Access) {
	c := &client{
		hub:     h,
		conn:    conn,
		user:    user,
		access:  access,
		send:    make(chan Event, sendBuffer),
		stopped: make(chan struct{}),
	}
	if !h.join(c) {
		c.closeWith(websocket.CloseGoingAway, "server shutdown")
		return
	}
	go c.run()
}

func (c *client) run() {
	defer c.hub.running.Done()

	var helpers sync.WaitGroup
	helpers.Go(c.writePump)
	helpers.Go(c.watch)
	c.readPump()

	close(c.stopped)
	c.hub.leave(c)
	_ = c.conn.Close()
	helpers.Wait()
}

func (c *client) readPump() {
	c.conn.SetReadLimit(maxFrameBytes)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure,
				websocket.CloseNormalClosure, websocket.CloseNoStatusReceived) {
				c.hub.logger.Warn("read websocket", "user", c.user.Username, "err", err)
			}
			return
		}
		if !c.checkAccess() {
			return
		}
		if !c.access.AllowMessage() {
			c.hub.notify(c, EventError, "Du sendest zu schnell. Die Nachricht wurde nicht gesendet.")
			time.Sleep(floodPause)
			continue
		}
		c.handle(raw)
	}
}

func (c *client) handle(raw []byte) {
	var inbound inboundMessage
	// Raw line breaks are tolerated inside strings, although JSON does not allow them.
	if err := json.Unmarshal(bytes.ReplaceAll(raw, []byte{'\n'}, []byte{' '}), &inbound); err != nil {
		c.hub.logger.Warn("invalid message", "user", c.user.Username, "err", err)
		c.hub.notify(c, EventError, "Die Nachricht war ungueltig und wurde nicht gesendet.")
		return
	}
	text := strings.TrimSpace(inbound.Message)
	if text == "" {
		return
	}
	if utf8.RuneCountInString(text) > MaxMessageRunes {
		c.hub.notify(c, EventError, fmt.Sprintf("Die Nachricht ist zu lang (maximal %d Zeichen) und wurde nicht gesendet.", MaxMessageRunes))
		return
	}

	// Not bound to the hub, so that a shutdown lets a started write finish.
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()

	m := store.Message{Kind: store.KindPublic, SenderID: c.user.ID, Sender: c.user.Username, Body: text}
	if inbound.To != nil {
		// Only existing accounts may be addressed, otherwise whoever registers the name later could be targeted.
		// Never shorten the name: a truncated one can match a different account.
		recipient, err := c.hub.store.UserByName(ctx, strings.TrimSpace(*inbound.To))
		if errors.Is(err, store.ErrNotFound) {
			c.hub.notify(c, EventSystem, "Direktnachricht wurde nicht gesendet (Empfaenger unbekannt).")
			return
		}
		if err != nil {
			c.hub.logger.Error("look up recipient", "err", err)
			c.hub.notify(c, EventError, "Serverfehler. Die Nachricht wurde nicht gesendet.")
			return
		}
		m.Kind, m.RecipientID, m.Recipient = store.KindDirect, recipient.ID, recipient.Username
	}

	m.CreatedAt = time.UnixMilli(time.Now().UnixMilli())
	saved, err := c.hub.store.AddMessage(ctx, m)
	if err != nil {
		c.hub.logger.Error("store message", "user", c.user.Username, "err", err)
		c.hub.notify(c, EventError, "Die Nachricht konnte nicht gespeichert werden und wurde nicht gesendet.")
		return
	}
	c.hub.post(saved)
}

func (c *client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case event, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			if !c.checkAccess() {
				return
			}
			if err := c.conn.WriteJSON(event); err != nil {
				select {
				case <-c.stopped: // closed on purpose
				case <-c.hub.done:
				default:
					c.hub.logger.Warn("write websocket", "user", c.user.Username, "err", err)
				}
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.stopped:
			return
		}
	}
}

// watch runs apart from writePump, which can block on a client that stops reading.
func (c *client) watch() {
	ticker := time.NewTicker(authCheckPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopped:
			return
		case <-c.hub.done:
			c.closeWith(websocket.CloseGoingAway, "server shutdown")
			return
		case <-ticker.C:
			if !c.checkAccess() {
				return
			}
		}
	}
}

// checkAccess closes the connection if it may not continue.
func (c *client) checkAccess() bool {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	allowed, err := c.access.Authorized(ctx)
	switch {
	case err != nil:
		c.hub.logger.Error("check session", "user", c.user.Username, "err", err)
		// Not 1008: the session may be fine, so the browser should reconnect.
		c.closeWith(websocket.CloseTryAgainLater, "temporary error")
		return false
	case !allowed:
		// 1008 sends the browser to the login page.
		c.closeWith(websocket.ClosePolicyViolation, "session revoked")
		return false
	default:
		return true
	}
}

// closeWith may be called concurrently, unlike data writes.
func (c *client) closeWith(code int, reason string) {
	// Best effort: the connection is closed even if the close frame cannot be written.
	_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(closeWriteWait))
	_ = c.conn.Close()
}
