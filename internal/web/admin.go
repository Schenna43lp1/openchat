package web

import (
	"net/http"

	"github.com/Schenna43lp1/openchat/internal/store"
)

type adminView struct {
	CurrentUser    store.User
	Users          []store.User
	Roles          []store.Role
	CanChangeRoles bool
	Error          string
	Success        string
}

// adminUsers shows and changes accounts. The rules are in package auth.
func (s *server) adminUsers(w http.ResponseWriter, r *http.Request) {
	current := userFrom(r.Context())
	view := adminView{
		CurrentUser:    current,
		Roles:          []store.Role{store.RoleAdmin, store.RoleModerator, store.RoleUser},
		CanChangeRoles: current.Role == store.RoleAdmin,
	}

	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		if !parseForm(w, r) {
			return
		}
		username := r.FormValue("username")
		var err error
		switch r.FormValue("action") {
		case "toggle_ban":
			var banned bool
			if banned, err = s.Auth.ToggleBan(r.Context(), current, username); err == nil {
				view.Success = "Account wurde entsperrt."
				if banned {
					view.Success = "Account wurde gesperrt."
				}
			}
		case "set_role", "":
			if err = s.Auth.SetRole(r.Context(), current, username, store.Role(r.FormValue("role"))); err == nil {
				view.Success = "Rolle wurde aktualisiert."
			}
		default:
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err != nil {
			var known bool
			if view.Error, known = errorMessage(err); !known {
				s.Logger.Error("change account", "user", username, "err", err)
			}
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	users, err := s.Auth.List(r.Context())
	if err != nil {
		s.Logger.Error("list accounts", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	view.Users = users
	s.render(w, http.StatusOK, "admin", page{Title: "Benutzer | Open chat", Description: "Benutzerverwaltung für Open chat", Data: view})
}
