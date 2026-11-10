package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// Logger writes one structured access-log line per request.
func Logger(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := newStatusRecorder(w)

			next.ServeHTTP(rec, r)

			level := slog.LevelInfo
			switch {
			case rec.status >= 500:
				level = slog.LevelError
			case r.URL.Path == "/healthz" || r.URL.Path == "/readyz":
				level = slog.LevelDebug // probes fire every few seconds; keep them out of INFO
			}
			// LogAttrs with typed attrs avoids the reflection/allocations of
			// the key-value variadic form, which matters on the hot path.
			logger.LogAttrs(r.Context(), level, "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int("bytes", rec.bytes),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
				slog.String("remote_addr", r.RemoteAddr),
				slog.String("user_agent", r.UserAgent()),
			)
		})
	}
}
