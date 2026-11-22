// Tests for what ends up in the log.
package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Schenna43lp1/openchat/internal/store"
)

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func TestLogsNeverContainSessionTokens(t *testing.T) {
	var out lockedBuffer
	logger := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	a := startTestAppWith(t, t.TempDir(), Options{Logger: logger})
	seedAccounts(t, a.users, testAccount(t, "alice", store.RoleUser, false))
	expired, ended := newSessionCookie(t, a.sessions, "alice"), newSessionCookie(t, a.sessions, "alice")
	expireSession(a.sessions, expired.Value)

	a.send(http.MethodGet, "/", "", map[string]string{"Cookie": expired.Name + "=" + expired.Value})
	a.post("/logout", ended.Name+"="+ended.Value, nil)
	a.stop()

	if strings.Contains(out.String(), expired.Value) || strings.Contains(out.String(), ended.Value) {
		t.Fatalf("token in log: %s", out.String())
	}
}

func TestRequestPathCannotForgeLogLines(t *testing.T) {
	var out bytes.Buffer
	handler := logRequests(slog.New(slog.NewTextHandler(&out, nil)), http.NotFoundHandler())
	req := httptest.NewRequest(http.MethodGet, "/x%0Alevel=ERROR%20msg=forged%1b[31m", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if strings.Count(out.String(), "\n") != 1 || strings.Contains(out.String(), "\x1b") {
		t.Fatalf("log = %q", out.String())
	}
}
