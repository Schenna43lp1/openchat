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
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

const (
	authCookieName  = "openchat_session"
	sessionDuration = 24 * time.Hour
	usersFile       = "data/users.json"
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

type UserStore struct {
	mu     sync.RWMutex
	path   string
	format userStoreFormat
	db     *sql.DB
	users  map[string]authUser
}

type userStoreFormat string

const (
	userStoreJSON   userStoreFormat = "json"
	userStoreSQLite userStoreFormat = "sqlite"
)

func isStaffRole(role UserRole) bool {
	return role == RoleAdmin || role == RoleModerator
}

func NewUserStore(path string) (*UserStore, error) {
	store := &UserStore{
		path:   path,
		format: detectUserStoreFormat(path),
		users:  make(map[string]authUser),
	}
	if err := store.initStorage(); err != nil {
		return nil, err
	}
	if err := store.load(); err != nil {
		_ = store.closeStorage()
		return nil, err
	}
	if err := store.ensureAdmin(); err != nil {
		_ = store.closeStorage()
		return nil, err
	}
	return store, nil
}

func (s *UserStore) Register(username, password string) error {
	username = normalizeAuthUsername(username)
	if !validAuthUsername(username) {
		return errInvalidUsername
	}
	if len(password) < 8 || len(password) > 128 {
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
	if len(s.users) == 0 {
		role = RoleAdmin
	}

	s.users[key] = authUser{
		Username:     username,
		PasswordHash: string(hash),
		Role:         role,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	return s.saveLocked()
}

func (s *UserStore) Authenticate(username, password string) (authUser, error) {
	username = normalizeAuthUsername(username)

	s.mu.RLock()
	user, ok := s.users[strings.ToLower(username)]
	s.mu.RUnlock()
	if !ok {
		return authUser{}, errInvalidCredentials
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return authUser{}, errInvalidCredentials
	}
	if user.Banned {
		return authUser{}, errUserBanned
	}

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
		return errLastAdmin
	}

	user.Role = role
	s.users[key] = user
	return s.saveLocked()
}

func (s *UserStore) SetBanned(username string, banned bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := strings.ToLower(normalizeAuthUsername(username))
	user, ok := s.users[key]
	if !ok {
		return errUnknownUser
	}

	if user.Role == RoleAdmin && !user.Banned && banned && s.activeAdminCountLocked() <= 1 {
		return errLastAdmin
	}

	user.Banned = banned
	s.users[key] = user
	return s.saveLocked()
}

func (s *UserStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeStorage()
}

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

func (s *UserStore) ensureAdmin() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.users) == 0 || s.adminCountLocked() > 0 {
		return nil
	}

	users := make([]authUser, 0, len(s.users))
	for _, user := range s.users {
		users = append(users, user)
	}
	sortUsers(users)

	first := users[0]
	first.Role = RoleAdmin
	s.users[strings.ToLower(first.Username)] = first
	return s.saveLocked()
}

func (s *UserStore) saveLocked() error {
	switch s.format {
	case userStoreSQLite:
		return s.saveSQLiteLocked()
	default:
		return s.saveJSONLocked()
	}
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

func (m *SessionManager) Create(w http.ResponseWriter, username string) error {
	token, err := randomToken(32)
	if err != nil {
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
		Secure:   false,
	})

	return nil
}

func (m *SessionManager) Username(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(authCookieName)
	if err != nil || cookie.Value == "" {
		return "", false
	}

	now := time.Now()
	m.mu.RLock()
	current, ok := m.sessions[cookie.Value]
	m.mu.RUnlock()
	if !ok || now.After(current.ExpiresAt) {
		if ok {
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
	})
}

func (m *SessionManager) Delete(token string) {
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

func (s *UserStore) adminCountLocked() int {
	count := 0
	for _, user := range s.users {
		if user.Role == RoleAdmin {
			count++
		}
	}
	return count
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

func loginHandler(tmpl *template.Template, users *UserStore, sessions *SessionManager, logger *log.Logger) http.HandlerFunc {
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
			renderLogin(w, tmpl, loginViewData{})
		case http.MethodPost:
			handleLoginPost(w, r, tmpl, users, sessions, logger)
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

func authRequired(sessions *SessionManager, users *UserStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, ok := sessions.Username(r)
		if !ok {
			if r.URL.Path == "/ws" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		user, ok := users.Find(username)
		if !ok {
			sessions.Clear(w, r)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if user.Banned {
			sessions.Clear(w, r)
			if r.URL.Path == "/ws" {
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

func adminRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(currentUserContextKey).(currentUser)
		if !ok || user.Role != RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

type loginViewData struct {
	Mode     string
	Username string
	Error    string
}

func handleLoginPost(w http.ResponseWriter, r *http.Request, tmpl *template.Template, users *UserStore, sessions *SessionManager, logger *log.Logger) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	mode := r.FormValue("mode")
	username := normalizeAuthUsername(r.FormValue("username"))
	password := r.FormValue("password")

	switch mode {
	case "register":
		if err := users.Register(username, password); err != nil {
			renderLogin(w, tmpl, loginViewData{Mode: mode, Username: username, Error: authErrorMessage(err)})
			return
		}
	case "login", "":
		user, err := users.Authenticate(username, password)
		if err != nil {
			renderLogin(w, tmpl, loginViewData{Mode: "login", Username: username, Error: authErrorMessage(err)})
			return
		}
		username = user.Username
	default:
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if err := sessions.Create(w, username); err != nil {
		logger.Printf("create session: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func renderLogin(w http.ResponseWriter, tmpl *template.Template, data loginViewData) {
	if data.Mode == "" {
		data.Mode = "login"
	}
	w.WriteHeader(http.StatusOK)
	_ = tmpl.Execute(w, data)
}

func authErrorMessage(err error) string {
	switch {
	case errors.Is(err, errUsernameTaken):
		return "Dieser Benutzername ist bereits vergeben."
	case errors.Is(err, errInvalidUsername):
		return "Der Benutzername braucht 3 bis 32 Zeichen: Buchstaben, Zahlen, Punkt, Unterstrich oder Bindestrich."
	case errors.Is(err, errInvalidPassword):
		return "Das Passwort muss zwischen 8 und 128 Zeichen lang sein."
	case errors.Is(err, errInvalidRole):
		return "Diese Rolle ist nicht erlaubt."
	case errors.Is(err, errUnknownUser):
		return "Dieser Benutzer wurde nicht gefunden."
	case errors.Is(err, errLastAdmin):
		return "Der letzte aktive Admin kann nicht entfernt oder gesperrt werden."
	case errors.Is(err, errUserBanned):
		return "Dieser Account wurde gesperrt."
	default:
		return "Benutzername oder Passwort ist falsch."
	}
}

func normalizeAuthUsername(username string) string {
	return strings.TrimSpace(username)
}

func validAuthUsername(username string) bool {
	return usernamePattern.MatchString(username)
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
