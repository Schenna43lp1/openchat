// Tests for the cross-origin protection of legitimate requests.
package web

import (
	"net/http"
	"testing"
)

func TestOwnAndConfiguredOriginsMayPost(t *testing.T) {
	origins, _ := ParseOrigins("https://Chat.Example.com/")
	a := startTestAppWith(t, t.TempDir(), Options{AllowedOrigins: origins})
	for name, headers := range map[string]map[string]string{
		"same origin":       {"Origin": a.server.URL},
		"fetch metadata":    {"Origin": "https://behind.proxy.example", "Sec-Fetch-Site": "same-origin"},
		"configured origin": {"Origin": "https://chat.example.com", "Sec-Fetch-Site": "cross-site"},
		"no browser":        {},
	} {
		if res, _ := a.send(http.MethodPost, "/logout", "", headers); res.StatusCode != http.StatusSeeOther {
			t.Errorf("%s: status = %d, want 303", name, res.StatusCode)
		}
	}
}
