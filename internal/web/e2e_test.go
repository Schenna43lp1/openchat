// End-to-end test of the chat as a browser uses it: HTTPS, cookie jar, pages, forms and WebSockets.
package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Schenna43lp1/openchat/internal/chat"
)

type e2eServer struct {
	*httptest.Server
	stop func()
}

func startE2EServer(t *testing.T, dir string) *e2eServer {
	t.Helper()
	// TLS is required: the cookie jar, like a browser, returns the Secure session cookie only over HTTPS.
	a := launchTestApp(t, dir, Options{AllowRegistration: true}, httptest.NewTLSServer)
	// Like an operator who leaves the bootstrap variables set across restarts.
	if err := a.users.EnsureAdmin("admin", testPassword); err != nil {
		t.Fatal(err)
	}
	return &e2eServer{Server: a.server, stop: a.stop}
}

func (b *browser) cookie() *http.Cookie {
	b.t.Helper()
	address, err := url.Parse(b.base)
	if err != nil {
		b.t.Fatal(err)
	}
	for _, cookie := range b.client.Jar.Cookies(address) {
		if cookie.Name == cookieName {
			return cookie
		}
	}
	b.t.Fatal("browser has no session cookie")
	return nil
}

func (b *browser) setCookie(cookie *http.Cookie) {
	b.t.Helper()
	address, err := url.Parse(b.base)
	if err != nil {
		b.t.Fatal(err)
	}
	b.client.Jar.SetCookies(address, []*http.Cookie{{Name: cookie.Name, Value: cookie.Value, Path: "/"}})
}

type browser struct {
	t      *testing.T
	base   string
	client *http.Client
	dialer websocket.Dialer
}

func (s *e2eServer) newBrowser(t *testing.T) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := s.Client().Transport.(*http.Transport).Clone()
	tlsConfig := transport.TLSClientConfig.Clone()
	tlsConfig.NextProtos = nil // WebSockets need HTTP/1.1
	t.Cleanup(transport.CloseIdleConnections)
	return &browser{
		t:      t,
		base:   s.URL,
		client: &http.Client{Transport: transport, Jar: jar, Timeout: 10 * time.Second},
		dialer: websocket.Dialer{TLSClientConfig: tlsConfig, Jar: jar, HandshakeTimeout: 5 * time.Second},
	}
}

// open loads a page, follows redirects and checks where the browser ended up.
func (b *browser) open(path, wantPath string) string {
	b.t.Helper()
	res, err := b.client.Get(b.base + path)
	return b.read(res, err, wantPath)
}

// submit posts a form the way a browser does, including the Origin header.
func (b *browser) submit(path, wantPath string, form url.Values) string {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodPost, b.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", b.base)
	res, err := b.client.Do(req)
	return b.read(res, err, wantPath)
}

func (b *browser) read(res *http.Response, err error, wantPath string) string {
	b.t.Helper()
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || res.Request.URL.Path != wantPath {
		b.t.Fatalf("got %d at %q, want 200 at %q", res.StatusCode, res.Request.URL.Path, wantPath)
	}
	return string(body)
}

func (b *browser) signIn(mode, name, wantPath string) string {
	b.t.Helper()
	return b.submit("/login", wantPath, url.Values{"mode": {mode}, "username": {name}, "password": {testPassword}})
}

func (b *browser) chat() (*websocket.Conn, []chat.Message) {
	b.t.Helper()
	conn, res, err := b.dial(b.base)
	if err != nil {
		b.t.Fatalf("open chat: %v", err)
	}
	_ = res.Body.Close()
	b.t.Cleanup(func() { _ = conn.Close() })
	return conn, readUntil(b.t, conn, func(e chat.Event) bool { return e.Type == chat.EventHistory }).Messages
}

func (b *browser) dial(origin string) (*websocket.Conn, *http.Response, error) {
	address := "wss" + strings.TrimPrefix(b.base, "https") + "/ws"
	return b.dialer.Dial(address, http.Header{"Origin": {origin}})
}

func (b *browser) expectNoChat(origin string, status int) {
	b.t.Helper()
	conn, res, err := b.dial(origin)
	if err == nil {
		_ = conn.Close()
		b.t.Fatal("chat connection was accepted")
	}
	if res == nil {
		b.t.Fatalf("no HTTP response: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != status {
		b.t.Fatalf("status = %d, want %d", res.StatusCode, status)
	}
}

func expectRevoked(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
				t.Fatalf("connection ended with %v, want close code 1008", err)
			}
			return
		}
	}
}

func TestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	server := startE2EServer(t, dir)
	admin, alice, bob, mallory := server.newBrowser(t), server.newBrowser(t), server.newBrowser(t), server.newBrowser(t)

	// A visitor gets the login page and its assets, but no chat.
	if page := alice.open("/", "/login"); !strings.Contains(page, `name="password"`) {
		t.Fatal("login form is missing")
	}
	alice.open("/static/app.js", "/static/app.js")
	alice.open("/static/style.css", "/static/style.css")
	alice.expectNoChat(server.URL, http.StatusUnauthorized)

	for _, account := range []struct {
		name string
		*browser
	}{{"admin", admin}, {"alice", alice}, {"bob", bob}, {"mallory", mallory}} {
		mode := "register"
		if account.name == "admin" {
			mode = "login"
		}
		if page := account.signIn(mode, account.name, "/"); !strings.Contains(page, account.name) {
			t.Fatalf("chat page does not show %q", account.name)
		}
	}
	alice.open("/direct", "/direct")
	alice.expectNoChat("https://other.example", http.StatusForbidden)

	adminChat, _ := admin.chat()
	aliceChat, _ := alice.chat()
	bobChat, _ := bob.chat()
	malloryChat, _ := mallory.chat()
	readUntil(t, adminChat, func(e chat.Event) bool {
		return e.Type == chat.EventUsers && slices.Equal(e.Users, []string{"admin", "alice", "bob", "mallory"})
	})

	sendRaw(t, aliceChat, `{"message":"hello everyone"}`)
	for _, conn := range []*websocket.Conn{adminChat, aliceChat, bobChat, malloryChat} {
		readUntil(t, conn, func(e chat.Event) bool {
			return e.Type == chat.EventMessage && e.Username == "alice" && e.Message == "hello everyone"
		})
	}

	sendRaw(t, aliceChat, `{"message":"only for bob","to":"bob"}`)
	for _, conn := range []*websocket.Conn{aliceChat, bobChat} {
		readUntil(t, conn, func(e chat.Event) bool {
			return e.Type == chat.EventDirect && e.Username == "alice" && e.To == "bob" && e.Message == "only for bob"
		})
	}
	// The next public message proves that the direct message was skipped, without waiting for a timeout.
	sendRaw(t, bobChat, `{"message":"public marker"}`)
	for _, conn := range []*websocket.Conn{adminChat, malloryChat} {
		readUntil(t, conn, func(e chat.Event) bool {
			if e.Message == "only for bob" {
				t.Fatal("outsider received a direct message")
			}
			return e.Message == "public marker"
		})
	}

	list := admin.open("/admin/users", "/admin/users")
	if !strings.Contains(list, `data-user="mallory"`) {
		t.Fatal("admin page does not list mallory")
	}
	revokedCookies := map[string]*http.Cookie{"mallory": mallory.cookie(), "bob": bob.cookie()}
	link := regexp.MustCompile(`href="([^"]+)" data-user="mallory"`).FindStringSubmatch(list)
	if link == nil {
		t.Fatal("list does not link to the account")
	}
	mallorysPage := link[1]
	if page := admin.submit(mallorysPage, mallorysPage, url.Values{"action": {"ban"}}); !strings.Contains(page, "Konto wurde gesperrt.") {
		t.Fatal("ban was not confirmed")
	}
	expectRevoked(t, malloryChat)
	readUntil(t, adminChat, func(e chat.Event) bool {
		return e.Type == chat.EventUsers && slices.Equal(e.Users, []string{"admin", "alice", "bob"})
	})
	mallory.open("/", "/login")
	if page := mallory.signIn("login", "mallory", "/login"); !strings.Contains(page, "wurde gesperrt") {
		t.Fatal("banned user is not told about the ban")
	}

	bob.submit("/logout", "/login", nil)
	expectRevoked(t, bobChat)
	bob.open("/direct", "/login")

	// Keep this the last message before the restart: any later one would save the history and hide a lost write.
	readUntil(t, aliceChat, func(e chat.Event) bool {
		return e.Type == chat.EventUsers && slices.Equal(e.Users, []string{"admin", "alice"})
	})
	sendRaw(t, aliceChat, `{"message":"while bob was away","to":"bob"}`)
	readUntil(t, aliceChat, func(e chat.Event) bool {
		return e.Type == chat.EventSystem && strings.Contains(e.Message, "offline")
	})

	// Restart on the same database: everything stays, also the sessions.
	server.stop()
	server = startE2EServer(t, dir)
	aliceCookies := alice.client.Jar
	admin, alice, bob, mallory = server.newBrowser(t), server.newBrowser(t), server.newBrowser(t), server.newBrowser(t)
	// The host is unchanged, and cookie jars ignore the port, so this is the cookie from before the restart.
	returning := server.newBrowser(t)
	returning.client.Jar, returning.dialer.Jar = aliceCookies, aliceCookies
	if _, history := returning.chat(); !containsText(history, "while bob was away") {
		t.Fatal("session or history was lost in the restart")
	}
	for name, cookie := range revokedCookies {
		revoked := server.newBrowser(t)
		revoked.setCookie(cookie)
		revoked.expectNoChat(server.URL, http.StatusUnauthorized)
		if cookie.Value == "" {
			t.Fatalf("test lost the cookie of %s", name)
		}
	}

	if page := mallory.signIn("login", "mallory", "/login"); !strings.Contains(page, "wurde gesperrt") {
		t.Fatal("ban was lost in the restart")
	}
	admin.signIn("login", "admin", "/")
	if page := admin.submit(mallorysPage, mallorysPage, url.Values{"action": {"unban"}}); !strings.Contains(page, "Konto wurde entsperrt.") {
		t.Fatal("unban was not confirmed")
	}
	alice.signIn("login", "alice", "/")
	bob.signIn("login", "bob", "/")
	mallory.signIn("login", "mallory", "/")
	latecomer := server.newBrowser(t)
	latecomer.signIn("register", "latecomer", "/")

	for _, reader := range []struct {
		name string
		*browser
		direct bool
	}{{"admin", admin, false}, {"alice", alice, true}, {"bob", bob, true}, {"mallory", mallory, false}, {"latecomer", latecomer, false}} {
		_, history := reader.chat()
		if !containsText(history, "hello everyone") {
			t.Errorf("%s lost the public history", reader.name)
		}
		for _, private := range []string{"only for bob", "while bob was away"} {
			if got := containsText(history, private); got != reader.direct {
				t.Errorf("%s sees %q = %t, want %t", reader.name, private, got, reader.direct)
			}
		}
	}
}
