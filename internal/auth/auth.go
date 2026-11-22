// Package auth holds the rules for accounts, roles and sessions.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/Schenna43lp1/openchat/internal/store"
)

// Limits for credentials.
const (
	MinPasswordRunes = 8
	MaxPasswordBytes = 72 // bcrypt does not accept more
)

// Errors that are shown to users. A failed write is reported as store.ErrSave.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUsernameTaken      = errors.New("username already exists")
	ErrInvalidUsername    = errors.New("invalid username")
	ErrInvalidPassword    = errors.New("invalid password")
	ErrInvalidRole        = errors.New("invalid role")
	ErrUnknownUser        = errors.New("unknown user")
	ErrLastAdmin          = errors.New("cannot change the last admin")
	ErrBanned             = errors.New("user is banned")
	ErrSelfBan            = errors.New("cannot ban yourself")
	ErrStaffTarget        = errors.New("moderators can only ban users")
	ErrForbidden          = errors.New("role does not allow this")
	ErrAdminConflict      = errors.New("admin name belongs to an account with another password or a ban")
	ErrWrongPassword      = errors.New("wrong password")
	ErrSamePassword       = errors.New("new password equals the current one")
	ErrOperatorOnly       = errors.New("only the operator can reset this account")
	ErrAccountChanged     = errors.New("account changed meanwhile")
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)

// unknownUserHash is compared for unknown names, so that they take as long as a wrong password.
var unknownUserHash = sync.OnceValues(func() ([]byte, error) {
	return bcrypt.GenerateFromPassword([]byte("openchat-unknown-user"), bcrypt.DefaultCost)
})

// Service is safe for concurrent use.
type Service struct {
	store   *store.Store
	logger  *slog.Logger
	compare func(hash, password []byte) error
	dummy   []byte
	// revision changes whenever access may have been revoked, see Watch.
	revision atomic.Uint64
}

// New prepares the service. It grants no roles.
func New(st *store.Store, logger *slog.Logger) (*Service, error) {
	dummy, err := unknownUserHash()
	if err != nil {
		return nil, fmt.Errorf("prepare login check: %w", err)
	}
	return &Service{store: st, logger: logger, compare: bcrypt.CompareHashAndPassword, dummy: dummy}, nil
}

// Register creates an account with the role user, also if it is the first one.
func (s *Service) Register(ctx context.Context, username, password string) (store.User, error) {
	username = strings.TrimSpace(username)
	user, err := newAccount(username, password, store.RoleUser)
	if err != nil {
		return store.User{}, err
	}
	user, err = s.store.CreateUser(ctx, user)
	if errors.Is(err, store.ErrTaken) {
		return store.User{}, ErrUsernameTaken
	}
	if err != nil {
		return store.User{}, err
	}
	s.logger.Info("user registered", "user", user.Username)
	return user, nil
}

func newAccount(username, password string, role store.Role) (store.User, error) {
	if !usernamePattern.MatchString(username) {
		return store.User{}, ErrInvalidUsername
	}
	if !validPassword(password) {
		return store.User{}, ErrInvalidPassword
	}
	hash, err := hashPassword(password)
	if err != nil {
		return store.User{}, err
	}
	return store.User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		CreatedAt:    time.UnixMilli(time.Now().UnixMilli()),
	}, nil
}

func validPassword(password string) bool {
	return utf8.RuneCountInString(password) >= MinPasswordRunes && len(password) <= MaxPasswordBytes
}

// hashPassword is slow on purpose. It must not run inside a transaction, there is only one connection.
func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// Authenticate checks a password. A ban is only revealed to whoever knows the password.
func (s *Service) Authenticate(ctx context.Context, username, password string) (store.User, error) {
	username = strings.TrimSpace(username)
	user, err := s.store.UserByName(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		_ = s.compare(s.dummy, []byte(password)) // only for the time it takes
		s.logger.Warn("login failed", "user", username, "reason", "unknown user")
		return store.User{}, ErrInvalidCredentials
	}
	if err != nil {
		return store.User{}, err
	}
	if s.compare([]byte(user.PasswordHash), []byte(password)) != nil {
		s.logger.Warn("login failed", "user", username, "reason", "wrong password")
		return store.User{}, ErrInvalidCredentials
	}
	if user.Banned {
		s.logger.Warn("login failed", "user", username, "reason", "banned")
		return store.User{}, ErrBanned
	}
	s.logger.Info("login succeeded", "user", user.Username, "role", user.Role)
	return user, nil
}

