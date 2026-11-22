package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Schenna43lp1/openchat/internal/auth"
	"github.com/Schenna43lp1/openchat/internal/store"
)

// issueView is for both forms that end with an issued password, and for showing that password once.
type issueView struct {
	Admin    string
	Target   *store.User // nil while creating
	Username string
	State    string
	Blocked  bool
	Error    string
	Issued   *auth.Issued
}

func (s *server) renderIssue(w http.ResponseWriter, status int, view issueView) {
	title := "Konto anlegen | Open chat"
	if view.Target != nil {
		title = "Passwort zuruecksetzen | Open chat"
	}
	s.render(w, status, "issue", page{Title: title, Description: "Benutzerverwaltung fuer Open chat", Data: view})
}

func (s *server) newAccount(w http.ResponseWriter, r *http.Request) {
	admin := userFrom(r.Context())
	view := issueView{Admin: admin.Username}
	switch r.Method {
	case http.MethodGet:
		s.renderIssue(w, http.StatusOK, view)
	case http.MethodPost:
		if !parseForm(w, r) {
			return
		}
		view.Username = strings.TrimSpace(r.PostFormValue("username"))
		s.issuePassword(w, r, view, func(confirmation string) (auth.Issued, error) {
			return s.Auth.CreateAccount(r.Context(), admin, confirmation, view.Username)
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) resetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// The ID, not the name: request paths are logged.
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target, err := s.Auth.FindID(r.Context(), id)
	if errors.Is(err, auth.ErrUnknownUser) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.Logger.Error("read account", "id", id, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	admin := userFrom(r.Context())
	view := issueView{Admin: admin.Username, Target: &target, State: auth.ResetState(target)}
	if target.OperatorResetOnly {
		// Only for display, the rule is in package auth.
		view.Blocked = true
		view.Error, _ = errorMessage(auth.ErrOperatorOnly)
		s.renderIssue(w, http.StatusForbidden, view)
		return
	}
	if r.Method == http.MethodGet {
		s.renderIssue(w, http.StatusOK, view)
		return
	}
	if !parseForm(w, r) {
		return
	}
	s.issuePassword(w, r, view, func(confirmation string) (auth.Issued, error) {
		return s.Auth.ResetPassword(r.Context(), admin, confirmation, id, r.PostFormValue("state"))
	})
}

// issuePassword runs issue with the confirmation of the admin and shows the result.
func (s *server) issuePassword(w http.ResponseWriter, r *http.Request, view issueView, issue func(confirmation string) (auth.Issued, error)) {
	admin := userFrom(r.Context())
	allowed, wait, settle := s.admitPassword(admin.ID)
	if !allowed {
		setRetryAfter(w, wait)
		view.Error = waitMessage(wait)
		s.renderIssue(w, http.StatusTooManyRequests, view)
		return
	}
	issued, err := issue(r.PostFormValue("confirmation"))
	settle(err)
	if err != nil {
		var known bool
		status := http.StatusOK
		if view.Error, known = errorMessage(err); !known {
			s.Logger.Error("issue password", "by", admin.Username, "err", err)
			status = http.StatusInternalServerError
		}
		s.renderIssue(w, status, view)
		return
	}

	// Guesses at the old password must not keep the owner from using the new one.
	s.guard.forget(issued.User.Username)
	s.confirmations.forget(strconv.FormatInt(issued.User.ID, 10))
	view.Issued = &issued
	s.renderIssue(w, http.StatusOK, view)
}
