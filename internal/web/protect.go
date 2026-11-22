package web

import (
	"net/http"
	"os"
	"strings"
)

const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"font-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", contentSecurityPolicy)
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		// Not no-referrer: browsers would then send "Origin: null" with forms and every POST would fail.
		header.Set("Referrer-Policy", "same-origin")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.HasPrefix(r.URL.Path, "/static/") {
			header.Set("Cache-Control", "no-cache")
		} else {
			header.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// filesOnly hides directories, which http.FileServer would list.
type filesOnly struct {
	fs http.FileSystem
}

func (f filesOnly) Open(name string) (http.File, error) {
	file, err := f.fs.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if info.IsDir() {
		_ = file.Close()
		return nil, os.ErrNotExist
	}
	return file, nil
}

// rejectCrossOrigin refuses state-changing browser requests from other origins, including same-site subdomains.
func rejectCrossOrigin(next http.Handler, trusted []string) (http.Handler, error) {
	protection := http.NewCrossOriginProtection()
	for _, origin := range trusted {
		if err := protection.AddTrustedOrigin(origin); err != nil {
			return nil, err
		}
	}
	return protection.Handler(next), nil
}
