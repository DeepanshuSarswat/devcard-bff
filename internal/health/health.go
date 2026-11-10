// Package health serves the liveness and readiness probes.
package health

import (
	"net/http"
	"sync/atomic"

	"github.com/deepanshusarswat/devcard-bff/internal/httpx"
)

// Checker tracks whether the process should receive traffic.
//
// Liveness ("is the process wedged?") is always true while we can answer.
// Readiness ("should the load balancer send us requests?") flips to false
// when shutdown begins, so traffic drains before the listener closes.
// We deliberately do NOT ping GitHub here: if GitHub is down every replica
// would go unready at once, and probes would burn our rate limit.
type Checker struct {
	ready atomic.Bool
}

// New returns a Checker that starts out ready.
func New() *Checker {
	c := &Checker{}
	c.ready.Store(true)
	return c
}

// SetReady changes the readiness state. Safe for concurrent use.
func (c *Checker) SetReady(ready bool) { c.ready.Store(ready) }

// Register mounts /healthz and /readyz on mux.
func (c *Checker) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", c.liveness)
	mux.HandleFunc("GET /readyz", c.readiness)
}

func (c *Checker) liveness(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (c *Checker) readiness(w http.ResponseWriter, _ *http.Request) {
	if !c.ready.Load() {
		httpx.WriteError(w, http.StatusServiceUnavailable, "not_ready", "server is shutting down")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
