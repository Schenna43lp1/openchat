// Command openchat is a small self-hosted chat server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Schenna43lp1/openchat/internal/auth"
	"github.com/Schenna43lp1/openchat/internal/chat"
	"github.com/Schenna43lp1/openchat/internal/store"
	"github.com/Schenna43lp1/openchat/internal/web"
)

const (
	// Docker allows 10 seconds between SIGTERM and SIGKILL.
	shutdownTimeout = 8 * time.Second
	cleanupInterval = time.Hour
)

func main() {
	if len(os.Args) > 1 {
		if err := command(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "openchat:", err)
			if errors.Is(err, errUsage) {
				os.Exit(2)
			}
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := start(logger); err != nil {
		logger.Error("openchat stopped", "err", err)
		os.Exit(1)
	}
}

func command(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runCommand(ctx, args, os.Getenv, os.Stdout, os.Stderr)
}

func start(logger *slog.Logger) error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", fmt.Sprintf(":%d", cfg.port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return run(ctx, cfg, listener, logger)
}

// run serves on listener until ctx ends and then shuts down in the order that loses nothing:
// no new requests, then the connections, then the database.
func run(ctx context.Context, cfg config, listener net.Listener, logger *slog.Logger) (err error) {
	defer listener.Close() // for the returns before Serve owns it

	db, err := store.Open(ctx, cfg.dbFile)
	if err != nil {
		return fmt.Errorf("open storage as uid %d (on Docker volumes, run chown -R 65532:65532 on the data directory): %w", os.Getuid(), err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close database: %w", closeErr))
		}
	}()

	imported, err := db.ImportLegacy(ctx, store.LegacySources{Users: cfg.legacyUsersFile, History: cfg.legacyHistoryFile}, filepath.Dir(cfg.dbFile))
	if err != nil {
		return err
	}
	switch {
	case imported.Imported:
		logger.Info("imported data from v1.0.x", "users", imported.Users, "messages", imported.Messages, "skipped", imported.Skipped, "backup", imported.BackupDir)
	case imported.Ignored:
		logger.Warn("files from v1.0.x are ignored, the database already has data", "users_file", cfg.legacyUsersFile, "history_file", cfg.legacyHistoryFile)
	}

	accounts, err := auth.New(db, logger)
	if err != nil {
		return err
	}
	if cfg.adminUsername != "" {
		if err := accounts.EnsureAdmin(ctx, cfg.adminUsername, cfg.adminPassword); err != nil {
			return fmt.Errorf("create admin from %s: %w", envAdminUsername, err)
		}
	}
	hasAdmin, err := accounts.HasActiveAdmin(ctx)
	if err != nil {
		return err
	}
	if !hasAdmin {
		logger.Warn("no active admin, set " + envAdminUsername + " and " + envAdminPassword + " and restart")
	}
	if cfg.insecureCookie {
		logger.Warn("session cookie is sent over plain HTTP, use this only in a trusted network")
	}
	for _, origin := range cfg.invalidOrigins {
		logger.Warn("ignoring invalid origin", "variable", envAllowedOrigins, "value", origin)
	}

	hub := chat.NewHub(db, logger)
	handler, err := web.New(web.Options{
		Auth:              accounts,
		Hub:               hub,
		Health:            db.Ping,
		Keys:              db,
		Logger:            logger,
		AllowRegistration: cfg.allowRegistration,
		InsecureCookie:    cfg.insecureCookie,
		TrustedProxies:    cfg.trustedProxies,
		AllowedOrigins:    cfg.allowedOrigins,
	})
	if err != nil {
		return fmt.Errorf("configure routes: %w", err)
	}

	// Not derived from ctx: the hub and the cleanup end after the HTTP server, not together with it.
	background, stopBackground := context.WithCancel(context.WithoutCancel(ctx))
	hubDone, cleanupDone := make(chan struct{}), make(chan struct{})
	go func() {
		hub.Run(background)
		close(hubDone)
	}()
	go func() {
		removeExpiredSessions(background, accounts, logger)
		close(cleanupDone)
	}()
	defer func() {
		stopBackground()
		<-hubDone
		<-cleanupDone
		logger.Info("server stopped")
	}()

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// WebSockets are not affected, the connection deadlines end with the upgrade.
		WriteTimeout:   30 * time.Second,
		IdleTimeout:    2 * time.Minute,
		MaxHeaderBytes: 64 << 10,
		ErrorLog:       slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	logger.Info("server ready", "addr", listener.Addr().String(), "database", cfg.dbFile)

	select {
	case err := <-served:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutdown signal received")
	deadline, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(deadline); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		if err := server.Close(); err != nil {
			logger.Error("force close failed", "err", err)
		}
	}
	return nil
}

func removeExpiredSessions(ctx context.Context, accounts *auth.Service, logger *slog.Logger) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		if err := accounts.RemoveExpiredSessions(ctx); err != nil && ctx.Err() == nil {
			logger.Error("remove expired sessions", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
