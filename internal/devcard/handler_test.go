package devcard

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepanshusarswat/devcard-bff/internal/cache"
	"github.com/deepanshusarswat/devcard-bff/internal/github"
	"github.com/deepanshusarswat/devcard-bff/internal/httpx"
)

const (
	userJSON  = `{"login":"octocat","name":"The Octocat","avatar_url":"https://avatars.example/o.png","bio":"hi","followers":100,"html_url":"https://github.com/octocat"}`
	reposJSON = `[
		{"name":"hello-world","stargazers_count":50,"language":"Go","html_url":"https://github.com/octocat/hello-world"},
		{"name":"spoon-knife","stargazers_count":10,"language":"TypeScript","html_url":"https://github.com/octocat/spoon-knife"},
		{"name":"a-fork","stargazers_count":999,"language":"C","fork":true}
	]`
)

// fakeGitHub is an httptest server standing in for api.github.com. Each
// test supplies its own route handlers; call counters let tests assert how
// many times "GitHub" was actually hit.
type fakeGitHub struct {
	srv       *httptest.Server
	userCalls atomic.Int32
	repoCalls atomic.Int32
}

func newFakeGitHub(t *testing.T, user, repos http.HandlerFunc) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{u}", func(w http.ResponseWriter, r *http.Request) {
		f.userCalls.Add(1)
		user(w, r)
	})
	mux.HandleFunc("GET /users/{u}/repos", func(w http.ResponseWriter, r *http.Request) {
		f.repoCalls.Add(1)
		repos(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func respond(status int, body string, headers ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

type testApp struct {
	handler http.Handler
	gh      *fakeGitHub
}

func newTestApp(t *testing.T, gh *fakeGitHub, ttl, timeout time.Duration) testApp {
	t.Helper()
	client := github.NewClient(github.WithBaseURL(gh.srv.URL))
	svc := NewService(client, cache.New[string, Card](ttl), timeout)
	mux := http.NewServeMux()
	NewHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(mux)
	return testApp{handler: mux, gh: gh}
}

func (a testApp) get(t *testing.T, username string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/devcard/"+username, nil))
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) httpx.ErrorDetail {
	t.Helper()
	var body httpx.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v (raw %q)", err, rec.Body.String())
	}
	return body.Error
}

func TestHandlerSuccess(t *testing.T) {
	gh := newFakeGitHub(t, respond(200, userJSON), respond(200, reposJSON))
	app := newTestApp(t, gh, time.Minute, time.Second)

	rec := app.get(t, "octocat")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc == "" {
		t.Error("Cache-Control not set")
	}

	var card Card
	if err := json.NewDecoder(rec.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	if card.Profile.Name != "The Octocat" || card.Profile.Followers != 100 {
		t.Errorf("profile = %+v", card.Profile)
	}
	if card.TotalStars != 60 {
		t.Errorf("totalStars = %d, want 60 (fork excluded)", card.TotalStars)
	}
	if len(card.TopRepos) != 2 || card.TopRepos[0].Name != "hello-world" {
		t.Errorf("topRepos = %+v", card.TopRepos)
	}
	if len(card.Languages) != 2 || card.Languages[0].Percent != 50 {
		t.Errorf("languages = %+v", card.Languages)
	}
}

func TestHandlerErrors(t *testing.T) {
	reset := strconv.FormatInt(time.Now().Add(42*time.Second).Unix(), 10)
	tests := []struct {
		name           string
		user, repos    http.HandlerFunc
		wantStatus     int
		wantCode       string
		wantRetryAfter bool
	}{
		{
			name:       "user not found",
			user:       respond(404, `{"message":"Not Found"}`),
			repos:      respond(404, `{"message":"Not Found"}`),
			wantStatus: http.StatusNotFound, wantCode: "user_not_found",
		},
		{
			name:       "primary rate limit (403)",
			user:       respond(403, `{}`, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", reset),
			repos:      respond(200, `[]`),
			wantStatus: http.StatusServiceUnavailable, wantCode: "rate_limited", wantRetryAfter: true,
		},
		{
			name:       "secondary rate limit (429) on repos only",
			user:       respond(200, userJSON),
			repos:      respond(429, `{}`, "Retry-After", "7"),
			wantStatus: http.StatusServiceUnavailable, wantCode: "rate_limited", wantRetryAfter: true,
		},
		{
			name:       "upstream 500",
			user:       respond(500, `oops`),
			repos:      respond(200, `[]`),
			wantStatus: http.StatusBadGateway, wantCode: "upstream_error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTestApp(t, newFakeGitHub(t, tt.user, tt.repos), time.Minute, time.Second)
			rec := app.get(t, "octocat")

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body)
			}
			if got := decodeError(t, rec); got.Code != tt.wantCode || got.Message == "" {
				t.Errorf("error = %+v, want code %q", got, tt.wantCode)
			}
			ra := rec.Header().Get("Retry-After")
			if tt.wantRetryAfter {
				if n, err := strconv.Atoi(ra); err != nil || n < 1 {
					t.Errorf("Retry-After = %q, want positive integer seconds", ra)
				}
			} else if ra != "" {
				t.Errorf("unexpected Retry-After %q", ra)
			}
		})
	}
}

func TestHandlerUpstreamTimeout(t *testing.T) {
	hang := func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }
	app := newTestApp(t, newFakeGitHub(t, hang, hang), time.Minute, 50*time.Millisecond)

	start := time.Now()
	rec := app.get(t, "slowpoke")
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", rec.Code)
	}
	if got := decodeError(t, rec).Code; got != "upstream_timeout" {
		t.Errorf("code = %q", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %s; the timeout was not enforced", elapsed)
	}
}

func TestHandlerInvalidUsername(t *testing.T) {
	gh := newFakeGitHub(t, respond(200, userJSON), respond(200, reposJSON))
	app := newTestApp(t, gh, time.Minute, time.Second)

	for _, name := range []string{"-bad", "bad-", "dou--ble", "has_underscore", "a.b", "x123456789012345678901234567890123456789"} {
		rec := app.get(t, name)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400", name, rec.Code)
		}
	}
	if gh.userCalls.Load() != 0 {
		t.Error("invalid usernames must not reach GitHub")
	}
}

