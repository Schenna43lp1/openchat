package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

const (
	authCookieName  = "openchat_session"
	sessionDuration = 24 * time.Hour
	usersFile       = "data/users.json"

	minPasswordRunes = 8
	maxPasswordBytes = 72 // bcrypt does not accept more
)

type UserRole string

const (
	RoleAdmin     UserRole = "admin"
	RoleModerator UserRole = "moderator"
	RoleUser      UserRole = "user"
)

var (
	errInvalidCredentials = errors.New("invalid credentials")
	errUsernameTaken      = errors.New("username already exists")
	errInvalidUsername    = errors.New("invalid username")
	errInvalidPassword    = errors.New("invalid password")
	errInvalidRole        = errors.New("invalid role")
	errUnknownUser        = errors.New("unknown user")
	errLastAdmin          = errors.New("cannot change last admin")
	errUserBanned         = errors.New("user is banned")
	errAdminConflict      = errors.New("admin name belongs to an account with another password or a ban")
	errSaveFailed         = errors.New("save users failed")
	usernamePattern       = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)
)

type contextKey string

const currentUserContextKey contextKey = "currentUser"

type authUser struct {
	Username     string   `json:"username"`
	PasswordHash string   `json:"passwordHash"`
	Role         UserRole `json:"role"`
	Banned       bool     `json:"banned,omitempty"`
	CreatedAt    string   `json:"createdAt"`
}

type currentUser struct {
	Username string
	Role     UserRole
}

// staffRequired grants access to admins and moderators.
func staffRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(currentUserContextKey).(currentUser)
		if !ok || !isStaffRole(user.Role) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// unknownUserHash is compared for unknown names, so that they take as long as a wrong password.
var unknownUserHash = sync.OnceValues(func() ([]byte, error) {
	return bcrypt.GenerateFromPassword([]byte("openchat-unknown-user"), bcrypt.DefaultCost)
})

type UserStore struct {
	compare func(hash, password []byte) error
	dummy   []byte
	mu      sync.RWMutex
	path    string
	format  userStoreFormat
	db      *sql.DB
	users   map[string]authUser
}

// userStoreFormat defines the persistence backend for users.
type userStoreFormat string

const (
	userStoreJSON   userStoreFormat = "json"
	userStoreSQLite userStoreFormat = "sqlite"
)

func isStaffRole(role UserRole) bool {
	return role == RoleAdmin || role == RoleModerator
}

// NewUserStore initializes the selected backend and loads users. It grants no roles.
func NewUserStore(path string) (*UserStore, error) {
	dummy, err := unknownUserHash()
	if err != nil {
		return nil, fmt.Errorf("prepare login check: %w", err)
	}
	store := &UserStore{
		compare: bcrypt.CompareHashAndPassword,
		dummy:   dummy,
		path:    path,
		format:  detectUserStoreFormat(path),
		users:   make(map[string]authUser),
	}
	if err := store.initStorage(); err != nil {
		return nil, err
	}
	if err := store.load(); err != nil {
		_ = store.closeStorage()
		return nil, err
	}
	return store, nil
}

