package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"

	"github.com/Schenna43lp1/openchat/internal/auth"
	"github.com/Schenna43lp1/openchat/internal/store"
)

func envMap(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.allowRegistration || cfg.insecureCookie || cfg.adminUsername != "" || cfg.port != 8080 {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.dbFile != filepath.Join("data", "openchat.db") ||
		cfg.legacyUsersFile != filepath.Join("data", "users.json") ||
		cfg.legacyHistoryFile != filepath.Join("data", "chat-history.json") {
		t.Fatalf("paths = %+v", cfg)
	}
}

func TestConfigFromEnv(t *testing.T) {
	cfg, err := loadConfig(envMap(map[string]string{
		envPort:              "9090",
		envUsersFile:         filepath.Join("volume", "users.sqlite"),
		envHistoryFile:       filepath.Join("logs", "chat.json"),
		envAllowRegistration: "true",
		envCookieSecure:      "false",
		envTrustedProxies:    "10.0.0.0/8",
		envAllowedOrigins:    "https://chat.example.com,broken",
	}))
	if err != nil {
		t.Fatal(err)
	}
	// The database has to end up on the volume that held the old accounts.
	if cfg.dbFile != filepath.Join("volume", "openchat.db") || cfg.legacyHistoryFile != filepath.Join("logs", "chat.json") {
		t.Fatalf("paths = %+v", cfg)
	}
	if cfg.port != 9090 || !cfg.allowRegistration || !cfg.insecureCookie ||
		len(cfg.trustedProxies) != 1 || len(cfg.allowedOrigins) != 1 || len(cfg.invalidOrigins) != 1 {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestConfigRejectsInvalidValues(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"registration":   {envAllowRegistration: "opne"},
		"cookie":         {envCookieSecure: "no-thanks"},
		"name only":      {envAdminUsername: "root"},
		"password only":  {envAdminPassword: "test-only-password"},
		"port word":      {envPort: "http"},
		"port range":     {envPort: "70000"},
		"proxy":          {envTrustedProxies: "proxy.local"},
		"same as legacy": {envUsersFile: "data/openchat.db"},
	} {
		if _, err := loadConfig(envMap(env)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func startServer(t *testing.T, cfg config) (base string, stop func() error) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- run(ctx, cfg, listener, slog.New(slog.DiscardHandler)) }()

	stopped := false
	stop = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		select {
		case err := <-result:
			return err
		case <-time.After(shutdownTimeout + 5*time.Second):
			t.Fatal("shutdown hangs")
			return nil
		}
	}
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	})

	base = "http://" + listener.Addr().String()
	for range 200 {
		if res, err := http.Get(base + "/healthz"); err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return base, stop
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("server did not become ready")
	return "", nil
}

func TestShutdownWithManyClients(t *testing.T) {
	const clients = 100
	cfg := config{dbFile: filepath.Join(t.TempDir(), "openchat.db")}
	db, err := store.Open(t.Context(), cfg.dbFile)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := auth.New(db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	tokens := make([]string, clients)
	for i := range clients {
		user, err := db.CreateUser(t.Context(), store.User{Username: "user" + string(rune('A'+i/26)) + string(rune('a'+i%26)), PasswordHash: "unused", Role: store.RoleUser})
		if err != nil {
			t.Fatal(err)
		}
		if tokens[i], _, err = accounts.StartSession(t.Context(), user); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	base, stop := startServer(t, cfg)
	closed := make(chan error, clients)
	ready := make(chan struct{}, clients)
	for _, token := range tokens {
		conn, res, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(base, "http")+"/ws",
			http.Header{"Origin": {base}, "Cookie": {"openchat_session=" + token}})
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		t.Cleanup(func() { _ = conn.Close() })
		go func() {
			first := true
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					closed <- err
					return
				}
				if first {
					ready <- struct{}{}
					first = false
				}
			}
		}()
	}
	for range clients {
		select {
		case <-ready:
		case err := <-closed:
			t.Fatalf("client lost before the shutdown: %v", err)
		case <-time.After(20 * time.Second):
			t.Fatal("clients did not become ready")
		}
	}

	began := time.Now()
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > shutdownTimeout {
		t.Fatalf("shutdown took %v", took)
	}
	for range clients {
		select {
		case err := <-closed:
			if !websocket.IsCloseError(err, websocket.CloseGoingAway) {
				t.Fatalf("client ended with %v, want close code 1001", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("client was not closed")
		}
	}
}

func TestUpgradeKeepsAccountsAndSessions(t *testing.T) {
	dir := t.TempDir()
	const password = "test-only-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(envMap(map[string]string{
		envUsersFile:   filepath.Join(dir, "users.json"),
		envHistoryFile: filepath.Join(dir, "chat-history.json"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	cfg.insecureCookie = true // the test speaks plain HTTP
	users := `[{"username":"root","passwordHash":"` + string(hash) + `","role":"admin","createdAt":"2026-01-02T03:04:05Z"}]`
	if err := os.WriteFile(cfg.legacyUsersFile, []byte(users), 0o600); err != nil {
		t.Fatal(err)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	expectAdmin := func(res *http.Response, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK || res.Request.URL.Path == "/login" {
			t.Fatalf("got %d at %s", res.StatusCode, res.Request.URL.Path)
		}
	}

	base, stop := startServer(t, cfg)
	expectAdmin(client.PostForm(base+"/login", url.Values{"username": {"root"}, "password": {password}}))
	if err := stop(); err != nil {
		t.Fatal(err)
	}

	base, _ = startServer(t, cfg)
	expectAdmin(client.Get(base + "/admin/users"))
}

func TestRunRefusesDamagedLegacyData(t *testing.T) {
	dir := t.TempDir()
	cfg := config{dbFile: filepath.Join(dir, "openchat.db"), legacyHistoryFile: filepath.Join(dir, "chat-history.json")}
	if err := os.WriteFile(cfg.legacyHistoryFile, []byte("[{"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), cfg, listener, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("damaged history was accepted")
	}
}
