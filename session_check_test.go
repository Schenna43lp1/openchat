// Tests for the endpoint that tells the browser whether reconnecting can work.
package main

import (
	"net/http"
	"testing"
)

func TestSessionCheck(t *testing.T) {
	for actor, want := range map[string]int{
		"admin":     http.StatusNoContent,
		"user":      http.StatusNoContent,
		"anonymous": http.StatusUnauthorized,
		"expired":   http.StatusUnauthorized,
		"deleted":   http.StatusUnauthorized,
		"banned":    http.StatusForbidden,
	} {
		f := newAccessFixture(t)
		if rec := f.request(http.MethodGet, "/session", actor, nil); rec.Code != want {
			t.Errorf("%s: status = %d, want %d", actor, rec.Code, want)
		}
	}
}
