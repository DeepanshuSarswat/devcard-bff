// Package github is a minimal client for the parts of the GitHub REST API
// the devcard needs. It translates HTTP-level failures into typed Go errors
// so callers never have to inspect status codes themselves.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// User is the subset of GET /users/{username} we use.
type User struct {
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	Bio       string `json:"bio"`
	HTMLURL   string `json:"html_url"`
	Followers int    `json:"followers"`
}

// Repo is the subset of GET /users/{username}/repos we use.
type Repo struct {
	Name     string `json:"name"`
	HTMLURL  string `json:"html_url"`
	Language string `json:"language"`
	Stars    int    `json:"stargazers_count"`
	Fork     bool   `json:"fork"`
}

// Sentinel errors. Callers match them with errors.Is, which also sees
// through any fmt.Errorf("...: %w", err) wrapping added along the way.
var (
	ErrNotFound    = errors.New("github: not found")
	ErrRateLimited = errors.New("github: rate limited")
)

// RateLimitError carries how long the caller should wait. It reports itself
// as ErrRateLimited, so errors.Is(err, ErrRateLimited) works, while
// errors.As(err, &rle) gives access to RetryAfter.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("github: rate limited, retry after %s", e.RetryAfter)
}

// Is makes errors.Is(err, ErrRateLimited) true for *RateLimitError.
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// StatusError is any other non-2xx answer from GitHub.
type StatusError struct {
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("github: unexpected status %d", e.StatusCode)
}

const (
	defaultBaseURL = "https://api.github.com"
	reposPerPage   = 100
	// maxBodyBytes caps how much of an upstream response we will read, so a
	// misbehaving upstream cannot make us allocate unbounded memory.
	maxBodyBytes = 5 << 20 // 5 MiB
)

// Client talks to the GitHub REST API. The zero value is not usable; build
// one with NewClient. A Client is safe for concurrent use.
type Client struct {
	baseURL      string
	token        string
	httpClient   *http.Client
	maxRepoPages int
	now          func() time.Time
}

// Option configures a Client (the "functional options" pattern).
type Option func(*Client)

// WithBaseURL points the client at a different host, e.g. an httptest server.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithToken sends "Authorization: Bearer <token>" for higher rate limits.
func WithToken(t string) Option { return func(c *Client) { c.token = t } }

// WithHTTPClient replaces the underlying *http.Client.
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.httpClient = hc } }

// WithMaxRepoPages caps pagination (100 repos per page).
func WithMaxRepoPages(n int) Option { return func(c *Client) { c.maxRepoPages = n } }

// NewClient builds a Client with sane defaults, then applies opts.
func NewClient(opts ...Option) *Client {
	c := &Client{
		baseURL: defaultBaseURL,
		// No Timeout here on purpose: deadlines come from the request
		// context, so one knob (the context) controls cancellation end to end.
		httpClient:   &http.Client{},
		maxRepoPages: 3,
		now:          time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// GetUser fetches a user's public profile.
func (c *Client) GetUser(ctx context.Context, username string) (User, error) {
	var u User
	if err := c.getJSON(ctx, "/users/"+url.PathEscape(username), nil, &u); err != nil {
		return User{}, fmt.Errorf("get user %q: %w", username, err)
	}
	return u, nil
}

// ListRepos fetches the repositories a user owns, following pagination up
// to the configured page limit.
func (c *Client) ListRepos(ctx context.Context, username string) ([]Repo, error) {
	path := "/users/" + url.PathEscape(username) + "/repos"
	var all []Repo
	for page := 1; page <= c.maxRepoPages; page++ {
		q := url.Values{
			"type":     {"owner"},
			"sort":     {"pushed"},
			"per_page": {strconv.Itoa(reposPerPage)},
			"page":     {strconv.Itoa(page)},
		}
		var batch []Repo
		if err := c.getJSON(ctx, path, q, &batch); err != nil {
			return nil, fmt.Errorf("list repos %q page %d: %w", username, page, err)
		}
		all = append(all, batch...)
		if len(batch) < reposPerPage {
			break // last page
		}
	}
	return all, nil
}

// getJSON performs a GET and decodes a 200 response into dst. Every
// non-2xx response is turned into one of this package's error types.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, dst any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "devcard-bff")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// A context deadline surfaces here wrapped in *url.Error, which
		// implements Unwrap, so errors.Is(err, context.DeadlineExceeded)
		// still works for callers.
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if err := c.checkStatus(resp); err != nil {
		// Drain a little of the body so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return err
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(dst); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *Client) checkStatus(resp *http.Response) error {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusForbidden && isRateLimited(resp.Header):
		return &RateLimitError{RetryAfter: c.retryAfter(resp.Header)}
	default:
		return &StatusError{StatusCode: resp.StatusCode}
	}
}

// isRateLimited distinguishes a rate-limit 403 from a permissions 403.
// GitHub signals the primary limit with X-RateLimit-Remaining: 0 and the
// secondary (abuse) limit with a Retry-After header.
func isRateLimited(h http.Header) bool {
	return h.Get("X-RateLimit-Remaining") == "0" || h.Get("Retry-After") != ""
}

// retryAfter prefers Retry-After (seconds), then X-RateLimit-Reset (unix
// epoch), and falls back to one minute as GitHub's docs recommend.
func (c *Client) retryAfter(h http.Header) time.Duration {
	if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		if d := time.Unix(reset, 0).Sub(c.now()); d > 0 {
			return d.Round(time.Second)
		}
	}
	return time.Minute
}
