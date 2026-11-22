// Unit tests for the WebSocket origin check in the Open chat application.
// Ensures that WebSocket connections are only accepted from allowed origins.
package web

import (
	"net/http/httptest"
	"testing"
)

func TestCheckWebSocketOriginAllowsSameHost(t *testing.T) {
	srv := &server{}
	req := httptest.NewRequest("GET", "http://localhost:8080/ws", nil)
	req.Header.Set("Origin", "http://localhost:8080")

	if !srv.checkOrigin(req) {
		t.Fatal("expected same-host origin to be allowed")
	}
}

func TestCheckWebSocketOriginRejectsDifferentHost(t *testing.T) {
	srv := &server{}
	req := httptest.NewRequest("GET", "http://localhost:8080/ws", nil)
	req.Header.Set("Origin", "http://evil.example")

	if srv.checkOrigin(req) {
		t.Fatal("expected different-host origin to be rejected")
	}
}

func TestCheckWebSocketOriginAllowsConfiguredOrigin(t *testing.T) {
	srv := &server{}
	var invalid []string
	srv.AllowedOrigins, invalid = ParseOrigins("https://chat.example.com, https://App.example.com/, not an origin")
	if len(invalid) != 1 {
		t.Fatalf("invalid = %q", invalid)
	}

	req := httptest.NewRequest("GET", "http://localhost:8080/ws", nil)
	req.Header.Set("Origin", "https://app.example.com")

	if !srv.checkOrigin(req) {
		t.Fatal("expected configured origin to be allowed")
	}
}

func TestCheckWebSocketOriginRejectsMissingOrigin(t *testing.T) {
	srv := &server{}
	req := httptest.NewRequest("GET", "http://localhost:8080/ws", nil)

	if srv.checkOrigin(req) {
		t.Fatal("expected missing origin to be rejected")
	}
}
