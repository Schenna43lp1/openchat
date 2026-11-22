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
	"io"
	"log"
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

var logger = log.New(io.Discard, "", 0)

func main() {
	logger = log.New(os.Stdout, "chat: ", log.LstdFlags|log.Lshortfile)

	tmpl, err := template.ParseFiles("templates/index.html")
	if err != nil {
		logger.Fatalf("parse template: %v", err)
		logger.Println("exiting due to template parse error")
	}

	directTmpl, err := template.ParseFiles("templates/direct.html")
	if err != nil {
		logger.Fatalf("parse direct template: %v", err)
		logger.Println("exiting due to direct template parse error")
	}

	loginTmpl, err := template.ParseFiles("templates/login.html")
	if err != nil {
		logger.Fatalf("parse login template: %v", err)
		logger.Println("exiting due to login template parse error")
	}

	adminTmpl, err := template.ParseFiles("templates/admin.html")
	if err != nil {
		logger.Fatalf("parse admin template: %v", err)
	}

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		logger.Fatalf("configuration: %v", err)
	}

	for _, path := range []string{resolveUsersStorePath(), resolveChatHistoryPath()} {
		if err := ensureWritable(path); err != nil {
			logger.Fatalf("storage is not writable by uid %d (on Docker volumes, run chown -R 65532:65532 on the data directory): %v", os.Getuid(), err)
		}
	}

	// User storage backend/path is resolved from env and can point to JSON or SQLite.
	logger.Printf("loading user store from %s", resolveUsersStorePath())
	users, err := NewUserStore(resolveUsersStorePath())
	if err != nil {
		logger.Fatalf("load users: %v", err)
	}
	logger.Printf("user store ready: %s", resolveUsersStorePath())
	defer func() {
		if err := users.Close(); err != nil {
			logger.Printf("close user store: %v", err)
		}
	}()
	if cfg.adminUsername != "" {
		if err := users.EnsureAdmin(cfg.adminUsername, cfg.adminPassword); err != nil {
			logger.Fatalf("create admin from %s: %v", envAdminUsername, err)
		}
	}
	if !users.HasActiveAdmin() {
		logger.Printf("no active admin: set %s and %s and restart", envAdminUsername, envAdminPassword)
	}
	sessions := NewSessionManager()

	// Hub owns websocket clients and message fan-out.
	hub := NewHub(logger, users)
	go hub.Run()

	mux, err := newRouter(appTemplates{index: tmpl, direct: directTmpl, login: loginTmpl, admin: adminTmpl}, users, sessions, hub, cfg, logger)
	if err != nil {
		logger.Fatalf("configure routes: %v", err)
	}

	server := &http.Server{
		Addr:              defaultAddr,
		Handler:           loggingMiddleware(logger, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Printf("starting http server on http://localhost%s", defaultAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("listen and serve: %v", err)
		}
	}()

	waitForShutdown(logger, server, hub)
}

type appTemplates struct {
	index, direct, login, admin *template.Template
}

// newRouter wires all routes with their access checks.
func newRouter(t appTemplates, users *UserStore, sessions *SessionManager, hub *Hub, cfg config, logger *log.Logger) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.Handle("/", authRequired(sessions, users, indexHandler(t.index, logger)))
	mux.Handle("/direct", authRequired(sessions, users, directHandler(t.direct, logger)))
	mux.Handle("GET /session", authRequired(sessions, users, http.HandlerFunc(sessionCheckHandler)))
	mux.Handle("/ws", authRequired(sessions, users, serveWebSocket(hub, users, sessions, newRateLimiter(messageBurst, messageInterval), logger)))
	mux.Handle("/admin/users", authRequired(sessions, users, staffRequired(adminUsersHandler(t.admin, users, logger))))
	mux.HandleFunc("/login", loginHandler(loginPage{
		tmpl:              t.login,
		allowRegistration: cfg.allowRegistration,
		trustedProxies:    cfg.trustedProxies,
		logins:            newRateLimiter(loginBurst, loginInterval),
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
func indexHandler(tmpl *template.Template, logger *log.Logger) http.Handler {
	return chatPageHandler("/", tmpl, logger)
}

// directHandler renders the direct message UI for authenticated users.
func directHandler(tmpl *template.Template, logger *log.Logger) http.Handler {
	return chatPageHandler("/direct", tmpl, logger)
}

// chatPageHandler renders the chat page for the given path.
func chatPageHandler(path string, tmpl *template.Template, logger *log.Logger) http.Handler {
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
			logger.Printf("execute template: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	})
}

// loggingMiddleware logs the HTTP requests with method, path, and duration.
func loggingMiddleware(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

// waitForShutdown performs graceful shutdown for HTTP server and websocket hub.
func waitForShutdown(logger *log.Logger, server *http.Server, hub *Hub) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Println("shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	hub.Close()

	if err := server.Shutdown(ctx); err != nil {
		logger.Printf("graceful shutdown failed: %v", err)
		if err := server.Close(); err != nil {
			logger.Printf("force close failed: %v", err)
		}
	}

	logger.Println("server stopped")
	logger.Println("shutdown complete")
}

// resolveUsersStorePath allows overriding the default user storage file by env var.
func resolveUsersStorePath() string {
	if path := os.Getenv("OPENCHAT_USERS_FILE"); path != "" {
		logger.Printf("using custom users store path: %s", path)
		return path
	}
	logger.Printf("using default users store path: %s", usersFile)
	return usersFile
}

// resolveUsersStorePath returns the path to the users storage file, allowing override via environment variable.
