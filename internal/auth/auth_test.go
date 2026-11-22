package auth

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Schenna43lp1/openchat/internal/store"
)

const testPassword = "test-only-password"

var operator = store.User{Username: "operator", Role: store.RoleAdmin}

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "openchat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s, err := New(st, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

// seed uses the cheapest bcrypt cost to keep the suite fast under the race detector.
func seed(t *testing.T, st *store.Store, name string, role store.Role, banned bool) store.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(t.Context(), store.User{
		Username: name, PasswordHash: string(hash), Role: role, Banned: banned, CreatedAt: time.UnixMilli(1_767_323_045_000),
	})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func list(t *testing.T, s *Service) []store.User {
	t.Helper()
	users, err := s.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return users
}

func TestRegister(t *testing.T) {
	s, _ := newTestService(t)
	got, err := s.Register(t.Context(), "  Alice  ", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "Alice" || got.Role != store.RoleUser || got.Banned || got.CreatedAt.IsZero() {
		t.Fatalf("account = %+v", got)
	}
	if got.PasswordHash == testPassword || bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte(testPassword)) != nil {
		t.Fatal("password is not stored as a matching bcrypt hash")
	}
	if found, err := s.Find(t.Context(), " ALICE "); err != nil || found != got {
		t.Fatalf("found = %+v, %v", found, err)
	}
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	s, st := newTestService(t)
	seed(t, st, "existing", store.RoleAdmin, false)
	before := list(t, s)
	for _, tc := range []struct {
		name, username, password string
		want                     error
	}{
		{"empty name", "", testPassword, ErrInvalidUsername},
		{"short name", "ab", testPassword, ErrInvalidUsername},
		{"long name", strings.Repeat("a", 33), testPassword, ErrInvalidUsername},
		{"inner space", "al ice", testPassword, ErrInvalidUsername},
		{"line break", "al\nice", testPassword, ErrInvalidUsername},
		{"markup", "<alice>", testPassword, ErrInvalidUsername},
		{"lookalike letter", "аlice", testPassword, ErrInvalidUsername},
		{"short password", "alice", "1234567", ErrInvalidPassword},
		{"four umlauts", "alice", strings.Repeat("ä", 4), ErrInvalidPassword},
		{"73 bytes", "alice", strings.Repeat("x", 73), ErrInvalidPassword},
		{"37 umlauts", "alice", strings.Repeat("ä", 37), ErrInvalidPassword},
		{"taken name", "existing", testPassword, ErrUsernameTaken},
		{"taken name in other case", "EXISTING", testPassword, ErrUsernameTaken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Register(t.Context(), tc.username, tc.password); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if got := list(t, s); !slices.Equal(got, before) {
				t.Fatalf("store changed to %+v", got)
			}
		})
	}
}

func TestRegisterAcceptsLimits(t *testing.T) {
	s, _ := newTestService(t)
	for name, password := range map[string]string{
		"a.b":                   "12345678",
		strings.Repeat("z", 32): strings.Repeat("x", 72),
		"umlauts":               strings.Repeat("ä", 36),
		"eight":                 strings.Repeat("ä", 8),
	} {
		if _, err := s.Register(t.Context(), name, password); err != nil {
			t.Errorf("Register(%q): %v", name, err)
		}
	}
}

