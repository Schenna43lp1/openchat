package main

import (
	"html/template"
	"log"
	"net/http"
)

type adminUsersViewData struct {
	CurrentUser currentUser
	Users       []authUser
	Roles       []UserRole
	Error       string
	Success     string
}

func adminUsersHandler(tmpl *template.Template, users *UserStore, logger *log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/users" {
			http.NotFound(w, r)
			return
		}

		current, _ := r.Context().Value(currentUserContextKey).(currentUser)
		data := adminUsersViewData{
			CurrentUser: current,
			Users:       users.List(),
			Roles:       []UserRole{RoleAdmin, RoleModerator, RoleUser},
		}

		switch r.Method {
		case http.MethodGet:
			renderAdminUsers(w, tmpl, data, logger)
		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}

			username := r.FormValue("username")
			role := UserRole(r.FormValue("role"))
			if err := users.SetRole(username, role); err != nil {
				data.Error = authErrorMessage(err)
			} else {
				data.Success = "Rolle wurde aktualisiert."
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
