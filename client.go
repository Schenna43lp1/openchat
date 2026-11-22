package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

// WebSocket client implementation for Open chat.
const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10

	maxMessageRunes = 4096
	// JSON needs up to 12 bytes for one escaped character. This frame limit leaves room for that and
	// for moderately oversized texts, which get an error event. Anything larger ends the connection.
	maxFrameBytes = 64 * 1024

	// A revoked client is closed within authCheckPeriod + revokeWriteWait, even if it is idle or not reading.
	authCheckPeriod = 500 * time.Millisecond
	revokeWriteWait = 200 * time.Millisecond

	// After a rejected frame the socket is not read for a moment, which slows a flood down at the source.
	floodPause = 200 * time.Millisecond
)

var newline = []byte{'\n'}

type Client struct {
	hub      *Hub
	conn     *websocket.Conn
	send     chan ChatEvent
	username string
	logger   *slog.Logger

	// authorized is checked before every inbound and outbound event and periodically while idle.
	authorized func() bool
	// allowMessage consumes the message budget of the account.
	allowMessage func() bool
	stopped      chan struct{} // closed when readPump exits
}

type inboundMessage struct {
	Message string `json:"message"`
	// To is a pointer so that a blank recipient is rejected instead of becoming a public message.
	To *string `json:"to,omitempty"`
}

// readPump consumes websocket frames, validates payloads and forwards chat messages to the hub.
func (c *Client) readPump() {
	defer func() {
		close(c.stopped)
		c.hub.unregister <- c
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(maxFrameBytes)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, rawMessage, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.logger.Warn("read websocket", "user", c.username, "err", err)
			}
			break
		}
		if !c.authorized() {
			c.revoke()
			break
		}

		if !c.allowMessage() {
			c.hub.Notify(c, "Du sendest zu schnell. Die Nachricht wurde nicht gesendet.")
			time.Sleep(floodPause)
			continue
		}

		rawMessage = bytes.TrimSpace(bytes.ReplaceAll(rawMessage, newline, []byte{' '}))
		var inbound inboundMessage
		if err := json.Unmarshal(rawMessage, &inbound); err != nil {
			c.logger.Warn("invalid message", "user", c.username, "err", err)
			c.hub.Notify(c, "Die Nachricht war ungueltig und wurde nicht gesendet.")
			continue
		}

		text := strings.TrimSpace(inbound.Message)
		if utf8.RuneCountInString(text) > maxMessageRunes {
			c.hub.Notify(c, fmt.Sprintf("Die Nachricht ist zu lang (maximal %d Zeichen) und wurde nicht gesendet.", maxMessageRunes))
			continue
		}
		if text == "" {
			continue
		}

		if inbound.To != nil {
			// Never shorten the recipient: a truncated name can match a different account.
			recipient := strings.TrimSpace(*inbound.To)
			c.hub.SendDirectMessage(c.username, recipient, text)
			continue
		}

		c.hub.BroadcastMessage(c.username, text)
	}
}

// writePump sends outbound events and periodic ping frames.
func (c *Client) writePump() {
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
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if !c.authorized() {
				c.revoke()
				return
			}

			if err := c.conn.WriteJSON(event); err != nil {
				c.logger.Warn("write websocket", "user", c.username, "err", err)
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// watchAuthorization closes connections whose session or account was revoked.
// It runs apart from writePump, which can block on a client that stops reading.
func (c *Client) watchAuthorization() {
	ticker := time.NewTicker(authCheckPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopped:
			return
		case <-ticker.C:
			if !c.authorized() {
				c.revoke()
				return
			}
		}
	}
}

// revoke closes the socket with the policy code that sends the browser to the login page.
// Unlike data writes, WriteControl and Close may be called concurrently.
func (c *Client) revoke() {
	reason := websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "session revoked")
	// Best effort: the connection is closed even if the close frame cannot be written.
	_ = c.conn.WriteControl(websocket.CloseMessage, reason, time.Now().Add(revokeWriteWait))
	_ = c.conn.Close()
}
