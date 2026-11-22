// Limits the open WebSockets of one account.
package main

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	maxConnectionsPerAccount = 10

	// Sent after the upgrade, because browsers cannot read the status of a rejected handshake.
	closeTooManyConnections = 4008
	refusalWriteWait        = 200 * time.Millisecond
)

type connectionLimiter struct {
	mu   sync.Mutex
	open map[string]int
}

func newConnectionLimiter() *connectionLimiter {
	return &connectionLimiter{open: make(map[string]int)}
}

// acquire takes a slot. release may be called more than once.
func (l *connectionLimiter) acquire(key string) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open[key] >= maxConnectionsPerAccount {
		return nil, false
	}
	l.open[key]++
	return sync.OnceFunc(func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.open[key]--; l.open[key] <= 0 {
			delete(l.open, key)
		}
	}), true
}

// refuseConnection starts no goroutine and allocates no send buffer.
func refuseConnection(conn *websocket.Conn) {
	reason := websocket.FormatCloseMessage(closeTooManyConnections, "too many connections")
	_ = conn.WriteControl(websocket.CloseMessage, reason, time.Now().Add(refusalWriteWait))
	_ = conn.Close()
}
