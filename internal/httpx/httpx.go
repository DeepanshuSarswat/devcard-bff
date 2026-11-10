// Package httpx holds small JSON response helpers shared by every handler,
// so all endpoints emit the same error envelope.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ErrorBody is the wire shape of every error response:
//
//	{"error": {"code": "user_not_found", "message": "..."}}
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail carries a stable machine-readable code (for the frontend to
// switch on) and a human-readable message (for logs and toasts).
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON encodes v as the response body with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent, so the only thing left to do is record it.
		slog.Warn("httpx: encode response", "err", err)
	}
}

// WriteError writes the standard error envelope.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}
