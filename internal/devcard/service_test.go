package devcard

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepanshusarswat/devcard-bff/internal/cache"
	"github.com/deepanshusarswat/devcard-bff/internal/github"
)

// stubGitHub implements GitHubClient with plain funcs: the payoff of
// defining a small interface at the consumer.
type stubGitHub struct {
	getUser   func(ctx context.Context, u string) (github.User, error)
	listRepos func(ctx context.Context, u string) ([]github.Repo, error)
	calls     atomic.Int32
}

func (s *stubGitHub) GetUser(ctx context.Context, u string) (github.User, error) {
	s.calls.Add(1)
	return s.getUser(ctx, u)
}

func (s *stubGitHub) ListRepos(ctx context.Context, u string) ([]github.Repo, error) {
	return s.listRepos(ctx, u)
}

func okUser(context.Context, string) (github.User, error) { return github.User{Login: "x"}, nil }
func okRepos(context.Context, string) ([]github.Repo, error) {
	return []github.Repo{{Name: "r", Stars: 1}}, nil
}

func TestServiceFirstErrorWinsAndCancelsSibling(t *testing.T) {
	siblingCancelled := make(chan struct{})
	stub := &stubGitHub{
		getUser: func(context.Context, string) (github.User, error) { return github.User{}, github.ErrNotFound },
		listRepos: func(ctx context.Context, _ string) ([]github.Repo, error) {
			<-ctx.Done() // only returns once the failing sibling cancels us
			close(siblingCancelled)
			return nil, ctx.Err()
		},
	}
	svc := NewService(stub, cache.New[string, Card](time.Minute), 5*time.Second)

	_, err := svc.Get(context.Background(), "x")
	if !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (not the sibling's context.Canceled)", err)
	}
	select {
	case <-siblingCancelled:
	case <-time.After(time.Second):
		t.Fatal("sibling request was not cancelled")
	}
}

func TestServiceCallerCancelDoesNotAbortSharedFetch(t *testing.T) {
	release := make(chan struct{})
	stub := &stubGitHub{
		getUser: func(ctx context.Context, u string) (github.User, error) {
			select {
			case <-release:
				return okUser(ctx, u)
			case <-ctx.Done():
				return github.User{}, ctx.Err()
			}
		},
		listRepos: okRepos,
	}
	c := cache.New[string, Card](time.Minute)
	svc := NewService(stub, c, 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := svc.Get(ctx, "x")
		errCh <- err
	}()

	for stub.calls.Load() == 0 { // wait until the fetch has started
		time.Sleep(time.Millisecond)
	}
	cancel() // the first caller hangs up...
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller err = %v, want context.Canceled", err)
	}

	close(release) // ...but the shared fetch completes and fills the cache
	deadline := time.Now().Add(time.Second)
	for c.Len() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("fetch was aborted by the caller's cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := svc.Get(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1", got)
	}
}

func TestServiceRecoversPanic(t *testing.T) {
	stub := &stubGitHub{
		getUser:   func(context.Context, string) (github.User, error) { panic("boom") },
		listRepos: okRepos,
	}
	svc := NewService(stub, cache.New[string, Card](time.Minute), time.Second)

	if _, err := svc.Get(context.Background(), "x"); err == nil {
		t.Fatal("expected error from panicking fetch")
	}

	// And through the handler it is a clean 502, not a crash.
	mux := http.NewServeMux()
	NewHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/devcard/x", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

func TestHandlerClientCancelled(t *testing.T) {
	stub := &stubGitHub{
		getUser: func(ctx context.Context, _ string) (github.User, error) {
			<-ctx.Done()
			return github.User{}, ctx.Err()
		},
		listRepos: okRepos,
	}
	svc := NewService(stub, cache.New[string, Card](time.Minute), 100*time.Millisecond)
	mux := http.NewServeMux()
	NewHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(mux)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/devcard/x", nil).WithContext(ctx))
	if rec.Code != statusClientClosedRequest {
		t.Errorf("status = %d, want %d", rec.Code, statusClientClosedRequest)
	}
}
