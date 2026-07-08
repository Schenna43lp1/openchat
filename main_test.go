package main

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// By default the application should keep using the legacy JSON location.
func TestResolveUsersStorePathDefault(t *testing.T) {
	t.Setenv("OPENCHAT_USERS_FILE", "")
	if got := resolveUsersStorePath(); got != usersFile {
		t.Fatalf("resolveUsersStorePath() = %q, want %q", got, usersFile)
	}
}

// OPENCHAT_USERS_FILE must override the default store path.
func TestResolveUsersStorePathFromEnv(t *testing.T) {
	t.Setenv("OPENCHAT_USERS_FILE", "data/users.sqlite")
	if got := resolveUsersStorePath(); got != "data/users.sqlite" {
		t.Fatalf("resolveUsersStorePath() = %q, want %q", got, "data/users.sqlite")
	}
}

func TestResolveChatHistoryPathDefault(t *testing.T) {
	t.Setenv("OPENCHAT_CHAT_HISTORY_FILE", "")
	if got := resolveChatHistoryPath(); got != filepath.Join("data", "chat-history.json") {
		t.Fatalf("resolveChatHistoryPath() = %q, want %q", got, filepath.Join("data", "chat-history.json"))
	}
}

func TestResolveChatHistoryPathFromEnv(t *testing.T) {
	t.Setenv("OPENCHAT_CHAT_HISTORY_FILE", "logs/chat.json")
	if got := resolveChatHistoryPath(); got != "logs/chat.json" {
		t.Fatalf("resolveChatHistoryPath() = %q, want %q", got, "logs/chat.json")
	}
}

func TestDirectHandlerRendersTemplate(t *testing.T) {
	tmpl, err := template.ParseFiles("templates/direct.html")
	if err != nil {
		t.Fatalf("parse direct template: %v", err)
	}

	handler := directHandler(tmpl, testLogger(t))
	req := httptest.NewRequest(http.MethodGet, "http://localhost/direct", nil)
	req = req.WithContext(context.WithValue(req.Context(), currentUserContextKey, currentUser{
		Username: "alice",
		Role:     RoleUser,
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Direkt-Chat") {
		t.Fatal("expected direct chat heading in response body")
	}
	if !strings.Contains(body, "data-chat-scope=\"direct\"") {
		t.Fatal("expected direct chat scope marker in response body")
	}
}
