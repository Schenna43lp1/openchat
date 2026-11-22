// Tests for rate limiting and client addresses.
package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	limit := newRateLimiter(3, 10*time.Second)
	limit.now = func() time.Time { return now }

	for i := range 3 {
		if ok, _ := limit.allow("a"); !ok {
			t.Fatalf("event %d of the burst was rejected", i)
		}
	}
	if ok, wait := limit.allow("a"); ok || wait != 10*time.Second {
		t.Fatalf("over the burst: %t, %v", ok, wait)
	}
	if ok, _ := limit.allow("b"); !ok {
		t.Fatal("keys share a budget")
	}
	now = now.Add(10 * time.Second)
	if ok, _ := limit.allow("a"); !ok {
		t.Fatal("budget did not refill")
	}
	if ok, _ := limit.allow("a"); ok {
		t.Fatal("budget refilled more than one event")
	}
	now = now.Add(2 * time.Minute)
	limit.allow("c")
	if len(limit.next) != 1 {
		t.Fatalf("stale keys kept: %v", limit.next)
	}
}

func TestClientKey(t *testing.T) {
	trusted, err := parseTrustedProxies("10.0.0.0/8, 192.0.2.1, ::1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, remote, forwarded, want string }{
		{"direct", "203.0.113.5:1234", "", "203.0.113.5"},
		{"untrusted peer", "203.0.113.5:1234", "198.51.100.7", "203.0.113.5"},
		{"trusted peer", "192.0.2.1:1234", "198.51.100.7", "198.51.100.7"},
		{"forged left entry", "10.1.1.1:1234", "1.1.1.1, 198.51.100.7", "198.51.100.7"},
		{"proxy chain", "10.1.1.1:1234", "198.51.100.7, 10.2.2.2", "198.51.100.7"},
		{"only proxies", "10.1.1.1:1234", "10.2.2.2", "10.2.2.2"},
		{"garbage", "10.1.1.1:1234", "198.51.100.7, garbage", "10.1.1.1"},
		{"mapped address", "[::ffff:10.1.1.1]:1234", "198.51.100.7", "198.51.100.7"},
		{"IPv6 network", "[2001:db8:1:2:3:4:5:6]:1234", "", "2001:db8:1:2::/64"},
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = tc.remote
		if tc.forwarded != "" {
			req.Header.Set("X-Forwarded-For", tc.forwarded)
		}
		if got := clientKey(req, trusted); got != tc.want {
			t.Errorf("%s: key = %q, want %q", tc.name, got, tc.want)
		}
	}
	if _, err := parseTrustedProxies("10.0.0.0/8, proxy.local"); err == nil {
		t.Error("invalid proxy was accepted")
	}
}
