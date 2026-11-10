package health

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbes(t *testing.T) {
	c := New()
	mux := http.NewServeMux()
	c.Register(mux)

	get := func(path string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}

	if got := get("/healthz"); got != http.StatusOK {
		t.Errorf("/healthz = %d, want 200", got)
	}
	if got := get("/readyz"); got != http.StatusOK {
		t.Errorf("/readyz = %d, want 200", got)
	}

	c.SetReady(false)
	if got := get("/readyz"); got != http.StatusServiceUnavailable {
		t.Errorf("/readyz after SetReady(false) = %d, want 503", got)
	}
	if got := get("/healthz"); got != http.StatusOK {
		t.Errorf("/healthz must stay 200 while draining, got %d", got)
	}
}
