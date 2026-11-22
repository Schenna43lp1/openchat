// Protection that applies to every request.
package main

import (
	"net/http"
	"strings"
)

// rejectCrossOrigin refuses state-changing browser requests from other origins, including same-site subdomains.
func rejectCrossOrigin(next http.Handler) (http.Handler, error) {
	protection := http.NewCrossOriginProtection()
	for _, origin := range configuredAllowedOrigins() {
		if err := protection.AddTrustedOrigin(strings.ToLower(origin)); err != nil {
			return nil, err
		}
	}
	return protection.Handler(next), nil
}