func TestHandlerCacheHit(t *testing.T) {
	gh := newFakeGitHub(t, respond(200, userJSON), respond(200, reposJSON))
	app := newTestApp(t, gh, time.Minute, time.Second)

	// Different casing on purpose: logins are case-insensitive.
	for _, name := range []string{"octocat", "octocat", "OctoCat"} {
		if rec := app.get(t, name); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	if u, r := gh.userCalls.Load(), gh.repoCalls.Load(); u != 1 || r != 1 {
		t.Errorf("upstream calls: user=%d repos=%d, want 1 and 1", u, r)
	}
}

func TestHandlerErrorsAreNotCached(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	user := func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			respond(500, "")(w, r)
			return
		}
		respond(200, userJSON)(w, r)
	}
	gh := newFakeGitHub(t, user, respond(200, reposJSON))
	app := newTestApp(t, gh, time.Minute, time.Second)

	if rec := app.get(t, "octocat"); rec.Code != http.StatusBadGateway {
		t.Fatalf("first: status = %d", rec.Code)
	}
	fail.Store(false)
	if rec := app.get(t, "octocat"); rec.Code != http.StatusOK {
		t.Fatalf("second: status = %d, a failure was cached", rec.Code)
	}
}

func TestHandlerSingleflight(t *testing.T) {
	const n = 20
	started := make(chan struct{}, n)
	release := make(chan struct{})

	// The user endpoint blocks until released, so all n requests pile up on
	// the same in-flight fetch.
	user := func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		respond(200, userJSON)(w, r)
	}
	gh := newFakeGitHub(t, user, respond(200, reposJSON))
	// A 1ns TTL makes the cache useless, so only singleflight can dedupe.
	app := newTestApp(t, gh, time.Nanosecond, 5*time.Second)

	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = app.get(t, "octocat").Code
		}()
	}

	<-started // the one upstream call is in progress
	// Give the other goroutines time to reach DoChan and join the flight.
	// If any arrived late they would trigger a second call and fail the test,
	// so this sleep can only cause a false failure, never a false pass.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("request %d: status %d", i, c)
		}
	}
	if got := gh.userCalls.Load(); got != 1 {
		t.Errorf("upstream user calls = %d, want 1", got)
	}
}

func TestValidUsername(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"a", true},
		{"octocat", true},
		{"Octo-Cat-1", true},
		{"123456789012345678901234567890123456789", true}, // 39 chars
		{"", false},
		{"1234567890123456789012345678901234567890", false}, // 40 chars
		{"-x", false},
		{"x-", false},
		{"x--y", false},
		{"x_y", false},
		{"émile", false},
	}
	for _, tt := range tests {
		if got := validUsername(tt.in); got != tt.want {
			t.Errorf("validUsername(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
