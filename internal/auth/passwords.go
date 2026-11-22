package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/Schenna43lp1/openchat/internal/store"
)

// Issued is an account together with a password that the server chose. It is shown once and never logged.
type Issued struct {
	User     store.User
	Password string
}

// ResetState identifies the password that a reset form was made for, so that sending the form again does not
// reset again. It reveals nothing: the hash it is derived from has a random salt.
func ResetState(user store.User) string {
	sum := sha256.Sum256([]byte("openchat-reset-state\x00" + user.PasswordHash))
	return hex.EncodeToString(sum[:16])
}

// ChangePassword lets user choose a new password. It ends all sessions of the account, also the calling one.
func (s *Service) ChangePassword(ctx context.Context, user store.User, current, next string) (store.User, error) {
	if !validPassword(next) {
		return store.User{}, ErrInvalidPassword
	}
	if s.compare([]byte(user.PasswordHash), []byte(current)) != nil {
		s.logger.Warn("password change failed", "user", user.Username, "reason", "wrong password")
		return store.User{}, ErrWrongPassword
	}
	// Otherwise a password that somebody else has seen could be kept.
	if next == current {
		return store.User{}, ErrSamePassword
	}
	hash, err := hashPassword(next)
	if err != nil {
		return store.User{}, err
	}
	updated, err := s.setPassword(ctx, store.PasswordChange{
		ActorID:  user.ID,
		TargetID: user.ID,
		Hash:     hash,
		Check: func(_, target store.User) error {
			if target.Banned || target.PasswordHash != user.PasswordHash {
				return ErrAccountChanged
			}
			return nil
		},
	})
	if err != nil {
		return store.User{}, err
	}
	s.logger.Info("password changed", "user", updated.Username)
	return updated, nil
}

// CreateAccount is for admins, who confirm with their own password. The account gets the role user.
func (s *Service) CreateAccount(ctx context.Context, actor store.User, confirmation, username string) (Issued, error) {
	if err := s.confirmAdmin(actor, confirmation); err != nil {
		return Issued{}, err
	}
	password := rand.Text()
	account, err := newAccount(strings.TrimSpace(username), password, store.RoleUser)
	if err != nil {
		return Issued{}, err
	}
	account.MustChangePassword = true
	created, err := s.store.CreateUserBy(ctx, actor.ID, account, func(now store.User) error {
		return stillAdmin(now, actor)
	})
	switch {
	case errors.Is(err, store.ErrTaken):
		return Issued{}, ErrUsernameTaken
	case errors.Is(err, store.ErrNotFound):
		return Issued{}, ErrForbidden
	case err != nil:
		return Issued{}, err
	}
	s.logger.Info("account created", "by", actor.Username, "user", created.Username)
	return Issued{User: created, Password: password}, nil
}

// ResetPassword is for admins, who confirm with their own password. It is refused for everybody who is or was
// admin, and it hides the direct messages so far from the account, because the admin could now sign in as it.
// state is the ResetState that the admin saw.
func (s *Service) ResetPassword(ctx context.Context, actor store.User, confirmation string, targetID int64, state string) (Issued, error) {
	if err := s.confirmAdmin(actor, confirmation); err != nil {
		return Issued{}, err
	}
	resettable := func(target store.User) error {
		switch {
		case target.OperatorResetOnly || target.Role == store.RoleAdmin:
			return ErrOperatorOnly
		case ResetState(target) != state:
			return ErrAccountChanged
		default:
			return nil
		}
	}
	target, err := s.FindID(ctx, targetID)
	if err != nil {
		return Issued{}, err
	}
	if err := resettable(target); err != nil {
		return Issued{}, err // before bcrypt, and again inside the transaction
	}

	password := rand.Text()
	hash, err := hashPassword(password)
	if err != nil {
		return Issued{}, err
	}
	updated, err := s.setPassword(ctx, store.PasswordChange{
		ActorID:            actor.ID,
		TargetID:           targetID,
		Hash:               hash,
		MustChange:         true,
		HideDirectMessages: true,
		Check: func(now, target store.User) error {
			if err := stillAdmin(now, actor); err != nil {
				return err
			}
			return resettable(target)
		},
	})
	if err != nil {
		return Issued{}, err
	}
	s.logger.Info("password reset", "by", actor.Username, "user", updated.Username, "direct_messages_hidden", true)
	return Issued{User: updated, Password: password}, nil
}

// OperatorReset works on every account and hides nothing, because the operator can read the database anyway.
// Connections only notice it in the same process, see ADR 0001.
func (s *Service) OperatorReset(ctx context.Context, username string) (Issued, error) {
	target, err := s.Find(ctx, username)
	if err != nil {
		return Issued{}, err
	}
	password := rand.Text()
	hash, err := hashPassword(password)
	if err != nil {
		return Issued{}, err
	}
	updated, err := s.setPassword(ctx, store.PasswordChange{
		TargetID:   target.ID,
		Hash:       hash,
		MustChange: true,
		Check:      func(_, _ store.User) error { return nil },
	})
	if err != nil {
		return Issued{}, err
	}
	s.logger.Info("password reset", "by", "operator", "user", updated.Username, "direct_messages_hidden", false)
	return Issued{User: updated, Password: password}, nil
}

// FindID returns ErrUnknownUser for IDs without an account.
func (s *Service) FindID(ctx context.Context, id int64) (store.User, error) {
	user, err := s.store.UserByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, ErrUnknownUser
	}
	return user, err
}

func (s *Service) setPassword(ctx context.Context, change store.PasswordChange) (store.User, error) {
	// Also on errors: SQLite can report one for a write that happened. Checking again is harmless.
	defer s.revision.Add(1)
	user, err := s.store.SetPassword(ctx, change)
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, ErrUnknownUser
	}
	return user, err
}

func (s *Service) confirmAdmin(actor store.User, password string) error {
	if actor.Role != store.RoleAdmin || actor.Banned || actor.MustChangePassword {
		return ErrForbidden
	}
	if s.compare([]byte(actor.PasswordHash), []byte(password)) != nil {
		s.logger.Warn("confirmation failed", "user", actor.Username, "reason", "wrong password")
		return ErrWrongPassword
	}
	return nil
}

// stillAdmin compares the actor inside the transaction with the one whose password was checked.
// Comparing takes long enough to be demoted, banned or reset meanwhile.
func stillAdmin(now, confirmed store.User) error {
	switch {
	case now.Role != store.RoleAdmin || now.Banned || now.MustChangePassword:
		return ErrForbidden
	case now.PasswordHash != confirmed.PasswordHash:
		return ErrAccountChanged
	default:
		return nil
	}
}
