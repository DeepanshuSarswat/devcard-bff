// Package cache provides a small, generic, in-memory TTL cache.
package cache

import (
	"context"
	"sync"
	"time"
)

// Cache maps keys to values that expire ttl after they were set.
// It is safe for concurrent use. The zero value is not usable; call New.
//
// K must be comparable because it is a map key; V can be anything.
type Cache[K comparable, V any] struct {
	mu         sync.RWMutex
	items      map[K]entry[V]
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
}

type entry[V any] struct {
	value     V
	expiresAt time.Time
}

// Option configures a Cache.
type Option func(*options)

type options struct {
	maxEntries int
	now        func() time.Time
}

// WithMaxEntries bounds memory. When full, Set first drops expired entries
// and, if still full, evicts an arbitrary entry. Zero means unbounded.
func WithMaxEntries(n int) Option { return func(o *options) { o.maxEntries = n } }

// WithClock injects a time source, so tests can move time forward instead
// of sleeping.
func WithClock(now func() time.Time) Option { return func(o *options) { o.now = now } }

// New creates a cache whose entries live for ttl.
func New[K comparable, V any](ttl time.Duration, opts ...Option) *Cache[K, V] {
	o := options{now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	return &Cache[K, V]{
		items:      make(map[K]entry[V]),
		ttl:        ttl,
		maxEntries: o.maxEntries,
		now:        o.now,
	}
}

// Get returns the value for key and whether it was present and fresh.
// Expired entries are treated as missing (and left for the janitor or the
// next Set to remove, so Get only ever needs the read lock).
func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	e, ok := c.items[key]
	c.mu.RUnlock()

	if !ok || !c.now().Before(e.expiresAt) {
		var zero V // the idiomatic "nothing" for a type parameter
		return zero, false
	}
	return e.value, true
}

// Set stores value under key, replacing any previous value and resetting
// its TTL.
func (c *Cache[K, V]) Set(key K, value V) {
	now := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.items[key]; !exists && c.maxEntries > 0 && len(c.items) >= c.maxEntries {
		c.deleteExpiredLocked(now)
		if len(c.items) >= c.maxEntries {
			for k := range c.items { // map order is unspecified: effectively random eviction
				delete(c.items, k)
				break
			}
		}
	}
	c.items[key] = entry[V]{value: value, expiresAt: now.Add(c.ttl)}
}

// Delete removes key if present.
func (c *Cache[K, V]) Delete(key K) {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
}

// Len reports the number of stored entries, including expired ones not yet
// swept.
func (c *Cache[K, V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// DeleteExpired removes every expired entry.
func (c *Cache[K, V]) DeleteExpired() {
	now := c.now()
	c.mu.Lock()
	c.deleteExpiredLocked(now)
	c.mu.Unlock()
}

// deleteExpiredLocked requires c.mu to be held for writing. The "Locked"
// suffix is a common Go convention for that precondition.
func (c *Cache[K, V]) deleteExpiredLocked(now time.Time) {
	for k, e := range c.items {
		if !now.Before(e.expiresAt) {
			delete(c.items, k) // deleting during range is explicitly allowed in Go
		}
	}
}

// RunJanitor sweeps expired entries every interval until ctx is cancelled.
// It blocks; start it with `go c.RunJanitor(ctx, time.Minute)`. Leaving the
// `go` to the caller keeps concurrency visible at the call site.
func (c *Cache[K, V]) RunJanitor(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.DeleteExpired()
		}
	}
}