// Register validates credentials, hashes password and persists the new account.
func (s *UserStore) Register(username, password string) error {
	username = normalizeAuthUsername(username)
	if !validAuthUsername(username) {
		return errInvalidUsername
	}
	if !validPassword(password) {
		return errInvalidPassword
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := strings.ToLower(username)
	if _, exists := s.users[key]; exists {
		return errUsernameTaken
	}

	role := RoleUser
	s.users[key] = authUser{
		Username:     username,
		PasswordHash: string(hash),
		Role:         role,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	if err := s.saveLocked(); err != nil {
		delete(s.users, key)
		logger.Error("save registration", "user", username, "err", err)
		return err
	}
	logger.Info("user registered", "user", username, "role", role)
	return nil
}

// Authenticate validates password and blocks banned accounts from logging in.
func (s *UserStore) Authenticate(username, password string) (authUser, error) {
	username = normalizeAuthUsername(username)

	s.mu.RLock()
	user, ok := s.users[strings.ToLower(username)]
	s.mu.RUnlock()
	if !ok {
		_ = s.compare(s.dummy, []byte(password)) // only for the time it takes
		logger.Warn("login failed", "user", username, "reason", "unknown user")
		return authUser{}, errInvalidCredentials
	}

	if err := s.compare([]byte(user.PasswordHash), []byte(password)); err != nil {
		logger.Warn("login failed", "user", username, "reason", "wrong password")
		return authUser{}, errInvalidCredentials
	}
	if user.Banned {
		logger.Warn("login failed", "user", username, "reason", "banned")
		return authUser{}, errUserBanned
	}

	logger.Info("login succeeded", "user", user.Username, "role", user.Role)
	return user, nil
}

func (s *UserStore) Find(username string) (authUser, bool) {
	username = normalizeAuthUsername(username)

	s.mu.RLock()
	defer s.mu.RUnlock()

	user, ok := s.users[strings.ToLower(username)]
	return user, ok
}

func (s *UserStore) List() []authUser {
	s.mu.RLock()
	defer s.mu.RUnlock()

	users := make([]authUser, 0, len(s.users))
	for _, user := range s.users {
		users = append(users, user)
	}

	sortUsers(users)
	return users
}

// SetRole updates a user's role while protecting the last active admin.
func (s *UserStore) SetRole(username string, role UserRole) error {
	if !validRole(role) {
		return errInvalidRole
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := strings.ToLower(normalizeAuthUsername(username))
	user, ok := s.users[key]
	if !ok {
		return errUnknownUser
	}

	if user.Role == RoleAdmin && !user.Banned && role != RoleAdmin && s.activeAdminCountLocked() <= 1 {
		logger.Warn("demotion of last admin refused", "user", username)
		return errLastAdmin
	}

	previous := user
	oldRole := user.Role
	user.Role = role
	s.users[key] = user
	if err := s.saveLocked(); err != nil {
		s.users[key] = previous
		logger.Error("save role change", "user", username, "err", err)
		return err
	}
	logger.Info("role changed", "user", username, "from", oldRole, "to", role)
	return nil
}

// SetBanned toggles account ban status while preserving at least one active admin.
func (s *UserStore) SetBanned(username string, banned bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := strings.ToLower(normalizeAuthUsername(username))
	user, ok := s.users[key]
	if !ok {
		return errUnknownUser
	}

	if user.Role == RoleAdmin && !user.Banned && banned && s.activeAdminCountLocked() <= 1 {
		logger.Warn("ban of last admin refused", "user", username)
		return errLastAdmin
	}

	previous := user
	oldState := user.Banned
	user.Banned = banned
	s.users[key] = user
	if err := s.saveLocked(); err != nil {
		s.users[key] = previous
		logger.Error("save ban change", "user", username, "err", err)
		return err
	}
	logger.Info("ban changed", "user", username, "from", oldState, "to", banned)
	return nil
}

// Close releases backend resources (important for SQLite file handles).
func (s *UserStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeStorage()
}

// load dispatches to the configured storage backend.
func (s *UserStore) load() error {
	switch s.format {
	case userStoreSQLite:
		return s.loadSQLite()
	default:
		return s.loadJSON()
	}
}

func (s *UserStore) loadJSON() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read users: %w", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}

	var users []authUser
	if err := json.Unmarshal(raw, &users); err != nil {
		return fmt.Errorf("parse users: %w", err)
	}

	for _, user := range users {
		if user.Username == "" || user.PasswordHash == "" {
			continue
		}
		if !validRole(user.Role) {
			user.Role = RoleUser
		}
		s.users[strings.ToLower(user.Username)] = user
	}

	return nil
}

// EnsureAdmin applies the operator's bootstrap credentials while no active admin exists.
// An existing account is only promoted with its own password, so registering the name first gains nothing.
func (s *UserStore) EnsureAdmin(username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.activeAdminCountLocked() > 0 {
		return nil
	}
	username = normalizeAuthUsername(username)
	if !validAuthUsername(username) {
		return errInvalidUsername
	}
	key := strings.ToLower(username)
	account, exists := s.users[key]
	if exists {
		if account.Banned || bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(password)) != nil {
			return errAdminConflict
		}
	} else {
		if !validPassword(password) {
			return errInvalidPassword
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		account = authUser{Username: username, PasswordHash: string(hash), CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	}

	previous := account
	account.Role = RoleAdmin
	s.users[key] = account
	if err := s.saveLocked(); err != nil {
		if exists {
			s.users[key] = previous
		} else {
			delete(s.users, key)
		}
		return err
	}
	return nil
}

// HasActiveAdmin reports whether anybody can manage this instance.
func (s *UserStore) HasActiveAdmin() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeAdminCountLocked() > 0
}

func (s *UserStore) saveLocked() error {
	var err error
	switch s.format {
	case userStoreSQLite:
		err = s.saveSQLiteLocked()
	default:
		err = s.saveJSONLocked()
	}
	if err != nil {
		return fmt.Errorf("%w: %w", errSaveFailed, err)
	}
	return nil
}

func (s *UserStore) saveJSONLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	users := make([]authUser, 0, len(s.users))
	for _, user := range s.users {
		users = append(users, user)
	}

	raw, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return fmt.Errorf("encode users: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write users: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace users: %w", err)
	}

	return nil
}

