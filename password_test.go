// Tests for password limits and error messages.
package main

import (
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestPasswordLimits(t *testing.T) {
	for _, tc := range []struct {
		name, password string
		valid          bool
	}{
		{"72 bytes", strings.Repeat("x", 72), true},
		{"36 umlauts", strings.Repeat("ä", 36), true},
		{"eight umlauts", strings.Repeat("ä", 8), true},
		{"73 bytes", strings.Repeat("x", 73), false},
		{"37 umlauts", strings.Repeat("ä", 37), false},
		{"four umlauts are four characters", strings.Repeat("ä", 4), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := openTestStore(t, filepath.Join(t.TempDir(), "users.json")).Register("alice", tc.password)
			if (tc.valid && err != nil) || (!tc.valid && !errors.Is(err, errInvalidPassword)) {
				t.Fatalf("error = %v, valid = %t", err, tc.valid)
			}
		})
	}
}

func TestTooLongPasswordIsExplained(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	_, body := a.post("/login", "", url.Values{"mode": {"register"}, "username": {"alice"}, "password": {strings.Repeat("x", 100)}})
	if !strings.Contains(body, "72 Byte") {
		t.Fatal("limit is not explained")
	}
}

func TestUnexpectedErrorIsNotReportedAsWrongPassword(t *testing.T) {
	if got := authErrorMessage(errors.New("boom")); strings.Contains(got, "Passwort ist falsch") {
		t.Fatalf("message = %q", got)
	}
}
