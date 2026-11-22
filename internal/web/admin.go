package web

import (
	"cmp"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/Schenna43lp1/openchat/internal/auth"
	"github.com/Schenna43lp1/openchat/internal/store"
)

const (
	usersPath     = "/admin/users"
	usersPageSize = 25
)

var userFilters = []struct {
	key, label string
	match      func(store.User) bool
}{
	{"", "Alle", func(store.User) bool { return true }},
	{"admin", "Admins", func(u store.User) bool { return u.Role == store.RoleAdmin }},
	{"moderator", "Moderatoren", func(u store.User) bool { return u.Role == store.RoleModerator }},
	{"pending", "Startpasswort offen", func(u store.User) bool { return u.MustChangePassword }},
	{"banned", "Gesperrt", func(u store.User) bool { return u.Banned }},
}

type filterLink struct {
	Label  string
	URL    string
	Count  int
	Active bool
}

type usersView struct {
	CurrentUser store.User
	CanCreate   bool
	Query       string
	Filter      string
	Filters     []filterLink
	Users       []store.User
	Found       int
	Page, Pages int
	Previous    string
	Next        string
}

func usersURL(query, filter string, page int) string {
	values := url.Values{}
	if query != "" {
		values.Set("q", query)
	}
	if filter != "" {
		values.Set("filter", filter)
	}
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	if len(values) == 0 {
		return usersPath
	}
	return usersPath + "?" + values.Encode()
}

// newUsersView selects what the list shows. The counts of the filters follow the search.
func newUsersView(current store.User, all []store.User, params url.Values) usersView {
	view := usersView{
		CurrentUser: current,
		CanCreate:   current.Role == store.RoleAdmin,
		Query:       strings.TrimSpace(params.Get("q")),
	}
	needle := strings.ToLower(view.Query)
	found := slices.DeleteFunc(slices.Clone(all), func(u store.User) bool {
		return !strings.Contains(strings.ToLower(u.Username), needle)
	})

	active := userFilters[0]
	for _, f := range userFilters {
		if f.key == params.Get("filter") {
			active = f
		}
	}
	view.Filter = active.key
	for _, f := range userFilters {
		link := filterLink{Label: f.label, URL: usersURL(view.Query, f.key, 1), Active: f.key == active.key}
		for _, u := range found {
			if f.match(u) {
				link.Count++
			}
		}
		view.Filters = append(view.Filters, link)
	}

	found = slices.DeleteFunc(found, func(u store.User) bool { return !active.match(u) })
	view.Found = len(found)
	view.Pages = max(1, (view.Found+usersPageSize-1)/usersPageSize)
	// An invalid page is the first one, a page after the end the last one.
	requested, err := strconv.Atoi(cmp.Or(params.Get("page"), "1"))
	if err != nil {
		requested = 1
	}
	view.Page = min(max(requested, 1), view.Pages)
	first := (view.Page - 1) * usersPageSize
	view.Users = found[first:min(first+usersPageSize, view.Found)]
	if view.Page > 1 {
		view.Previous = usersURL(view.Query, view.Filter, view.Page-1)
	}
	if view.Page < view.Pages {
		view.Next = usersURL(view.Query, view.Filter, view.Page+1)
	}
	return view
}

