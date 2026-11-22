package web

import (
	"sync"
	"time"
)

// rateLimiter allows burst events at once and one more per interval afterwards, separately per key.
type rateLimiter struct {
	mu        sync.Mutex
	interval  time.Duration
	burst     int
	next      map[string]time.Time // theoretical arrival time of the next event
	lastSweep time.Time
	now       func() time.Time
}

func newRateLimiter(burst int, interval time.Duration) *rateLimiter {
	return &rateLimiter{interval: interval, burst: burst, next: make(map[string]time.Time), now: time.Now}
}

// allow consumes one event or returns how long the caller has to wait.
func (l *rateLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if now.Sub(l.lastSweep) >= time.Minute {
		for k, next := range l.next {
			if !next.After(now) {
				delete(l.next, k)
			}
		}
		l.lastSweep = now
	}

	next := l.next[key]
	if next.Before(now) {
		next = now
	}
	if wait := next.Sub(now) - time.Duration(l.burst-1)*l.interval; wait > 0 {
		return false, wait
	}
	l.next[key] = next.Add(l.interval)
	return true, 0
}

// forget gives key its full budget again.
func (l *rateLimiter) forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.next, key)
}

// refund gives back one event that turned out to be harmless.
func (l *rateLimiter) refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if next, ok := l.next[key]; ok {
		l.next[key] = next.Add(-l.interval)
	}
}
