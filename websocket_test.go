package main

import (
	"net/http/httptest"
	"testing"
)

func TestCheckWebSocketOriginAllowsSameHost(t *testing.T) {
	req := httptest.NewRequest("GET", "http://localhost:8080/ws", nil)
	req.Header.Set("Origin", "http://localhost:8080")

	if !checkWebSocketOrigin(req) {
		t.Fatal("expected same-host origin to be allowed")
	}
}

func TestCheckWebSocketOriginRejectsDifferentHost(t *testing.T) {
	req := httptest.NewRequest("GET", "http://localhost:8080/ws", nil)
	req.Header.Set("Origin", "http://evil.example")

	if checkWebSocketOrigin(req) {
		t.Fatal("expected different-host origin to be rejected")
	}
}

func TestCheckWebSocketOriginAllowsConfiguredOrigin(t *testing.T) {
	t.Setenv("OPENCHAT_ALLOWED_ORIGINS", "https://chat.example.com, https://app.example.com")

	req := httptest.NewRequest("GET", "http://localhost:8080/ws", nil)
	req.Header.Set("Origin", "https://app.example.com")

	if !checkWebSocketOrigin(req) {
		t.Fatal("expected configured origin to be allowed")
	}
}

func TestCheckWebSocketOriginRejectsMissingOrigin(t *testing.T) {
	req := httptest.NewRequest("GET", "http://localhost:8080/ws", nil)

	if checkWebSocketOrigin(req) {
		t.Fatal("expected missing origin to be rejected")
	}
}