// adminUsers lists accounts. Changes are made on the page of one account.
func (s *server) adminUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	users, err := s.Auth.List(r.Context())
	if err != nil {
		s.Logger.Error("list accounts", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	view := newUsersView(userFrom(r.Context()), users, r.URL.Query())
	s.render(w, http.StatusOK, "admin", page{Title: "Benutzer | Open chat", Description: "Benutzerverwaltung für Open chat", Data: view})
}

type roleOption struct {
	Role        store.Role
	Description string
}

var roleOptions = []roleOption{
	{store.RoleUser, "Darf chatten."},
	{store.RoleModerator, "Darf ausserdem Benutzer mit der Rolle user sperren und entsperren."},
	{store.RoleAdmin, "Darf alles in der App. Das Passwort setzt danach nur noch der Betreiber zurueck, auch nach einem Herabstufen."},
}

// The results of the actions, shown after the redirect.
var userChanges = map[string]string{
	"banned":   "Konto wurde gesperrt.",
	"unbanned": "Konto wurde entsperrt.",
	"role":     "Rolle wurde aktualisiert.",
}

type userView struct {
	Admin   string
	Target  store.User
	Self    bool
	IsAdmin bool
	CanBan  bool
	Roles   []roleOption
	History store.IssueHistory
	Error   string
	Success string
}

// targetUser reads the account of the path. The ID, not the name: request paths are logged.
func (s *server) targetUser(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return store.User{}, false
	}
	target, err := s.Auth.FindID(r.Context(), id)
	if errors.Is(err, auth.ErrUnknownUser) {
		http.NotFound(w, r)
		return store.User{}, false
	}
	if err != nil {
		s.Logger.Error("read account", "id", id, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return store.User{}, false
	}
	return target, true
}

func userPath(target store.User) string {
	return usersPath + "/" + strconv.FormatInt(target.ID, 10)
}

// adminUser shows and changes one account. The rules are in package auth.
func (s *server) adminUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		s.showUser(w, r, http.StatusOK, target, userChanges[r.URL.Query().Get("done")], "")
		return
	}
	if !parseForm(w, r) {
		return
	}

	current := userFrom(r.Context())
	var err error
	var done string
	switch r.PostFormValue("action") {
	case "ban":
		done, err = "banned", s.Auth.SetBanned(r.Context(), current, target.Username, true)
	case "unban":
		done, err = "unbanned", s.Auth.SetBanned(r.Context(), current, target.Username, false)
	case "set_role":
		allowed, wait, settle := s.admitPassword(current.ID)
		if !allowed {
			setRetryAfter(w, wait)
			s.showUser(w, r, http.StatusTooManyRequests, target, "", waitMessage(wait))
			return
		}
		role := store.Role(r.PostFormValue("role"))
		done, err = "role", s.Auth.ChangeRole(r.Context(), current, r.PostFormValue("confirmation"), target.ID, store.Role(r.PostFormValue("from")), role)
		settle(err)
		if err == nil && target.ID == current.ID && !role.Staff() {
			http.Redirect(w, r, "/", http.StatusSeeOther) // the user management is closed for them now
			return
		}
	default:
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err != nil {
		message, known := errorMessage(err)
		status := http.StatusOK
		if !known {
			s.Logger.Error("change account", "user", target.Username, "err", err)
			status = http.StatusInternalServerError
		}
		// What is true now, it can be the reason for the refusal.
		if fresh, findErr := s.Auth.FindID(r.Context(), target.ID); findErr == nil {
			target = fresh
		}
		s.showUser(w, r, status, target, "", message)
		return
	}
	// Redirect, so that a reload does not send the action again.
	http.Redirect(w, r, userPath(target)+"?done="+done, http.StatusSeeOther)
}

func (s *server) showUser(w http.ResponseWriter, r *http.Request, status int, target store.User, success, failure string) {
	current := userFrom(r.Context())
	view := userView{
		Admin:   current.Username,
		Target:  target,
		Self:    target.ID == current.ID,
		IsAdmin: current.Role == store.RoleAdmin,
		CanBan:  auth.CanBan(current, target),
		Roles:   roleOptions,
		Error:   failure,
		Success: success,
	}
	if view.IsAdmin {
		// Admins see the resets of each other.
		history, err := s.Auth.IssueHistory(r.Context(), target)
		if err != nil {
			s.Logger.Error("read issued passwords", "user", target.Username, "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		view.History = history
	}
	s.render(w, status, "user", page{Title: "Konto verwalten | Open chat", Description: "Benutzerverwaltung für Open chat", Data: view})
}
