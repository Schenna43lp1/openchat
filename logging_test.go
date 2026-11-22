// Tests for what ends up in the log.
package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	previous := logger
	logger = slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { logger = previous })
	return &out
}

func TestLogsNeverContainSessionTokens(t *testing.T) {
	out := captureLog(t)
	sessions := NewSessionManager()
	expired, ended := newSessionCookie(t, sessions, "alice"), newSessionCookie(t, sessions, "bob")
	expireSession(sessions, expired.Value)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(expired)
	sessions.Username(req)
	req = httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(ended)
	sessions.Clear(httptest.NewRecorder(), req)

	if strings.Contains(out.String(), expired.Value) || strings.Contains(out.String(), ended.Value) {
		t.Fatalf("token in log: %s", out)
	}
}

func TestRequestPathCannotForgeLogLines(t *testing.T) {
	var out bytes.Buffer
	handler := loggingMiddleware(slog.New(slog.NewTextHandler(&out, nil)), http.NotFoundHandler())
	req := httptest.NewRequest(http.MethodGet, "/x%0Alevel=ERROR%20msg=forged%1b[31m", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if strings.Count(out.String(), "\n") != 1 || strings.Contains(out.String(), "\x1b") {
		t.Fatalf("log = %q", out.String())
	}
}