func (s *UserStore) initStorage() error {
	if s.format != userStoreSQLite {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		return fmt.Errorf("open sqlite users db: %w", err)
	}

	// Schema is created lazily so switching to SQLite requires no manual migration step.
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			username TEXT PRIMARY KEY COLLATE NOCASE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL,
			banned INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		)
	`); err != nil {
		_ = db.Close()
		return fmt.Errorf("create sqlite users schema: %w", err)
	}

	s.db = db
	return nil
}

func (s *UserStore) closeStorage() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// loadSQLite reads all users into the in-memory map used by the application.
func (s *UserStore) loadSQLite() error {
	if s.db == nil {
		return errors.New("sqlite store not initialized")
	}

	rows, err := s.db.Query(`SELECT username, password_hash, role, banned, created_at FROM users`)
	if err != nil {
		return fmt.Errorf("query sqlite users: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var user authUser
		var banned int
		if err := rows.Scan(&user.Username, &user.PasswordHash, &user.Role, &banned, &user.CreatedAt); err != nil {
			return fmt.Errorf("scan sqlite user: %w", err)
		}
		if user.Username == "" || user.PasswordHash == "" {
			continue
		}
		if !validRole(user.Role) {
			user.Role = RoleUser
		}
		user.Banned = banned != 0
		s.users[strings.ToLower(user.Username)] = user
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sqlite users: %w", err)
	}
	return nil
}

// saveSQLiteLocked replaces the table content from the in-memory source of truth.
func (s *UserStore) saveSQLiteLocked() error {
	if s.db == nil {
		return errors.New("sqlite store not initialized")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin sqlite transaction: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.Exec(`DELETE FROM users`); err != nil {
		return fmt.Errorf("clear sqlite users: %w", err)
	}

	stmt, err := tx.Prepare(`INSERT INTO users (username, password_hash, role, banned, created_at) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare sqlite insert: %w", err)
	}
	defer stmt.Close()

	for _, user := range s.users {
		banned := 0
		if user.Banned {
			banned = 1
		}
		if _, err := stmt.Exec(user.Username, user.PasswordHash, string(user.Role), banned, user.CreatedAt); err != nil {
			return fmt.Errorf("insert sqlite user: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite transaction: %w", err)
	}
	tx = nil
	return nil
}

