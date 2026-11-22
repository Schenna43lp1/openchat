package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/Schenna43lp1/openchat/internal/auth"
	"github.com/Schenna43lp1/openchat/internal/store"
)

const usage = `usage:
  openchat                        start the server
  openchat reset-password NAME    give an account a password that has to be changed at the next login

reset-password works on every account, also on admins, and keeps direct messages visible.
Stop the server first: open connections do not notice changes of another process.
It needs the same OPENCHAT_* variables and the same volume as the server.
`

var errUsage = errors.New("invalid arguments")

// runCommand handles everything but starting the server. Only a password goes to stdout, so that it can be
// piped and does not end up between log lines.
func runCommand(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	switch {
	case len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help"):
		_, err := io.WriteString(stdout, usage)
		return err
	case len(args) == 2 && args[0] == "reset-password":
		return resetPassword(ctx, args[1], getenv, stdout, stderr)
	default:
		_, _ = io.WriteString(stderr, usage)
		return errUsage
	}
}

func resetPassword(ctx context.Context, name string, getenv func(string) string, stdout, stderr io.Writer) (err error) {
	cfg, err := loadConfig(getenv)
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	// store.Open would create an empty database and hide a wrong path or a missing volume.
	if _, err := os.Stat(cfg.dbFile); err != nil {
		return fmt.Errorf("no database at %s (check the volume and %s): %w", cfg.dbFile, envDBFile, err)
	}
	db, err := store.Open(ctx, cfg.dbFile)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close database: %w", closeErr))
		}
	}()

	accounts, err := auth.New(db, slog.New(slog.NewTextHandler(stderr, nil)))
	if err != nil {
		return err
	}
	issued, err := accounts.OperatorReset(ctx, name)
	if err != nil {
		return fmt.Errorf("reset password of %q: %w", name, err)
	}
	if _, err := fmt.Fprintln(stdout, issued.Password); err != nil {
		return fmt.Errorf("print the new password, which is already set (run the command again): %w", err)
	}
	if issued.User.Banned {
		_, _ = fmt.Fprintln(stderr, "The account is still banned.")
	}
	_, _ = fmt.Fprintln(stderr, "The password has to be changed at the next login. A server that kept running has to be restarted.")
	return nil
}
