// Tests for the configurable Secure attribute.
package main

import "testing"

func TestCookieSecureSetting(t *testing.T) {
	for value, want := range map[string]bool{"": true, "true": true, "false": false} {
		cfg, err := loadConfig(envMap(map[string]string{envCookieSecure: value}))
		if err != nil {
			t.Fatal(err)
		}
		sessions := NewSessionManager()
		sessions.insecureCookie = cfg.insecureCookie
		if got := newSessionCookie(t, sessions, "alice").Secure; got != want {
			t.Errorf("%q: Secure = %t, want %t", value, got, want)
		}
	}
	if _, err := loadConfig(envMap(map[string]string{envCookieSecure: "no-thanks"})); err == nil {
		t.Error("invalid value was accepted")
	}
}
