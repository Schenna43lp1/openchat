package main

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 4096
)

var newline = []byte{'\n'}

type Client struct {
	hub      *Hub
	conn     *websocket.Conn
	send     chan ChatEvent
	username string
	logger   *log.Logger
}

type inboundMessage struct {
	Message string `json:"message"`
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, rawMessage, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.logger.Printf("read websocket: %v", err)
			}
			break
		}

		rawMessage = bytes.TrimSpace(bytes.ReplaceAll(rawMessage, newline, []byte{' '}))
		var inbound inboundMessage
		if err := json.Unmarshal(rawMessage, &inbound); err != nil {
			c.logger.Printf("invalid message from %s: %v", c.username, err)
			continue
		}

		text := sanitizeMessage(inbound.Message)
		if text == "" {
			continue
		}

		c.hub.BroadcastMessage(c.username, text)
	}
}

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

			if err := c.conn.WriteJSON(event); err != nil {
				c.logger.Printf("write websocket: %v", err)
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

func sanitizeMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > maxMessageSize {
		message = message[:maxMessageSize]
	}
	return message
}
