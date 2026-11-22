// Package web serves the pages, the WebSocket endpoint and the assets.
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Schenna43lp1/openchat/internal/auth"
	"github.com/Schenna43lp1/openchat/internal/chat"
	"github.com/Schenna43lp1/openchat/internal/store"
)

//go:embed templates static
var assets embed.FS

const (
	maxBodyBytes = 64 << 10

	// Per client address. Registrations are rarer and cost storage, so their budget is smaller.
	loginBurst           = 10
	loginInterval        = 6 * time.Second
	registrationBurst    = 5
	registrationInterval = 12 * time.Minute
	// Per account, over all of its connections.
	messageBurst    = 10
	messageInterval = time.Second

	healthTimeout = 2 * time.Second
)

// Options are the dependencies and settings of the handler.
type Options struct {
	Auth   *auth.Service
	Hub    *chat.Hub
	Health func(context.Context) error
	Logger *slog.Logger

	AllowRegistration bool
	InsecureCookie    bool // for plain HTTP in a LAN
	TrustedProxies    []netip.Prefix
	AllowedOrigins    []string // from ParseOrigins
}

type server struct {
	Options
	pages         map[string]*template.Template
	upgrader      websocket.Upgrader
	logins        *rateLimiter
	guard         *loginGuard
	connections   *connectionLimiter
	registrations *rateLimiter
	messages      *rateLimiter
	confirmations *rateLimiter
	passwordWork  *rateLimiter
}

// New returns the handler of the whole application.
func New(options Options) (http.Handler, error) {
	s := &server{
		Options:       options,
		pages:         make(map[string]*template.Template),
		logins:        newRateLimiter(loginBurst, loginInterval),
		guard:         newLoginGuard(),
		connections:   newConnectionLimiter(),
		registrations: newRateLimiter(registrationBurst, registrationInterval),
		messages:      newRateLimiter(messageBurst, messageInterval),
		confirmations: newRateLimiter(confirmationBurst, confirmationInterval),
		passwordWork:  newRateLimiter(passwordWorkBurst, passwordWorkInterval),
	}
	s.upgrader = websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 1024, CheckOrigin: s.checkOrigin}

	for _, name := range []string{"chat", "login", "admin", "account"} {
		page, err := template.ParseFS(assets, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", name, err)
		}
		s.pages[name] = page
	}
	static, err := staticHandler()
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/", s.authRequired(s.chatPage("/")))
	mux.Handle("/direct", s.authRequired(s.chatPage("/direct")))
	mux.Handle("GET /session", s.authRequired(http.HandlerFunc(sessionCheck)))
	mux.Handle("/ws", s.authRequired(http.HandlerFunc(s.webSocket)))
	mux.Handle("/admin/users", s.authRequired(staffRequired(http.HandlerFunc(s.adminUsers))))
	mux.Handle(passwordPath, s.authRequired(http.HandlerFunc(s.changePassword)))
	mux.HandleFunc("/login", s.login)
	mux.HandleFunc("/logout", s.logout)
	mux.HandleFunc("GET /healthz", s.health)
	mux.Handle("/static/", static)

	protected, err := rejectCrossOrigin(http.MaxBytesHandler(mux, maxBodyBytes), options.AllowedOrigins)
	if err != nil {
		return nil, err
	}
	return logRequests(options.Logger, securityHeaders(protected)), nil
}

// staticHandler serves files only. Embedded files have no modification time, so they get an ETag.
func staticHandler() (http.Handler, error) {
	files, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	version := sha256.New()
	if err := fs.WalkDir(files, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(files, path)
		if err != nil {
			return err
		}
		_, err = version.Write(raw)
		return err
	}); err != nil {
		return nil, fmt.Errorf("read static files: %w", err)
	}
	tag := `"` + hex.EncodeToString(version.Sum(nil)[:12]) + `"`
	serve := http.StripPrefix("/static/", http.FileServer(filesOnly{http.FS(files)}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", tag)
		serve.ServeHTTP(w, r)
	}), nil
}

// page is what the shared layout needs. Data belongs to the template of the page.
type page struct {
	Title       string
	Description string
	Scope       string
	Data        any
}

func (s *server) render(w http.ResponseWriter, status int, name string, p page) {
	render(w, status, s.pages[name], p, s.Logger)
}

// render executes into a buffer, so that a failure cannot send half a page with status 200.
func render(w http.ResponseWriter, status int, tmpl *template.Template, p page, logger *slog.Logger) {
	var body bytes.Buffer
	if err := tmpl.ExecuteTemplate(&body, "layout", p); err != nil {
		logger.Error("execute template", "page", p.Title, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := body.WriteTo(w); err != nil {
		logger.Debug("write page", "err", err)
	}
}

type chatView struct {
	Username       string
	Role           store.Role
	Direct         bool
	CanManageUsers bool
}

func (s *server) chatPage(path string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		user := userFrom(r.Context())
		view := chatView{Username: user.Username, Role: user.Role, CanManageUsers: user.Role.Staff()}
		p := page{Title: "Open chat", Description: "Oeffentlicher Chat fuer Open chat", Scope: "public"}
		if path == "/direct" {
			view.Direct = true
			p = page{Title: "Direkt-Chat | Open chat", Description: "Direktnachrichten in Open chat", Scope: "direct"}
		}
		p.Data = view
		s.render(w, http.StatusOK, "chat", p)
	})
}

// sessionCheck answers behind authRequired. Browsers cannot read the status of a rejected
// WebSocket handshake, so they ask here before they reconnect.
func sessionCheck(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
	defer cancel()
	if err := s.Health(ctx); err != nil {
		s.Logger.Error("health check", "err", err)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (s *server) webSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	user := userFrom(r.Context())
	key := fmt.Sprint(user.ID)
	// Taken before the upgrade, so that parallel handshakes cannot overrun the limit.
	release, admitted := s.connections.acquire(key)
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		if admitted {
			release()
		}
		s.Logger.Warn("websocket upgrade failed", "user", user.Username, "err", err)
		return
	}
	if !admitted {
		s.Logger.Warn("websocket refused: too many connections", "user", user.Username)
		refuseConnection(conn)
		return
	}
	s.Hub.Serve(conn, user, chat.Access{
		Authorized: s.Auth.Watch(cookie.Value),
		Release:    release,
		AllowMessage: func() bool {
			allowed, _ := s.messages.allow(key)
			return allowed
		},
	})
}

func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/healthz" {
			logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start).Round(time.Millisecond))
		}
	})
}
