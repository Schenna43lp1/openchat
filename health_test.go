package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestHealthcheckCommand(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer server.Close()
	address, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{envPort: address.Port()}

	if out, errOut, err := runTestCommand(t, env, "healthcheck"); err != nil || out != "" || errOut != "" {
		t.Fatalf("healthy: %q, %q, %v", out, errOut, err)
	}
	status = http.StatusServiceUnavailable
	if _, _, err := runTestCommand(t, env, "healthcheck"); err == nil {
		t.Fatal("503 passed")
	}
	server.Close()
	if _, _, err := runTestCommand(t, env, "healthcheck"); err == nil {
		t.Fatal("stopped server passed")
	}
	if _, _, err := runTestCommand(t, map[string]string{envPort: "http"}, "healthcheck"); err == nil {
		t.Fatal("invalid configuration passed")
	}
}
