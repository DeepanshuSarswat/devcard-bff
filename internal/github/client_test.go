package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.Handler, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewClient(append([]Option{WithBaseURL(srv.URL)}, opts...)...)
}

func TestGetUser(t *testing.T) {
	var gotAuth, gotUA string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/octocat" {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotAuth, gotUA = r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		fmt.Fprint(w, `{"login":"octocat","name":"The Octocat","avatar_url":"a","bio":"b","followers":42,"html_url":"h"}`)
	}), WithToken("secret"))

	u, err := c.GetUser(context.Background(), "octocat")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	want := User{Login: "octocat", Name: "The Octocat", AvatarURL: "a", Bio: "b", Followers: 42, HTMLURL: "h"}
	if u != want {
		t.Errorf("got %+v, want %+v", u, want)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotUA == "" {
		t.Error("User-Agent not set (GitHub rejects requests without one)")
	}
}

func TestListReposPaginates(t *testing.T) {
	var pages []string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		n := reposPerPage // full page
		if page == "2" {
			n = 1 // short page => last
		}
		w.Write([]byte("["))
		for i := range n {
			if i > 0 {
				w.Write([]byte(","))
			}
			fmt.Fprintf(w, `{"name":"r%s-%d","stargazers_count":%d}`, page, i, i)
		}
		w.Write([]byte("]"))
	}))

	repos, err := c.ListRepos(context.Background(), "octocat")
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != reposPerPage+1 {
		t.Errorf("len = %d, want %d", len(repos), reposPerPage+1)
	}
	if len(pages) != 2 {
		t.Errorf("fetched pages %v, want [1 2]", pages)
	}
}

func TestListReposRespectsMaxPages(t *testing.T) {
	calls := 0
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Write([]byte("["))
		for i := range reposPerPage {
			if i > 0 {
				w.Write([]byte(","))
			}
			w.Write([]byte(`{"name":"x"}`))
		}
		w.Write([]byte("]"))
	}), WithMaxRepoPages(2))

	if _, err := c.ListRepos(context.Background(), "busy"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestErrorMapping(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name          string
		status        int
		header        map[string]string
		wantIs        error
		wantRetry     time.Duration
		wantStatusErr int
	}{
		{name: "not found", status: 404, wantIs: ErrNotFound},
		{name: "429 with retry-after", status: 429, header: map[string]string{"Retry-After": "30"}, wantIs: ErrRateLimited, wantRetry: 30 * time.Second},
		{
			name: "403 primary limit uses reset", status: 403,
			header: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(now.Add(90*time.Second).Unix(), 10)},
			wantIs: ErrRateLimited, wantRetry: 90 * time.Second,
		},
		{name: "403 secondary limit", status: 403, header: map[string]string{"Retry-After": "5"}, wantIs: ErrRateLimited, wantRetry: 5 * time.Second},
		{name: "429 without hints defaults to a minute", status: 429, wantIs: ErrRateLimited, wantRetry: time.Minute},
		{name: "403 not rate limit", status: 403, header: map[string]string{"X-RateLimit-Remaining": "4999"}, wantStatusErr: 403},
		{name: "500", status: 500, wantStatusErr: 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tt.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tt.status)
			}))
			c.now = func() time.Time { return now }

			_, err := c.GetUser(context.Background(), "someone")
			if err == nil {
				t.Fatal("expected error")
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("errors.Is(%v, %v) = false", err, tt.wantIs)
			}
			if tt.wantRetry != 0 {
				var rle *RateLimitError
				if !errors.As(err, &rle) {
					t.Fatalf("errors.As RateLimitError failed for %v", err)
				}
				if rle.RetryAfter != tt.wantRetry {
					t.Errorf("RetryAfter = %s, want %s", rle.RetryAfter, tt.wantRetry)
				}
			}
			if tt.wantStatusErr != 0 {
				var se *StatusError
				if !errors.As(err, &se) || se.StatusCode != tt.wantStatusErr {
					t.Errorf("want StatusError %d, got %v", tt.wantStatusErr, err)
				}
				if errors.Is(err, ErrRateLimited) {
					t.Error("non-rate-limit 403 must not be ErrRateLimited")
				}
			}
		})
	}
}

func TestContextDeadlinePropagates(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hang until the client gives up
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := c.GetUser(ctx, "slow")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
}

func TestBadJSON(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{not json`))
	}))
	if _, err := c.GetUser(context.Background(), "x"); err == nil {
		t.Fatal("expected decode error")
	}
	if _, err := c.ListRepos(context.Background(), "x"); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestPathEscaping(t *testing.T) {
	var raw string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.EscapedPath()
		w.Write([]byte(`{}`))
	}))
	_, _ = c.GetUser(context.Background(), "../admin")
	if raw != "/users/..%2Fadmin" {
		t.Errorf("escaped path = %q", raw)
	}
}
