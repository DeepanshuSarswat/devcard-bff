// Package middleware holds the cross-cutting HTTP concerns: request IDs,
// access logging, panic recovery and CORS.
//
// Every middleware has the standard shape func(http.Handler) http.Handler,
// so they compose with each other and with any third-party middleware.
package middleware

import "net/http"

// Middleware wraps a handler with extra behaviour.
type Middleware func(http.Handler) http.Handler

// Chain applies mws so that the first one listed is the outermost, i.e.
// Chain(h, A, B) handles a request as A → B → h.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// statusRecorder captures the status code and body size a handler wrote,
// which http.ResponseWriter does not expose.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w, status: http.StatusOK}
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK) // mirrors what net/http does implicitly
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer (for
// Flush, SetWriteDeadline, ...) through our wrapper.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