func TestConcurrentRegistrationCreatesOneAccount(t *testing.T) {
	s, _ := newTestService(t)
	results := make([]error, 4)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() { _, results[i] = s.Register(context.Background(), "alice", testPassword) })
	}
	wg.Wait()

	created := 0
	for _, err := range results {
		if err == nil {
			created++
		} else if !errors.Is(err, ErrUsernameTaken) {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if created != 1 || len(list(t, s)) != 1 {
		t.Fatalf("created = %d, want 1", created)
	}
}

func TestAuthenticate(t *testing.T) {
	s, st := newTestService(t)
	seed(t, st, "Alice", store.RoleAdmin, false)
	seed(t, st, "blocked", store.RoleUser, true)
	for _, tc := range []struct {
		name, username, password string
		want                     error
	}{
		{"valid", "Alice", testPassword, nil},
		{"other case and spaces", " aLICE ", testPassword, nil},
		{"wrong password", "Alice", "wrong-password", ErrInvalidCredentials},
		{"empty password", "Alice", "", ErrInvalidCredentials},
		{"password is case sensitive", "Alice", strings.ToUpper(testPassword), ErrInvalidCredentials},
		{"unknown user", "ghost", testPassword, ErrInvalidCredentials},
		{"banned", "blocked", testPassword, ErrBanned},
		{"banned with wrong password", "blocked", "wrong-password", ErrInvalidCredentials},
	} {
		user, err := s.Authenticate(t.Context(), tc.username, tc.password)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
		if err != nil && user != (store.User{}) {
			t.Errorf("%s: failed login returned account data", tc.name)
		}
		if err == nil && user.Username != "Alice" {
			t.Errorf("%s: username = %q", tc.name, user.Username)
		}
	}
}

func TestUnknownUserCostsOnePasswordCheck(t *testing.T) {
	s, _ := newTestService(t)
	var costs []int
	s.compare = func(hash, _ []byte) error {
		cost, err := bcrypt.Cost(hash)
		if err != nil {
			t.Fatal(err)
		}
		costs = append(costs, cost)
		return bcrypt.ErrMismatchedHashAndPassword
	}
	if _, err := s.Authenticate(t.Context(), "ghost", testPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("error = %v", err)
	}
	if !slices.Equal(costs, []int{bcrypt.DefaultCost}) {
		t.Fatalf("checks = %v, want one at the default cost", costs)
	}
}

func TestEnsureAdmin(t *testing.T) {
	type existing struct {
		name   string
		role   store.Role
		banned bool
	}
	for _, tc := range []struct {
		name     string
		existing []existing
		username string
		password string
		want     error
		admin    bool // whether username is an admin afterwards
	}{
		{"fresh instance", nil, "root", testPassword, nil, true},
		{"own account", []existing{{"root", store.RoleUser, false}}, "ROOT", testPassword, nil, true},
		{"squatted name", []existing{{"root", store.RoleUser, false}}, "root", "operator-password", ErrAdminConflict, false},
		{"banned account", []existing{{"root", store.RoleUser, true}}, "root", testPassword, ErrAdminConflict, false},
		{"admin exists", []existing{{"boss", store.RoleAdmin, false}}, "root", testPassword, nil, false},
		{"only banned admin", []existing{{"boss", store.RoleAdmin, true}}, "root", testPassword, nil, true},
		{"invalid name", nil, "r", testPassword, ErrInvalidUsername, false},
		{"invalid password", nil, "root", "short", ErrInvalidPassword, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, st := newTestService(t)
			for _, e := range tc.existing {
				seed(t, st, e.name, e.role, e.banned)
			}
			if err := s.EnsureAdmin(t.Context(), tc.username, tc.password); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			u, err := s.Find(t.Context(), tc.username)
			if err != nil && !errors.Is(err, ErrUnknownUser) {
				t.Fatal(err)
			}
			if (u.Role == store.RoleAdmin) != tc.admin {
				t.Fatalf("account = %+v, want admin = %t", u, tc.admin)
			}
		})
	}
}

func TestAccountRules(t *testing.T) {
	s, st := newTestService(t)
	root := seed(t, st, "root", store.RoleAdmin, false)
	moderator := seed(t, st, "moderator", store.RoleModerator, false)
	user := seed(t, st, "user", store.RoleUser, false)
	seed(t, st, "former", store.RoleAdmin, true) // must not count as a remaining admin
	before := list(t, s)

	ban := func(actor store.User, name string) error {
		return s.SetBanned(t.Context(), actor, name, true)
	}
	for name, tc := range map[string]struct{ got, want error }{
		"invalid role":            {s.SetRole(t.Context(), root, "user", "owner"), ErrInvalidRole},
		"role of unknown user":    {s.SetRole(t.Context(), root, "ghost", store.RoleUser), ErrUnknownUser},
		"demotion of last admin":  {s.SetRole(t.Context(), root, "root", store.RoleModerator), ErrLastAdmin},
		"role change by staff":    {s.SetRole(t.Context(), moderator, "user", store.RoleAdmin), ErrForbidden},
		"ban of last admin":       {ban(operator, "root"), ErrLastAdmin},
		"ban of unknown user":     {ban(root, "ghost"), ErrUnknownUser},
		"self ban":                {ban(root, " ROOT "), ErrSelfBan},
		"moderator against staff": {ban(moderator, "root"), ErrStaffTarget},
		"ban by a user":           {ban(user, "moderator"), ErrForbidden},
	} {
		if !errors.Is(tc.got, tc.want) {
			t.Errorf("%s: error = %v, want %v", name, tc.got, tc.want)
		}
	}
	if got := list(t, s); !slices.Equal(got, before) {
		t.Fatalf("refused changes were stored: %+v", got)
	}

	if err := s.SetRole(t.Context(), root, "root", store.RoleAdmin); err != nil {
		t.Errorf("unchanged role: %v", err)
	}
	if err := s.SetRole(t.Context(), root, "former", store.RoleUser); err != nil {
		t.Errorf("demotion of a banned admin: %v", err)
	}
	for _, banned := range []bool{true, true, false, false} {
		if err := s.SetBanned(t.Context(), moderator, "user", banned); err != nil {
			t.Errorf("banned = %t: %v", banned, err)
		}
		if u, err := s.Find(t.Context(), "user"); err != nil || u.Banned != banned {
			t.Errorf("after banned = %t: %+v, %v", banned, u, err)
		}
	}
}

