package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Schenna43lp1/openchat/internal/auth"
	"github.com/Schenna43lp1/openchat/internal/store"
)

const (
	passwordPath = "/account/password"

	// Per account, for forms that ask for the password of the one who is signed in. A stolen session must
	// not become an oracle for that password. Only wrong passwords use this budget up.
	confirmationBurst    = 10
	confirmationInterval = 5 * time.Minute
	// Right ones cost a bcrypt, too.
	passwordWorkBurst    = 20
	passwordWorkInterval = 6 * time.Second
)

type passwordView struct {
	Username string
	Forced   bool
	History  store.IssueHistory
	Error    string
	Success  string
}

// changePassword is the only page for an account that still has an issued password.
func (s *server) changePassword(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	history, err := s.Auth.IssueHistory(r.Context(), user)
	if err != nil {
		// An empty history would say that nothing happened.
		s.Logger.Error("read issued passwords", "user", user.Username, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	view := passwordView{Username: user.Username, Forced: user.MustChangePassword, History: history}
	show := func(status int) {
		s.render(w, status, "account", page{Title: "Passwort | Open chat", Description: "Passwort in Open chat aendern", Data: view})
	}

	switch r.Method {
	case http.MethodGet:
		if !view.Forced && r.URL.Query().Get("changed") == "1" {
			view.Success = "Passwort wurde geaendert. Andere Anmeldungen wurden beendet."
		}
		show(http.StatusOK)
		return
	case http.MethodPost:
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !parseForm(w, r) {
		return
	}
	// PostFormValue: a password must not be accepted from the URL, which ends up in histories and proxy logs.
	current, next := r.PostFormValue("current"), r.PostFormValue("password")
	if next != r.PostFormValue("repeat") {
		view.Error = "Die neuen Passwoerter stimmen nicht ueberein."
		show(http.StatusOK)
		return
	}
	allowed, wait, settle := s.admitPassword(user.ID)
	if !allowed {
		setRetryAfter(w, wait)
		view.Error = waitMessage(wait)
		show(http.StatusTooManyRequests)
		return
	}
	updated, err := s.Auth.ChangePassword(r.Context(), user, current, next)
	settle(err)
	if err != nil {
		var known bool
		status := http.StatusOK
		if view.Error, known = errorMessage(err); !known {
			s.Logger.Error("change password", "user", user.Username, "err", err)
			status = http.StatusInternalServerError
		}
		show(status)
		return
	}

	// The change ended all sessions, also this one.
	token, expires, err := s.Auth.StartSession(r.Context(), updated)
	if err != nil {
		s.Logger.Error("session after a password change", "user", user.Username, "err", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.setCookie(w, token, expires)
	rememberDevice(w, updated.Username, updated.PasswordHash, !s.InsecureCookie)
	http.Redirect(w, r, passwordPath+"?changed=1", http.StatusSeeOther)
}

// admitPassword reserves a guess before the password is checked. settle gives it back unless it was wrong.
func (s *server) admitPassword(userID int64) (allowed bool, wait time.Duration, settle func(error)) {
	key := strconv.FormatInt(userID, 10)
	if allowed, wait = s.passwordWork.allow(key); !allowed {
		return false, wait, nil
	}
	if allowed, wait = s.confirmations.allow(key); !allowed {
		return false, wait, nil
	}
	return true, 0, func(err error) {
		if !errors.Is(err, auth.ErrWrongPassword) {
			s.confirmations.refund(key)
		}
	}
}
