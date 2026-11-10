package devcard

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/deepanshusarswat/devcard-bff/internal/cache"
	"github.com/deepanshusarswat/devcard-bff/internal/github"
)

// GitHubClient is the slice of the upstream client this package needs.
// Defining the interface here, at the consumer, rather than in package
// github, is idiomatic Go: *github.Client satisfies it implicitly, and
// tests can substitute a tiny fake without any mocking library.
type GitHubClient interface {
	GetUser(ctx context.Context, username string) (github.User, error)
	ListRepos(ctx context.Context, username string) ([]github.Repo, error)
}

// Service builds devcards, caching results and collapsing concurrent
// requests for the same user into one upstream fetch.
//
// Cached Cards are shared between callers and must be treated as read-only.
type Service struct {
	gh      GitHubClient
	cache   *cache.Cache[string, Card]
	group   singleflight.Group // zero value is ready to use
	timeout time.Duration
}

// NewService wires a Service. timeout bounds each upstream fetch.
func NewService(gh GitHubClient, c *cache.Cache[string, Card], timeout time.Duration) *Service {
	return &Service{gh: gh, cache: c, timeout: timeout}
}

// Get returns the card for username from cache, or fetches it.
func (s *Service) Get(ctx context.Context, username string) (Card, error) {
	// GitHub logins are case-insensitive; normalise so "Octocat" and
	// "octocat" share one cache entry and one in-flight fetch.
	key := strings.ToLower(username)

	if card, ok := s.cache.Get(key); ok {
		return card, nil
	}

	// DoChan (rather than Do) lets each waiting caller give up on its own
	// context while the shared fetch carries on for everyone else.
	ch := s.group.DoChan(key, func() (any, error) {
		// A flight that finished just before we joined may have filled the cache.
		if card, ok := s.cache.Get(key); ok {
			return card, nil
		}

		// The fetch is shared by every waiter, so it must not die just because
		// the *first* caller disconnected. WithoutCancel keeps ctx's values
		// (request ID for logs) but drops its cancellation; we then add our own
		// deadline so the fetch is still bounded.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.timeout)
		defer cancel()

		card, err := s.fetchSafely(fetchCtx, username)
		if err != nil {
			return Card{}, err // errors are not cached: the next request retries
		}
		s.cache.Set(key, card)
		return card, nil
	})

	select {
	case <-ctx.Done():
		return Card{}, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return Card{}, res.Err
		}
		return res.Val.(Card), nil
	}
}

// fetchSafely converts a panic into an error. With DoChan, singleflight
// re-panics on a fresh goroutine, which no HTTP recovery middleware can
// catch, so one bad upstream payload would otherwise kill the process.
// recover only sees panics on its own goroutine, so fetch's child
// goroutines need (and have) their own recovery too.
func (s *Service) fetchSafely(ctx context.Context, username string) (card Card, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("devcard: panic while building card for %q: %v", username, r)
		}
	}()
	return s.fetch(ctx, username)
}

// fetch calls GitHub for the profile and the repos concurrently. If either
// call fails, the other is cancelled and the first error wins. This is the
// pattern errgroup.WithContext packages up; it is spelled out here because
// only singleflight is allowed from x/sync.
func (s *Service) fetch(ctx context.Context, username string) (Card, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	fail := func(err error) {
		once.Do(func() {
			firstErr = err
			cancel() // stop the sibling request early
		})
	}
	spawn := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					fail(fmt.Errorf("devcard: panic while fetching %q: %v", username, r))
				}
			}()
			if err := fn(); err != nil {
				fail(err)
			}
		}()
	}

	var (
		user  github.User
		repos []github.Repo
	)
	// Each closure writes only its own variable, so there is no shared write.
	spawn(func() (err error) {
		user, err = s.gh.GetUser(ctx, username)
		return err
	})
	spawn(func() (err error) {
		repos, err = s.gh.ListRepos(ctx, username)
		return err
	})
	// wg.Wait establishes happens-before with each goroutine's Done, so the
	// plain reads of user, repos and firstErr below are race-free.
	wg.Wait()

	if firstErr != nil {
		return Card{}, firstErr
	}
	return Build(user, repos), nil
}
