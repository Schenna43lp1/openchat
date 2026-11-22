package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Schenna43lp1/openchat/internal/auth"
	"github.com/Schenna43lp1/openchat/internal/store"
)

const cookieName = "openchat_session"

type contextKey struct{}

func userFrom(ctx context.Context) store.User {
	user, _ := ctx.Value(contextKey{}).(store.User)
	return user
}

// authRequired enforces login and puts the current state of the account into the context. An account with an
// issued password only gets to the password page. That is decided here, so that no route can forget it.
func (s *server) authRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// These routes are called by scripts, which cannot act on a redirect to the login page.
		wantsStatus := r.URL.Path == "/ws" || r.URL.Path == "/session"
		deny := func(status int) {
			if wantsStatus {
				http.Error(w, strings.ToLower(http.StatusText(status)), status)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		}

		user, err := s.currentSession(r)
		switch {
		case errors.Is(err, auth.ErrNoSession):
			deny(http.StatusUnauthorized)
		case err != nil:
			s.Logger.Error("read session", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		case user.Banned:
			if err := s.endSession(w, r); err != nil {
				s.Logger.Error("end session of banned account", "err", err)
			}
			deny(http.StatusForbidden)
		// The matched route, not the path: "/account%2Fpassword" has the same path and is served by "/".
		case user.MustChangePassword && r.Pattern != passwordPath:
			if wantsStatus {
				deny(http.StatusUnauthorized)
			} else {
				http.Redirect(w, r, passwordPath, http.StatusSeeOther)
			}
		default:
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, user)))
		}
	})
}

func (s *server) currentSession(r *http.Request) (store.User, error) {
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return store.User{}, auth.ErrNoSession
	}
	return s.Auth.Session(r.Context(), cookie.Value)
}

func staffRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !userFrom(r.Context()).Role.Staff() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func adminRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r.Context()).Role != store.RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type loginView struct {
	Username          string
	Error             string
	AllowRegistration bool
}

