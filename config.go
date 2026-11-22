// Configuration from environment variables.
package main

import (
	"errors"
	"fmt"
	"strconv"
)

const (
	envAllowRegistration = "OPENCHAT_ALLOW_REGISTRATION"
	envAdminUsername     = "OPENCHAT_ADMIN_USERNAME"
	envAdminPassword     = "OPENCHAT_ADMIN_PASSWORD"
)

type config struct {
	allowRegistration bool
	adminUsername     string
	adminPassword     string
}

// loadConfig refuses invalid values, so a typo cannot silently weaken or break an instance.
func loadConfig(getenv func(string) string) (config, error) {
	var cfg config
	var err error
	if cfg.allowRegistration, err = envBool(getenv, envAllowRegistration, false); err != nil {
		return config{}, err
	}
	cfg.adminUsername, cfg.adminPassword = getenv(envAdminUsername), getenv(envAdminPassword)
	if (cfg.adminUsername == "") != (cfg.adminPassword == "") {
		return config{}, errors.New(envAdminUsername + " and " + envAdminPassword + " must be set together")
	}
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