// EnsureAdmin applies the operator's bootstrap credentials while no active admin exists.
// An existing account is only promoted with its own password, so registering the name first gains nothing.
func (s *Service) EnsureAdmin(ctx context.Context, username, password string) error {
	if active, err := s.HasActiveAdmin(ctx); err != nil || active {
		return err
	}
	username = strings.TrimSpace(username)
	existing, err := s.store.UserByName(ctx, username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		admin, err := newAccount(username, password, store.RoleAdmin)
		if err != nil {
			return err
		}
		if _, err := s.store.CreateUser(ctx, admin); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if existing.Banned || s.compare([]byte(existing.PasswordHash), []byte(password)) != nil {
			return ErrAdminConflict
		}
		if _, _, err := s.store.UpdateUser(ctx, username, func(u *store.User, _ int) error {
			u.Role = store.RoleAdmin
			return nil
		}); err != nil {
			return err
		}
	}
	s.logger.Info("admin created from configuration", "user", username)
	return nil
}

// HasActiveAdmin reports whether anybody can manage this instance.
func (s *Service) HasActiveAdmin(ctx context.Context) (bool, error) {
	count, err := s.store.ActiveAdmins(ctx)
	return count > 0, err
}

// Find returns ErrUnknownUser for names without an account.
func (s *Service) Find(ctx context.Context, username string) (store.User, error) {
	user, err := s.store.UserByName(ctx, strings.TrimSpace(username))
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, ErrUnknownUser
	}
	return user, err
}

// List returns all accounts by name.
func (s *Service) List(ctx context.Context) ([]store.User, error) {
	return s.store.Users(ctx)
}

// SetRole is for admins and keeps one active admin.
func (s *Service) SetRole(ctx context.Context, actor store.User, username string, role store.Role) error {
	if actor.Role != store.RoleAdmin {
		return ErrForbidden
	}
	if !role.Valid() {
		return ErrInvalidRole
	}
	before, after, err := s.store.UpdateUser(ctx, strings.TrimSpace(username), func(u *store.User, activeAdmins int) error {
		if u.Role == store.RoleAdmin && !u.Banned && role != store.RoleAdmin && activeAdmins <= 1 {
			return ErrLastAdmin
		}
		u.Role = role
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return ErrUnknownUser
	}
	if err != nil {
		return err
	}
	s.logger.Info("role changed", "by", actor.Username, "user", after.Username, "from", before.Role, "to", after.Role)
	return nil
}

// ToggleBan bans or unbans and reports the new state. A ban ends all sessions of the account.
func (s *Service) ToggleBan(ctx context.Context, actor store.User, username string) (bool, error) {
	if !actor.Role.Staff() {
		return false, ErrForbidden
	}
	_, after, err := s.store.UpdateUser(ctx, strings.TrimSpace(username), func(u *store.User, activeAdmins int) error {
		switch {
		case u.ID == actor.ID:
			return ErrSelfBan
		case actor.Role == store.RoleModerator && u.Role != store.RoleUser:
			return ErrStaffTarget
		case u.Role == store.RoleAdmin && !u.Banned && activeAdmins <= 1:
			return ErrLastAdmin
		}
		u.Banned = !u.Banned
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return false, ErrUnknownUser
	}
	if err != nil {
		return false, err
	}
	s.revision.Add(1)
	s.logger.Info("ban changed", "by", actor.Username, "user", after.Username, "banned", after.Banned)
	return after.Banned, nil
}