// detectUserStoreFormat selects SQLite by extension, JSON otherwise.
func detectUserStoreFormat(path string) userStoreFormat {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".db", ".sqlite", ".sqlite3":
		return userStoreSQLite
	default:
		return userStoreJSON
	}
}

type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]session
}

type session struct {
	Username  string
	ExpiresAt time.Time
}

func NewSessionManager() *SessionManager {
	return &SessionManager{sessions: make(map[string]session)}
}

// Create stores a server-side session and sends its token as HttpOnly cookie.
func (m *SessionManager) Create(w http.ResponseWriter, username string) error {
	token, err := randomToken(32)
	if err != nil {
		logger.Error("create session token", "user", username, "err", err)
		return err
	}

	expires := time.Now().Add(sessionDuration)
	m.mu.Lock()
	m.sessions[token] = session{Username: username, ExpiresAt: expires}
	m.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(sessionDuration.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   true,
	})

	logger.Debug("session created", "user", username, "expires", expires)
	return nil
}

// Username resolves and validates the current cookie session.
func (m *SessionManager) Username(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(authCookieName)
	if err != nil || cookie.Value == "" {
		if err != nil && !errors.Is(err, http.ErrNoCookie) {
			logger.Warn("read session cookie", "err", err)
		}
		return "", false
	}

	now := time.Now()
	m.mu.RLock()
	current, ok := m.sessions[cookie.Value]
	m.mu.RUnlock()
	if !ok || now.After(current.ExpiresAt) {
		if ok {
			logger.Info("session expired", "user", current.Username)
			m.Delete(cookie.Value)
		}
		return "", false
	}

	return current.Username, true
}

func (m *SessionManager) Clear(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(authCookieName); err == nil {
		m.Delete(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   true,
	})
}

// Active reports whether the token still belongs to a live, unexpired session.
func (m *SessionManager) Active(token string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	current, ok := m.sessions[token]
	return ok && !time.Now().After(current.ExpiresAt)
}

