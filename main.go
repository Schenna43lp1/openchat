package main

import (
	"context"
	"html/template"
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
)

func main() {
	logger := log.New(os.Stdout, "chat: ", log.LstdFlags|log.Lshortfile)

	tmpl, err := template.ParseFiles("templates/index.html")
	if err != nil {
		logger.Fatalf("parse template: %v", err)
	}

	loginTmpl, err := template.ParseFiles("templates/login.html")
	if err != nil {
		logger.Fatalf("parse login template: %v", err)
	}

	adminTmpl, err := template.ParseFiles("templates/admin.html")
	if err != nil {
		logger.Fatalf("parse admin template: %v", err)
	}

	users, err := NewUserStore(resolveUsersStorePath())
	if err != nil {
		logger.Fatalf("load users: %v", err)
	}
	defer func() {
		if err := users.Close(); err != nil {
			logger.Printf("close user store: %v", err)
		}
	}()
	sessions := NewSessionManager()

	hub := NewHub(logger)
	go hub.Run()

	mux := http.NewServeMux()
	mux.Handle("/", authRequired(sessions, users, indexHandler(tmpl, logger)))
	mux.Handle("/ws", authRequired(sessions, users, serveWebSocket(hub, logger)))
	mux.Handle("/admin/users", authRequired(sessions, users, staffRequired(adminUsersHandler(adminTmpl, users, logger))))
	mux.HandleFunc("/login", loginHandler(loginTmpl, users, sessions, logger))
	mux.HandleFunc("/logout", logoutHandler(sessions))
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	server := &http.Server{
		Addr:              defaultAddr,
		Handler:           loggingMiddleware(logger, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Printf("server listening on http://localhost%s", defaultAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("listen and serve: %v", err)
		}
	}()

	waitForShutdown(logger, server, hub)
}

type indexViewData struct {
	Username       string
	Role           UserRole
	CanManageUsers bool
}

func indexHandler(tmpl *template.Template, logger *log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
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

func loggingMiddleware(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

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
}

func resolveUsersStorePath() string {
	if path := os.Getenv("OPENCHAT_USERS_FILE"); path != "" {
		return path
	}
	return usersFile
}
