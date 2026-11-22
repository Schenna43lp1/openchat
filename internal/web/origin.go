package web

import (
	"net/http"
	"net/url"
	"strings"
)

// ParseOrigins reads a comma-separated list. Entries that are no origin are returned separately.
func ParseOrigins(raw string) (origins, invalid []string) {
	for part := range strings.SplitSeq(raw, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		if origin, ok := normalizeOrigin(part); ok {
			// Browsers send scheme and host in lower case.
			origins = append(origins, strings.ToLower(origin.Scheme+"://"+origin.Host))
		} else {
			invalid = append(invalid, part)
		}
	}
	return origins, invalid
}

func normalizeOrigin(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return nil, false
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, false
	}
	return parsed, true
}

// checkOrigin allows the own host and the configured origins.
func (s *server) checkOrigin(r *http.Request) bool {
	origin, ok := normalizeOrigin(r.Header.Get("Origin"))
	if !ok {
		return false
	}
	if host := strings.TrimSpace(r.Host); host != "" && strings.EqualFold(origin.Host, host) {
		return true
	}
	for _, allowed := range s.AllowedOrigins {
		if strings.EqualFold(origin.Scheme+"://"+origin.Host, allowed) {
			return true
		}
	}
	return false
}
