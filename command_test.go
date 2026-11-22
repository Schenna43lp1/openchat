package main

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Schenna43lp1/openchat/internal/auth"
	"github.com/Schenna43lp1/openchat/internal/store"
)

func runTestCommand(t *testing.T, env map[string]string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = runCommand(t.Context(), args, envMap(env), &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestResetPasswordCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openchat.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := auth.New(db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.EnsureAdmin(t.Context(), "root", "forgotten-password"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runTestCommand(t, map[string]string{envDBFile: path}, "reset-password", "ROOT")
	if err != nil {
		t.Fatalf("%v: %s", err, errOut)
	}
	password := strings.TrimSuffix(out, "\n")
	if len(password) < 26 || strings.ContainsAny(password, " \n") {
		t.Fatalf("stdout is not only the password: %q", out)
	}
	if strings.Contains(errOut, password) {
		t.Fatal("password is in the log")
	}

	if db, err = store.Open(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if accounts, err = auth.New(db, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	user, err := accounts.Authenticate(t.Context(), "root", password)
	if err != nil || !user.MustChangePassword || user.Role != store.RoleAdmin {
		t.Fatalf("admin after the reset = %+v, %v", user, err)
	}
}

func TestResetPasswordCommandFailures(t *testing.T) {
	existing := filepath.Join(t.TempDir(), "openchat.db")
	db, err := store.Open(t.Context(), existing)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "openchat.db")

	for name, tc := range map[string]struct {
		env  map[string]string
		want error
	}{
		"unknown account":  {map[string]string{envDBFile: existing}, auth.ErrUnknownUser},
		"missing database": {map[string]string{envDBFile: missing}, os.ErrNotExist},
	} {
		if out, _, err := runTestCommand(t, tc.env, "reset-password", "ghost"); !errors.Is(err, tc.want) || out != "" {
			t.Errorf("%s: %q, %v", name, out, err)
		}
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the command created a database")
	}
}

func TestCommandArguments(t *testing.T) {
	for _, args := range [][]string{{"reset-password"}, {"reset-password", "a", "b"}, {"reset"}, {"--port", "80"}, {""}} {
		if out, errOut, err := runTestCommand(t, nil, args...); !errors.Is(err, errUsage) || out != "" || !strings.Contains(errOut, "usage:") {
			t.Errorf("%q: %q, %v", args, out, err)
		}
	}
	if out, _, err := runTestCommand(t, nil, "--help"); err != nil || !strings.Contains(out, "reset-password") {
		t.Errorf("help: %q, %v", out, err)
	}
}
