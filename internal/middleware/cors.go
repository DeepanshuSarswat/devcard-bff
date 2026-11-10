package middleware

import (
	"net/http"
	"strings"
)

// CORS allows browser calls from the configured frontend origin(s).
// allowed is a comma-separated list ("http://localhost:5173,https://app.example.com")
// or "*" for any origin.
//
// It must sit outside the router: the 1.22 mux would answer a preflight
// OPTIONS request with 405 because our routes are registered as GET only.
func CORS(allowed string) Middleware {
	allowAll := false
	origins := make(map[string]struct{}) // map[T]struct{} is Go's set; struct{} takes zero bytes
	for _, o := range strings.Split(allowed, ",") {
		o = strings.TrimSpace(o)
		switch o {
		case "":
		case "*":
			allowAll = true
		default:
			origins[o] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			// The response differs by Origin, so shared caches must key on it.
			h.Add("Vary", "Origin")

			origin := r.Header.Get("Origin")
			_, listed := origins[origin]
			ok := origin != "" && (allowAll || listed)
			if ok {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After")
			}

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if ok {
					h.Add("Vary", "Access-Control-Request-Method")
					h.Add("Vary", "Access-Control-Request-Headers")
					h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
					h.Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")
					h.Set("Access-Control-Max-Age", "600")
				}
				// A disallowed preflight still gets 204 but no allow headers;
				// the browser then blocks the real request itself.
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