func TestConcurrentDemotionKeepsOneAdmin(t *testing.T) {
	s, st := newTestService(t)
	names := []string{"first", "second", "third", "fourth"}
	for _, name := range names {
		seed(t, st, name, store.RoleAdmin, false)
	}
	results := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() { results[i] = s.SetRole(context.Background(), operator, name, store.RoleUser) })
	}
	wg.Wait()

	refused := 0
	for _, err := range results {
		if errors.Is(err, ErrLastAdmin) {
			refused++
		} else if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if refused != 1 {
		t.Fatalf("refused demotions = %d, want 1", refused)
	}
}

func TestSessions(t *testing.T) {
	s, st := newTestService(t)
	alice := seed(t, st, "alice", store.RoleUser, false)
	first, _, err := s.StartSession(t.Context(), alice)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.StartSession(t.Context(), alice)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 43 {
		t.Fatal("tokens must be unique and encode 32 bytes")
	}
	if _, _, err := st.Session(t.Context(), []byte(first)); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("token is stored in plain text")
	}
	if u, err := s.Session(t.Context(), first); err != nil || u != alice {
		t.Fatalf("session = %+v, %v", u, err)
	}
	for _, token := range []string{"", "unknown"} {
		if _, err := s.Session(t.Context(), token); !errors.Is(err, ErrNoSession) {
			t.Errorf("token %q: %v", token, err)
		}
	}

	if err := st.CreateSession(t.Context(), hashToken("expired"), alice, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(t.Context(), "expired"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired: %v", err)
	}
	if _, _, err := st.Session(t.Context(), hashToken("expired")); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expired session was not removed")
	}
	if err := st.CreateSession(t.Context(), hashToken("stale"), alice, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveExpiredSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Session(t.Context(), hashToken("stale")); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("cleanup kept an expired session")
	}
}

func TestWatch(t *testing.T) {
	s, st := newTestService(t)
	alice := seed(t, st, "alice", store.RoleUser, false)
	laptop, _, err := s.StartSession(t.Context(), alice)
	if err != nil {
		t.Fatal(err)
	}
	phone, _, err := s.StartSession(t.Context(), alice)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(t.Context(), hashToken("short"), alice, time.Now().Add(1500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	onLaptop, onPhone, short := s.Watch(laptop), s.Watch(phone), s.Watch("short")
	expect := func(step string, check func(context.Context) (bool, error), want bool) {
		t.Helper()
		if got, err := check(t.Context()); err != nil || got != want {
			t.Fatalf("%s = %t, %v, want %t", step, got, err, want)
		}
	}
	expect("laptop", onLaptop, true)
	expect("phone", onPhone, true)
	expect("short", short, true)
	time.Sleep(1600 * time.Millisecond)
	expect("after expiry", short, false)

	if err := s.EndSession(t.Context(), laptop); err != nil {
		t.Fatal(err)
	}
	expect("after logout", onLaptop, false)
	expect("other session after logout", onPhone, true)
	if err := s.SetBanned(t.Context(), operator, "alice", true); err != nil {
		t.Fatal(err)
	}
	expect("after ban", onPhone, false)
}