func (s *server) renderLogin(w http.ResponseWriter, status int, view loginView) {
	view.AllowRegistration = s.AllowRegistration
	s.render(w, status, "login", page{Title: "Login | Open chat", Description: "Login-Seite für Open chat", Data: view})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		user, err := s.currentSession(r)
		if err == nil && !user.Banned {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if err != nil && !errors.Is(err, auth.ErrNoSession) {
			s.Logger.Error("read session", "err", err)
		}
		s.renderLogin(w, http.StatusOK, loginView{})
	case http.MethodPost:
		s.loginPost(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) loginPost(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	username, password := strings.TrimSpace(r.FormValue("username")), r.FormValue("password")

	var user store.User
	var err error
	switch r.FormValue("mode") {
	case "register":
		if !s.AllowRegistration {
			s.renderLogin(w, http.StatusForbidden, loginView{Username: username, Error: "Die Registrierung ist geschlossen."})
			return
		}
		if !s.admit(w, r, s.registrations, username) {
			return
		}
		user, err = s.Auth.Register(r.Context(), username, password)
	case "login", "":
		if !s.admit(w, r, s.logins, username) {
			return
		}
		name, hash := username, ""
		account, findErr := s.Auth.Find(r.Context(), username)
		switch {
		case findErr == nil:
			name, hash = account.Username, account.PasswordHash
		case !errors.Is(findErr, auth.ErrUnknownUser):
			err = findErr
		}
		if err != nil {
			break
		}
		allowed, wait, succeeded := s.guard.admit(r, name, hash)
		if !allowed {
			s.tooMany(w, username, wait)
			return
		}
		if user, err = s.Auth.Authenticate(r.Context(), username, password); err == nil {
			succeeded()
		}
	default:
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var token string
	var expires time.Time
	if err == nil {
		token, expires, err = s.Auth.StartSession(r.Context(), user)
	}
	if err != nil {
		message, known := errorMessage(err)
		status := http.StatusOK
		if !known {
			s.Logger.Error("sign in", "user", username, "err", err)
			status = http.StatusInternalServerError
		}
		s.renderLogin(w, status, loginView{Username: username, Error: message})
		return
	}

	s.setCookie(w, token, expires)
	rememberDevice(w, user.Username, user.PasswordHash, !s.InsecureCookie)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// admit applies a rate limit before any expensive work is done.
func (s *server) admit(w http.ResponseWriter, r *http.Request, limit *rateLimiter, username string) bool {
	allowed, wait := limit.allow(clientKey(r, s.TrustedProxies))
	if allowed {
		return true
	}
	s.tooMany(w, username, wait)
	return false
}

func (s *server) tooMany(w http.ResponseWriter, username string, wait time.Duration) {
	setRetryAfter(w, wait)
	s.renderLogin(w, http.StatusTooManyRequests, loginView{Username: username, Error: waitMessage(wait)})
}

func setRetryAfter(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
}

func waitMessage(wait time.Duration) string {
	if wait >= time.Minute {
		return fmt.Sprintf("Zu viele Versuche. Bitte versuche es in %d Minuten erneut.", int(wait/time.Minute)+1)
	}
	return "Zu viele Versuche. Bitte warte kurz."
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.endSession(w, r); err != nil {
		s.Logger.Error("logout", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *server) setCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(auth.SessionDuration.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   !s.InsecureCookie,
	})
}

// endSession keeps the cookie if the session could not be deleted, so that logging out can be retried.
func (s *server) endSession(w http.ResponseWriter, r *http.Request) error {
	if cookie, err := r.Cookie(cookieName); err == nil {
		if err := s.Auth.EndSession(r.Context(), cookie.Value); err != nil {
			return err
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   !s.InsecureCookie,
	})
	return nil
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

// errorMessage translates errors for users. known is false for failures of the server.
func errorMessage(err error) (message string, known bool) {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		return "Benutzername oder Passwort ist falsch.", true
	case errors.Is(err, auth.ErrUsernameTaken):
		return "Dieser Benutzername ist bereits vergeben.", true
	case errors.Is(err, auth.ErrInvalidUsername):
		return "Der Benutzername braucht 3 bis 32 Zeichen: Buchstaben, Zahlen, Punkt, Unterstrich oder Bindestrich.", true
	case errors.Is(err, auth.ErrInvalidPassword):
		return "Das Passwort braucht mindestens 8 Zeichen und darf hoechstens 72 Byte lang sein (Umlaute zaehlen doppelt, Emojis vierfach).", true
	case errors.Is(err, auth.ErrInvalidRole):
		return "Diese Rolle ist nicht erlaubt.", true
	case errors.Is(err, auth.ErrUnknownUser):
		return "Dieser Benutzer wurde nicht gefunden.", true
	case errors.Is(err, auth.ErrLastAdmin):
		return "Der letzte aktive Admin kann nicht entfernt oder gesperrt werden.", true
	case errors.Is(err, auth.ErrBanned):
		return "Dieser Account wurde gesperrt.", true
	case errors.Is(err, auth.ErrSelfBan):
		return "Du kannst deinen eigenen Account nicht sperren.", true
	case errors.Is(err, auth.ErrStaffTarget):
		return "Moderatoren duerfen nur Benutzer mit Rolle user sperren.", true
	case errors.Is(err, auth.ErrForbidden):
		return "Nur Admins duerfen Rollen aendern.", true
	case errors.Is(err, auth.ErrWrongPassword):
		return "Dein Passwort ist falsch.", true
	case errors.Is(err, auth.ErrSamePassword):
		return "Das neue Passwort muss sich vom bisherigen unterscheiden.", true
	case errors.Is(err, auth.ErrOperatorOnly):
		return "Das Passwort von Admins und frueheren Admins setzt nur der Betreiber auf dem Server zurueck.", true
	case errors.Is(err, auth.ErrAccountChanged):
		return "Das Konto wurde inzwischen geaendert. Bitte pruefe es und versuche es dann erneut.", true
	case errors.Is(err, store.ErrSave):
		return "Die Aenderung konnte nicht gespeichert werden. Bitte versuche es spaeter erneut.", false
	default:
		return "Serverfehler. Bitte versuche es spaeter erneut.", false
	}
}
