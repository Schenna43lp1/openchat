package main

import (
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"strconv"

	"github.com/Schenna43lp1/openchat/internal/web"
)

const (
	envPort              = "OPENCHAT_PORT"
	envDBFile            = "OPENCHAT_DB_FILE"
	envAllowRegistration = "OPENCHAT_ALLOW_REGISTRATION"
	envAdminUsername     = "OPENCHAT_ADMIN_USERNAME"
	envAdminPassword     = "OPENCHAT_ADMIN_PASSWORD"
	envCookieSecure      = "OPENCHAT_COOKIE_SECURE"
	envTrustedProxies    = "OPENCHAT_TRUSTED_PROXIES"
	envAllowedOrigins    = "OPENCHAT_ALLOWED_ORIGINS"
	// Only read by the import from v1.0.x.
	envUsersFile   = "OPENCHAT_USERS_FILE"
	envHistoryFile = "OPENCHAT_CHAT_HISTORY_FILE"

	defaultPort   = 8080
	defaultDBName = "openchat.db"
)

type config struct {
	port              int
	dbFile            string
	legacyUsersFile   string
	legacyHistoryFile string
	allowRegistration bool
	insecureCookie    bool
	adminUsername     string
	adminPassword     string
	trustedProxies    []netip.Prefix
	allowedOrigins    []string
	invalidOrigins    []string
}

// loadConfig refuses invalid values, so a typo cannot silently weaken or break an instance.
func loadConfig(getenv func(string) string) (config, error) {
	get := func(key, fallback string) string {
		if value := getenv(key); value != "" {
			return value
		}
		return fallback
	}

	cfg := config{
		legacyUsersFile:   get(envUsersFile, filepath.Join("data", "users.json")),
		legacyHistoryFile: get(envHistoryFile, filepath.Join("data", "chat-history.json")),
		adminUsername:     getenv(envAdminUsername),
		adminPassword:     getenv(envAdminPassword),
	}
	// Next to the old accounts, because that is the place a v1.0.x setup keeps on its volume.
	cfg.dbFile = get(envDBFile, filepath.Join(filepath.Dir(cfg.legacyUsersFile), defaultDBName))
	for _, legacy := range []string{cfg.legacyUsersFile, cfg.legacyHistoryFile} {
		if filepath.Clean(legacy) == filepath.Clean(cfg.dbFile) {
			return config{}, fmt.Errorf("%s is also a file of v1.0.x (%s), set %s to a new file", cfg.dbFile, legacy, envDBFile)
		}
	}

	var err error
	if cfg.port, err = strconv.Atoi(get(envPort, strconv.Itoa(defaultPort))); err != nil || cfg.port < 1 || cfg.port > 65535 {
		return config{}, fmt.Errorf("%s must be a port from 1 to 65535, got %q", envPort, getenv(envPort))
	}
	if cfg.allowRegistration, err = envBool(getenv, envAllowRegistration, false); err != nil {
		return config{}, err
	}
	secure, err := envBool(getenv, envCookieSecure, true)
	if err != nil {
		return config{}, err
	}
	cfg.insecureCookie = !secure
	if (cfg.adminUsername == "") != (cfg.adminPassword == "") {
		return config{}, errors.New(envAdminUsername + " and " + envAdminPassword + " must be set together")
	}
	if cfg.trustedProxies, err = web.ParseTrustedProxies(getenv(envTrustedProxies)); err != nil {
		return config{}, fmt.Errorf("%s: %w", envTrustedProxies, err)
	}
	cfg.allowedOrigins, cfg.invalidOrigins = web.ParseOrigins(getenv(envAllowedOrigins))
	return cfg, nil
}

func envBool(getenv func(string) string, key string, fallback bool) (bool, error) {
	raw := getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false, got %q", key, raw)
	}
	return value, nil
}
