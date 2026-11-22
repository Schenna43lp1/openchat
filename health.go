package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

const healthCheckTimeout = 3 * time.Second

// checkHealth asks the server in the same container. The image has no shell or curl for a container health check.
func checkHealth(ctx context.Context, getenv func(string) string) error {
	cfg, err := loadConfig(getenv)
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()

	address := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.port)) + "/healthz"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return fmt.Errorf("health check: %w", err)
	}
	// An own transport: a proxy from the environment has nothing to do with the own container.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("health check: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health check: %s", response.Status)
	}
	return nil
}
