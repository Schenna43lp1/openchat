// Main package for the Open chat application.
// Sets up HTTP server, routes, templates, and graceful shutdown.
// Handles user authentication, session management, and chat functionality.
// Provides the entry point for the application and initializes all necessary components.
// Supports both public and direct messaging between users.
// Designed to be modular and extensible for future enhancements.
// Utilizes Go's standard library for HTTP handling, templating, and logging.
// Entry point: main() initializes templates, user store, session manager, and HTTP server.
// The application supports graceful shutdown and proper resource cleanup.
package main

import (
	"context"
	_ "embed"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	defaultAddr     = ":8080"
	shutdownTimeout = 10 * time.Second
	maxBodyBytes    = 64 << 10

	// Per client address. Registrations are rarer and cost storage, so their budget is smaller.
	loginBurst           = 10
	loginInterval        = 6 * time.Second
	registrationBurst    = 5
	registrationInterval = 12 * time.Minute
	// Per account, over all of its connections.
	messageBurst    = 10
	messageInterval = time.Second
)

var logger = slog.New(slog.DiscardHandler)

func main() {
	logger = slog.New(slog.NewTextHandler(os.Stdout, nil))

	tmpl, err := template.ParseFiles("templates/index.html")
	if err != nil {
		fatal(logger, "parse template", "name", "index", "err", err)
	}

	directTmpl, err := template.ParseFiles("templates/direct.html")
	if err != nil {
		fatal(logger, "parse template", "name", "direct", "err", err)
	}

	loginTmpl, err := template.ParseFiles("templates/login.html")
	if err != nil {
		fatal(logger, "parse template", "name", "login", "err", err)
	}

	adminTmpl, err := template.ParseFiles("templates/admin.html")
	if err != nil {
		fatal(logger, "parse template", "name", "admin", "err", err)
	}

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		fatal(logger, "invalid configuration", "err", err)
	}

	for _, path := range []string{resolveUsersStorePath(), resolveChatHistoryPath()} {
		if err := ensureWritable(path); err != nil {
			fatal(logger, "storage is not writable (on Docker volumes, run chown -R 65532:65532 on the data directory)", "uid", os.Getuid(), "err", err)
		}
	}

	// User storage backend/path is resolved from env and can point to JSON or SQLite.
	users, err := NewUserStore(resolveUsersStorePath())
	if err != nil {
		fatal(logger, "load users", "err", err)
	}
	logger.Info("user store ready", "path", resolveUsersStorePath())
	defer func() {
		if err := users.Close(); err != nil {
			logger.Error("close user store", "err", err)
		}
	}()
	if cfg.adminUsername != "" {
		if err := users.EnsureAdmin(cfg.adminUsername, cfg.adminPassword); err != nil {
			fatal(logger, "create admin from "+envAdminUsername, "err", err)
		}
	}
	if !users.HasActiveAdmin() {
		logger.Warn("no active admin, set " + envAdminUsername + " and " + envAdminPassword + " and restart")
	}
	sessions := NewSessionManager()
	if cfg.insecureCookie {
		sessions.insecureCookie = true
		logger.Warn("session cookie is sent over plain HTTP, use this only in a trusted network")
	}

	// Hub owns websocket clients and message fan-out.
	hub := NewHub(logger, users)
	go hub.Run()

	mux, err := newRouter(appTemplates{index: tmpl, direct: directTmpl, login: loginTmpl, admin: adminTmpl}, users, sessions, hub, cfg, logger)
	if err != nil {
		fatal(logger, "configure routes", "err", err)
	}

	server := &http.Server{
		Addr:              defaultAddr,
		Handler:           loggingMiddleware(logger, mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// WebSockets are not affected, the connection deadlines end with the upgrade.
		WriteTimeout:   30 * time.Second,
		IdleTimeout:    2 * time.Minute,
		MaxHeaderBytes: 64 << 10,
		ErrorLog:       slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	go func() {
		logger.Info("starting http server", "addr", defaultAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal(logger, "listen and serve", "err", err)
		}
	}()

	waitForShutdown(logger, server, hub)
}

func fatal(logger *slog.Logger, message string, args ...any) {
	logger.Error(message, args...)
	os.Exit(1)
}

type appTemplates struct {
	index, direct, login, admin *template.Template
}

// newRouter wires all routes with their access checks.
func newRouter(t appTemplates, users *UserStore, sessions *SessionManager, hub *Hub, cfg config, logger *slog.Logger) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.Handle("/", authRequired(sessions, users, indexHandler(t.index, logger)))
	mux.Handle("/direct", authRequired(sessions, users, directHandler(t.direct, logger)))
	mux.Handle("GET /session", authRequired(sessions, users, http.HandlerFunc(sessionCheckHandler)))
	mux.Handle("/ws", authRequired(sessions, users, serveWebSocket(hub, users, sessions, newRateLimiter(messageBurst, messageInterval), newConnectionLimiter(), logger)))
	mux.Handle("/admin/users", authRequired(sessions, users, staffRequired(adminUsersHandler(t.admin, users, logger))))
	mux.HandleFunc("/login", loginHandler(loginPage{
		tmpl:              t.login,
		allowRegistration: cfg.allowRegistration,
		trustedProxies:    cfg.trustedProxies,
		logins:            newRateLimiter(loginBurst, loginInterval),
		guard:             newLoginGuard(),
		registrations:     newRateLimiter(registrationBurst, registrationInterval),
	}, users, sessions, logger))
	mux.HandleFunc("/logout", logoutHandler(sessions))
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(filesOnly{http.Dir("static")})))
	protected, err := rejectCrossOrigin(http.MaxBytesHandler(mux, maxBodyBytes))
	if err != nil {
		return nil, err
	}
	return securityHeaders(protected), nil
}

// sessionCheckHandler answers behind authRequired. Browsers cannot read the status of a rejected
// WebSocket handshake, so they ask here before they reconnect.
func sessionCheckHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

type indexViewData struct {
	Username       string
	Role           UserRole
	CanManageUsers bool
}

// indexHandler renders the chat UI for authenticated users.
func indexHandler(tmpl *template.Template, logger *slog.Logger) http.Handler {
	return chatPageHandler("/", tmpl, logger)
}

// directHandler renders the direct message UI for authenticated users.
func directHandler(tmpl *template.Template, logger *slog.Logger) http.Handler {
	return chatPageHandler("/direct", tmpl, logger)
}

// chatPageHandler renders the chat page for the given path.
func chatPageHandler(path string, tmpl *template.Template, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}

		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		user, _ := r.Context().Value(currentUserContextKey).(currentUser)
		data := indexViewData{
			Username:       user.Username,
			Role:           user.Role,
			CanManageUsers: isStaffRole(user.Role),
		}
		if err := tmpl.Execute(w, data); err != nil {
			logger.Error("execute template", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	})
}

// loggingMiddleware logs the HTTP requests with method, path, and duration.
func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start).Round(time.Millisecond))
	})
}

// waitForShutdown performs graceful shutdown for HTTP server and websocket hub.
func waitForShutdown(logger *slog.Logger, server *http.Server, hub *Hub) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Info("shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	hub.Close()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		if err := server.Close(); err != nil {
			logger.Error("force close failed", "err", err)
		}
	}

	logger.Info("server stopped")
}

// resolveUsersStorePath allows overriding the default user storage file by env var.
func resolveUsersStorePath() string {
	if path := os.Getenv("OPENCHAT_USERS_FILE"); path != "" {
		return path
	}
	return usersFile
}

// resolveUsersStorePath returns the path to the users storage file, allowing override via environment variable.
