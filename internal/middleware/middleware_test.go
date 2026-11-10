package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ok(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "hello") }

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestChainOrder(t *testing.T) {
	var order []string
	mark := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := Chain(http.HandlerFunc(ok), mark("a"), mark("b"), mark("c"))
	serve(h, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := strings.Join(order, ","); got != "a,b,c" {
		t.Errorf("order = %s, want a,b,c", got)
	}
}

func TestRequestID(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	}))

	t.Run("generates when missing", func(t *testing.T) {
		rec := serve(h, httptest.NewRequest(http.MethodGet, "/", nil))
		got := rec.Header().Get(HeaderRequestID)
		if len(got) != 32 || got != seen {
			t.Errorf("header %q, context %q", got, seen)
		}
	})
	t.Run("propagates a valid incoming id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(HeaderRequestID, "frontend-abc.123")
		rec := serve(h, req)
		if got := rec.Header().Get(HeaderRequestID); got != "frontend-abc.123" || seen != got {
			t.Errorf("header %q, context %q", got, seen)
		}
	})
	t.Run("replaces an invalid incoming id", func(t *testing.T) {
		for _, bad := range []string{"has space", "new\nline", strings.Repeat("x", 129)} {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(HeaderRequestID, bad)
			if got := serve(h, req).Header().Get(HeaderRequestID); got == bad || len(got) != 32 {
				t.Errorf("for %q got %q", bad, got)
			}
		}
	})
	t.Run("empty without middleware", func(t *testing.T) {
		if id := RequestIDFrom(httptest.NewRequest(http.MethodGet, "/", nil).Context()); id != "" {
			t.Errorf("got %q", id)
		}
	})
}

func jsonLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(ContextHandler{slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})})
}

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("log line is not JSON: %s", line)
		}
		out = append(out, m)
	}
	return out
}

func TestLogger(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		handler    http.HandlerFunc
		wantStatus float64
		wantBytes  float64
		wantLevel  string
	}{
		{"implicit 200", "/x", ok, 200, 5, "INFO"},
		{"explicit status", "/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }, 404, 0, "INFO"},
		{"server error", "/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }, 502, 0, "ERROR"},
		{"probe is debug", "/healthz", ok, 200, 5, "DEBUG"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			h := Chain(tt.handler, RequestID, Logger(jsonLogger(&buf)))
			serve(h, httptest.NewRequest(http.MethodGet, tt.path, nil))

			lines := decodeLines(t, &buf)
			if len(lines) != 1 {
				t.Fatalf("want 1 log line, got %d", len(lines))
			}
			l := lines[0]
			if l["status"] != tt.wantStatus || l["bytes"] != tt.wantBytes || l["level"] != tt.wantLevel {
				t.Errorf("log = %v", l)
			}
			if l["method"] != "GET" || l["path"] != tt.path {
				t.Errorf("log = %v", l)
			}
			if id, _ := l["request_id"].(string); len(id) != 32 {
				t.Errorf("request_id missing from access log: %v", l)
			}
			if _, ok := l["duration_ms"].(float64); !ok {
				t.Errorf("duration_ms missing: %v", l)
			}
		})
	}
}

func TestContextHandlerSurvivesWith(t *testing.T) {
	var buf bytes.Buffer
	base := jsonLogger(&buf)
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base.With("component", "x").WithGroup("g").InfoContext(r.Context(), "hi", "k", "v")
	}))
	serve(h, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(buf.String(), `"request_id"`) {
		t.Errorf("request_id lost through With/WithGroup: %s", buf.String())
	}
}

func TestRecover(t *testing.T) {
	var buf bytes.Buffer
	logger := jsonLogger(&buf)

	t.Run("panic becomes JSON 500", func(t *testing.T) {
		buf.Reset()
		h := Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("kaboom") }))
		rec := serve(h, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != 500 {
			t.Fatalf("status = %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"code":"internal_error"`) {
			t.Errorf("body = %s", rec.Body)
		}
		if !strings.Contains(buf.String(), "kaboom") || !strings.Contains(buf.String(), "stack") {
			t.Errorf("panic not logged with stack: %s", buf.String())
		}
	})

	t.Run("panic after headers keeps original status", func(t *testing.T) {
		h := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			panic("late")
		}))
		rec := serve(h, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
			t.Errorf("status = %d body = %q", rec.Code, rec.Body)
		}
	})

	t.Run("ErrAbortHandler is re-panicked", func(t *testing.T) {
		h := Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
		defer func() {
			if v := recover(); v != http.ErrAbortHandler { //nolint:errorlint // identity check on purpose
				t.Errorf("recovered %v, want http.ErrAbortHandler", v)
			}
		}()
		serve(h, httptest.NewRequest(http.MethodGet, "/", nil))
	})

	t.Run("no panic passes through", func(t *testing.T) {
		rec := serve(Recover(logger)(http.HandlerFunc(ok)), httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != 200 || rec.Body.String() != "hello" {
			t.Errorf("status = %d body = %q", rec.Code, rec.Body)
		}
	})
}

func TestCORS(t *testing.T) {
	const app = "http://localhost:5173"
	tests := []struct {
		name        string
		allowed     string
		method      string
		origin      string
		preflight   bool
		wantStatus  int
		wantACAO    string
		wantMethods bool
		wantNext    bool
	}{
		{name: "allowed simple request", allowed: app, method: "GET", origin: app, wantStatus: 200, wantACAO: app, wantNext: true},
		{name: "disallowed origin", allowed: app, method: "GET", origin: "https://evil.example", wantStatus: 200, wantNext: true},
		{name: "no origin (curl, server-to-server)", allowed: app, method: "GET", wantStatus: 200, wantNext: true},
		{name: "allowed preflight", allowed: app, method: "OPTIONS", origin: app, preflight: true, wantStatus: 204, wantACAO: app, wantMethods: true},
		{name: "disallowed preflight", allowed: app, method: "OPTIONS", origin: "https://evil.example", preflight: true, wantStatus: 204},
		{name: "list of origins", allowed: "https://a.example, " + app, method: "GET", origin: app, wantStatus: 200, wantACAO: app, wantNext: true},
		{name: "wildcard", allowed: "*", method: "GET", origin: "https://anything.example", wantStatus: 200, wantACAO: "https://anything.example", wantNext: true},
		{name: "plain OPTIONS is not a preflight", allowed: app, method: "OPTIONS", origin: app, wantStatus: 200, wantACAO: app, wantNext: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextCalled := false
			h := CORS(tt.allowed)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				ok(w, r)
			}))
			req := httptest.NewRequest(tt.method, "/api/v1/devcard/x", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.preflight {
				req.Header.Set("Access-Control-Request-Method", "GET")
			}
			rec := serve(h, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tt.wantACAO {
				t.Errorf("ACAO = %q, want %q", got, tt.wantACAO)
			}
			if got := rec.Header().Get("Access-Control-Allow-Methods") != ""; got != tt.wantMethods {
				t.Errorf("Allow-Methods present = %v, want %v", got, tt.wantMethods)
			}
			if nextCalled != tt.wantNext {
				t.Errorf("next called = %v, want %v", nextCalled, tt.wantNext)
			}
			if !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Origin") {
				t.Error("Vary: Origin missing")
			}
		})
	}
}

func TestStatusRecorderUnwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	if newStatusRecorder(rec).Unwrap() != rec {
		t.Error("Unwrap must return the wrapped writer")
	}
}
