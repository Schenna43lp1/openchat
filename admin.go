package main

import (
	"html/template"
	"log"
	"net/http"
	"strings"
)

type adminUsersViewData struct {
	CurrentUser    currentUser
	Users          []authUser
	Roles          []UserRole
	CanChangeRoles bool
	Error          string
	Success        string
}

func adminUsersHandler(tmpl *template.Template, users *UserStore, logger *log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/users" {
			http.NotFound(w, r)
			return
		}

		current, _ := r.Context().Value(currentUserContextKey).(currentUser)
		data := adminUsersViewData{
			CurrentUser:    current,
			Users:          users.List(),
			Roles:          []UserRole{RoleAdmin, RoleModerator, RoleUser},
			CanChangeRoles: current.Role == RoleAdmin,
		}

		switch r.Method {
		case http.MethodGet:
			renderAdminUsers(w, tmpl, data, logger)
		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}

			username := normalizeAuthUsername(r.FormValue("username"))
			action := r.FormValue("action")
			switch action {
			case "toggle_ban":
				target, ok := users.Find(username)
				if !ok {
					data.Error = authErrorMessage(errUnknownUser)
					break
				}
				if strings.EqualFold(target.Username, current.Username) {
					data.Error = "Du kannst deinen eigenen Account nicht sperren."
					break
				}
				if current.Role == RoleModerator && target.Role != RoleUser {
					data.Error = "Moderatoren duerfen nur Benutzer mit Rolle user sperren."
					break
				}
				banTarget := !target.Banned
				if err := users.SetBanned(username, banTarget); err != nil {
					data.Error = authErrorMessage(err)
					break
				}
				if banTarget {
					data.Success = "Account wurde gesperrt."
				} else {
					data.Success = "Account wurde entsperrt."
				}
			case "set_role", "":
				if current.Role != RoleAdmin {
					data.Error = "Nur Admins duerfen Rollen aendern."
					break
				}
				role := UserRole(r.FormValue("role"))
				if err := users.SetRole(username, role); err != nil {
					data.Error = authErrorMessage(err)
				} else {
					data.Success = "Rolle wurde aktualisiert."
				}
			default:
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			data.Users = users.List()
			renderAdminUsers(w, tmpl, data, logger)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func renderAdminUsers(w http.ResponseWriter, tmpl *template.Template, data adminUsersViewData, logger *log.Logger) {
	if err := tmpl.Execute(w, data); err != nil {
		logger.Printf("execute admin template: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}
