package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
)

// HeaderRequestID is read from incoming requests and echoed on responses.
const HeaderRequestID = "X-Request-ID"

// ctxKey is unexported and struct-typed, so no other package can collide
// with (or overwrite) our context value. Never use a bare string as a key.
type ctxKey struct{}

// RequestID reuses a well-formed incoming X-Request-ID (so a trace can span
// the frontend, proxies and us) or generates a new one, stores it in the
// request context and sets it on the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

// RequestIDFrom returns the request ID stored in ctx, or "".
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error on supported platforms
	return hex.EncodeToString(b[:])
}

// validRequestID accepts short, printable, header-safe IDs only, so a
// client cannot inject newlines or huge values into our logs.
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		switch c := id[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// ContextHandler is a slog.Handler decorator that adds the request ID from
// the context to every record. Any code that logs with
// logger.InfoContext(ctx, ...) gets correlated logs for free.
type ContextHandler struct {
	slog.Handler
}

// Handle implements slog.Handler.
func (h ContextHandler) Handle(ctx context.Context, rec slog.Record) error {
	if id := RequestIDFrom(ctx); id != "" {
		rec.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, rec)
}

// WithAttrs implements slog.Handler; it must re-wrap, or derived loggers
// (logger.With(...)) would silently lose the request ID.
func (h ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ContextHandler{h.Handler.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler.
func (h ContextHandler) WithGroup(name string) slog.Handler {
	return ContextHandler{h.Handler.WithGroup(name)}
}
