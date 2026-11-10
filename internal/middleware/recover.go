package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/deepanshusarswat/devcard-bff/internal/httpx"
)

// Recover turns a panic in a handler into a logged 500 with the standard
// JSON error body. net/http would otherwise recover it too, but only by
// dropping the connection and printing an unstructured stack trace.
//
// It only catches panics on the request goroutine: a panic in a goroutine
// the handler started must be recovered inside that goroutine.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := newStatusRecorder(w)
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				// ErrAbortHandler is the sanctioned way to abort a response;
				// net/http handles it quietly, so let it through.
				if v == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value, compared by identity on purpose
					panic(v)
				}
				logger.ErrorContext(r.Context(), "panic recovered",
					"panic", fmt.Sprint(v),
					"stack", string(debug.Stack()),
				)
				if !rec.wroteHeader { // too late to change the status otherwise
					httpx.WriteError(rec, http.StatusInternalServerError, "internal_error", "internal server error")
				}
			}()
			next.ServeHTTP(rec, r)
		})
	}
}
