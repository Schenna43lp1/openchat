// Structural checks for pages and stylesheets.
package web

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRenderedPages(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	admin := a.signInAdmin()
	for _, tc := range []struct {
		path, cookie string
		want         []string
	}{
		{"/login", "", []string{"<title>Login | Open chat</title>", `name="password"`}},
		{"/", admin, []string{"<title>Open chat</title>", `data-chat-scope="public"`, `id="splashScreen"`,
			`aria-label="Nachricht an den oeffentlichen Chat"`}},
		{"/direct", admin, []string{"Direkt-Chat</h1>", `data-chat-scope="direct"`, `id="directTarget"`,
			`aria-label="Direktnachricht"`}},
		{"/admin/users", admin, []string{"<title>Benutzer | Open chat</title>", `<option value="admin" selected>`}},
	} {
		res, body := a.send(http.MethodGet, tc.path, "", map[string]string{"Cookie": tc.cookie})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d", tc.path, res.StatusCode)
		}
		before, _, found := strings.Cut(body, "<head>")
		if !found || strings.Contains(before, "<meta") || strings.Count(body, "<title>") != 1 {
			t.Errorf("%s: broken document head", tc.path)
		}
		for _, part := range tc.want {
			if !strings.Contains(body, part) {
				t.Errorf("%s: lacks %s", tc.path, part)
			}
		}
		if script := strings.Index(body, "app.js"); script >= 0 && script < strings.Index(body, `id="toastStack"`) {
			t.Errorf("%s: script runs before the toast area exists", tc.path)
		}
	}
}

func TestStaticFilesRevalidate(t *testing.T) {
	a := startTestApp(t, t.TempDir())
	first, _ := a.send(http.MethodGet, "/static/app.js", "", nil)
	tag := first.Header.Get("ETag")
	if tag == "" {
		t.Fatal("ETag is missing")
	}
	if res, _ := a.send(http.MethodGet, "/static/app.js", "", map[string]string{"If-None-Match": tag}); res.StatusCode != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", res.StatusCode)
	}
}

func TestStylesheetImports(t *testing.T) {
	raw, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range regexp.MustCompile(`url\(\./([^)]+)\)`).FindAllStringSubmatch(string(raw), -1) {
		if _, err := os.Stat(filepath.Join("static", match[1])); err != nil {
			t.Errorf("import of %s: %v", match[1], err)
		}
	}
	tokens, err := os.ReadFile("static/css/root.css")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(tokens), "@import") {
		t.Error("@import is invalid inside :root")
	}
}
