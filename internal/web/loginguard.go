package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"time"
)

const (
	// Failed logins per account: all unknown browsers share one budget, the known ones another.
	accountGuessBurst    = 20
	accountGuessInterval = 5 * time.Minute

	deviceCookieName     = "openchat_device"
	deviceCookieDuration = 180 * 24 * time.Hour
)

// loginGuard stops distributed guessing. A limit per account alone would let anybody lock out the owner,
// so a browser that has signed in before has a budget that strangers cannot use up.
type loginGuard struct {
	strangers *rateLimiter
	devices   *rateLimiter
}

func newLoginGuard() *loginGuard {
	return &loginGuard{
		strangers: newRateLimiter(accountGuessBurst, accountGuessInterval),
		devices:   newRateLimiter(accountGuessBurst, accountGuessInterval),
	}
}

// admit reserves a guess before the password is checked, so that parallel guesses cannot overrun the limit.
// name is the stored name if the account exists, and passwordHash is empty if it does not.
// Unknown names are limited alike, otherwise the limit would reveal which accounts exist.
// succeeded gives the guess back.
func (g *loginGuard) admit(r *http.Request, name, passwordHash string) (allowed bool, wait time.Duration, succeeded func()) {
	limit := g.strangers
	if isKnownDevice(r, name, passwordHash) {
		limit = g.devices
	}
	key := guessKey(name)
	allowed, wait = limit.allow(key)
	return allowed, wait, func() { limit.refund(key) }
}

// forget clears both budgets of an account that got a new password.
func (g *loginGuard) forget(name string) {
	key := guessKey(name)
	g.strangers.forget(key)
	g.devices.forget(key)
}

func guessKey(name string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(name))) // the input can be 64 KiB long
	return string(sum[:])
}

// deviceToken needs no storage: the password hash is a secret that only the server has.
// The token grants no access, it only selects the budget, and it ends with a password change.
func deviceToken(name, passwordHash string) string {
	mac := hmac.New(sha256.New, []byte(passwordHash))
	mac.Write([]byte("openchat-device\x00" + strings.ToLower(name)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func isKnownDevice(r *http.Request, name, passwordHash string) bool {
	if passwordHash == "" {
		return false
	}
	cookie, err := r.Cookie(deviceCookieName)
	return err == nil && hmac.Equal([]byte(cookie.Value), []byte(deviceToken(name, passwordHash)))
}

// rememberDevice is called after a login. Logout keeps the cookie on purpose.
func rememberDevice(w http.ResponseWriter, name, passwordHash string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     deviceCookieName,
		Value:    deviceToken(name, passwordHash),
		Path:     "/login",
		MaxAge:   int(deviceCookieDuration.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}
