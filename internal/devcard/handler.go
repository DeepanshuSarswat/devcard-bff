package devcard

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/deepanshusarswat/devcard-bff/internal/github"
	"github.com/deepanshusarswat/devcard-bff/internal/httpx"
)

// CardGetter is what the handler needs from the service layer.
type CardGetter interface {
	Get(ctx context.Context, username string) (Card, error)
}

// Handler serves the devcard endpoint.
type Handler struct {
	svc    CardGetter
	logger *slog.Logger
}

// NewHandler returns a Handler backed by svc.
func NewHandler(svc CardGetter, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

// Register mounts the routes on mux using Go 1.22 method+wildcard patterns.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/devcard/{username}", h.getCard)
}

// statusClientClosedRequest is nginx's non-standard 499. The client never
// sees it, but it keeps access logs honest about why no 200 was sent.
const statusClientClosedRequest = 499

func (h *Handler) getCard(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if !validUsername(username) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_username",
			"username must be 1-39 characters of letters, digits or single hyphens, not starting or ending with a hyphen")
		return
	}

	card, err := h.svc.Get(r.Context(), username)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	// Let the browser reuse the card briefly too; the server cache absorbs the rest.
	w.Header().Set("Cache-Control", "public, max-age=60")
	httpx.WriteJSON(w, http.StatusOK, card)
}

// writeError is the single place upstream failures become HTTP responses.
// The order matters: more specific checks come first.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var rle *github.RateLimitError
	switch {
	case errors.Is(err, github.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "user_not_found", "GitHub user not found")

	case errors.As(err, &rle):
		// Our caller cannot fix a rate limit, so it is "service unavailable",
		// not GitHub's 403. Retry-After tells the client when to try again.
		secs := int(math.Ceil(rle.RetryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
		h.logger.WarnContext(r.Context(), "github rate limit hit", "retry_after_s", secs)
		httpx.WriteError(w, http.StatusServiceUnavailable, "rate_limited", "GitHub rate limit reached, please retry later")

	case errors.Is(err, context.DeadlineExceeded):
		h.logger.WarnContext(r.Context(), "github request timed out", "err", err)
		httpx.WriteError(w, http.StatusGatewayTimeout, "upstream_timeout", "GitHub did not respond in time")

	case errors.Is(err, context.Canceled):
		h.logger.DebugContext(r.Context(), "client went away", "err", err)
		w.WriteHeader(statusClientClosedRequest)

	default:
		h.logger.ErrorContext(r.Context(), "github request failed", "err", err)
		httpx.WriteError(w, http.StatusBadGateway, "upstream_error", "failed to fetch data from GitHub")
	}
}

// validUsername applies GitHub's login rules: 1–39 ASCII letters, digits or
// hyphens, no leading/trailing hyphen, no double hyphen. Rejecting bad input
// here saves an upstream call and keeps junk out of cache keys.
func validUsername(s string) bool {
	if len(s) == 0 || len(s) > 39 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' && s[i-1] != '-':
		default:
			return false
		}
	}
	return true
}