func (m *SessionManager) Delete(token string) {
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

func (s *UserStore) activeAdminCountLocked() int {
	count := 0
	for _, user := range s.users {
		if user.Role == RoleAdmin && !user.Banned {
			count++
		}
	}
	return count
}

func loginHandler(page loginPage, users *UserStore, sessions *SessionManager, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login" {
			http.NotFound(w, r)
			return
		}

		if username, ok := sessions.Username(r); ok && username != "" && r.Method == http.MethodGet {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		switch r.Method {
		case http.MethodGet:
			page.render(w, http.StatusOK, loginViewData{})
		case http.MethodPost:
			handleLoginPost(w, r, page, users, sessions, logger)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func logoutHandler(sessions *SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		sessions.Clear(w, r)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// authRequired enforces login and injects the current user into request context.
func authRequired(sessions *SessionManager, users *UserStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// These routes are called by scripts, which cannot act on a redirect to the login page.
		wantsStatus := r.URL.Path == "/ws" || r.URL.Path == "/session"
		username, ok := sessions.Username(r)
		if !ok {
			if wantsStatus {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		user, ok := users.Find(username)
		if !ok {
			sessions.Clear(w, r)
			if wantsStatus {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if user.Banned {
			sessions.Clear(w, r)
			if wantsStatus {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		ctx := context.WithValue(r.Context(), currentUserContextKey, currentUser{
			Username: user.Username,
			Role:     user.Role,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type loginViewData struct {
	Username          string
	Error             string
	AllowRegistration bool
}

type loginPage struct {
	tmpl              *template.Template
	allowRegistration bool
	trustedProxies    []netip.Prefix
	logins            *rateLimiter
	registrations     *rateLimiter
}

func handleLoginPost(w http.ResponseWriter, r *http.Request, page loginPage, users *UserStore, sessions *SessionManager, logger *slog.Logger) {
	if !parseForm(w, r) {
		return
	}

	mode := r.FormValue("mode")
	username := normalizeAuthUsername(r.FormValue("username"))
	password := r.FormValue("password")

	switch mode {
	case "register":
		if !page.allowRegistration {
			page.render(w, http.StatusForbidden, loginViewData{Username: username, Error: "Die Registrierung ist geschlossen."})
			return
		}
		if !page.admit(w, r, page.registrations, username) {
			return
		}
		if err := users.Register(username, password); err != nil {
			page.render(w, http.StatusOK, loginViewData{Username: username, Error: authErrorMessage(err)})
			return
		}
	case "login", "":
		if !page.admit(w, r, page.logins, username) {
			return
		}
		user, err := users.Authenticate(username, password)
		if err != nil {
			page.render(w, http.StatusOK, loginViewData{Username: username, Error: authErrorMessage(err)})
			return
		}
		username = user.Username
	default:
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if err := sessions.Create(w, username); err != nil {
		logger.Error("create session", "user", username, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	logger.Debug("signed in", "user", username, "mode", mode)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (p loginPage) render(w http.ResponseWriter, status int, data loginViewData) {
	data.AllowRegistration = p.allowRegistration
	w.WriteHeader(status)
	_ = p.tmpl.Execute(w, data)
}

// admit applies a rate limit before any expensive work is done.
func (p loginPage) admit(w http.ResponseWriter, r *http.Request, limit *rateLimiter, username string) bool {
	allowed, wait := limit.allow(clientKey(r, p.trustedProxies))
	if allowed {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
	p.render(w, http.StatusTooManyRequests, loginViewData{Username: username, Error: "Zu viele Versuche. Bitte warte kurz."})
	return false
}

func parseForm(w http.ResponseWriter, r *http.Request) bool {
	err := r.ParseForm()
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
	} else {
		http.Error(w, "bad request", http.StatusBadRequest)
	}
	return false
}

func authErrorMessage(err error) string {
	switch {
	case errors.Is(err, errUsernameTaken):
		return "Dieser Benutzername ist bereits vergeben."
	case errors.Is(err, errInvalidUsername):
		return "Der Benutzername braucht 3 bis 32 Zeichen: Buchstaben, Zahlen, Punkt, Unterstrich oder Bindestrich."
	case errors.Is(err, errInvalidPassword):
		return "Das Passwort braucht mindestens 8 Zeichen und darf hoechstens 72 Byte lang sein (Umlaute zaehlen doppelt, Emojis vierfach)."
	case errors.Is(err, errInvalidRole):
		return "Diese Rolle ist nicht erlaubt."
	case errors.Is(err, errUnknownUser):
		return "Dieser Benutzer wurde nicht gefunden."
	case errors.Is(err, errLastAdmin):
		return "Der letzte aktive Admin kann nicht entfernt oder gesperrt werden."
	case errors.Is(err, errSaveFailed):
		return "Die Aenderung konnte nicht gespeichert werden. Bitte versuche es spaeter erneut."
	case errors.Is(err, errUserBanned):
		return "Dieser Account wurde gesperrt."
	case errors.Is(err, errInvalidCredentials):
		return "Benutzername oder Passwort ist falsch."
	default:
		return "Serverfehler. Bitte versuche es spaeter erneut."
	}
}

func normalizeAuthUsername(username string) string {
	return strings.TrimSpace(username)
}

func validAuthUsername(username string) bool {
	return usernamePattern.MatchString(username)
}

func validPassword(password string) bool {
	return utf8.RuneCountInString(password) >= minPasswordRunes && len(password) <= maxPasswordBytes
}

func validRole(role UserRole) bool {
	switch role {
	case RoleAdmin, RoleModerator, RoleUser:
		return true
	default:
		return false
	}
}

func sortUsers(users []authUser) {
	sort.Slice(users, func(i, j int) bool {
		return strings.ToLower(users[i].Username) < strings.ToLower(users[j].Username)
	})
}

func randomToken(bytesCount int) (string, error) {
	token := make([]byte, bytesCount)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("create token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(token), nil
}
