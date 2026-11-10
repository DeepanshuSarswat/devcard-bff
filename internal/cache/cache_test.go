package cache

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock. The mutex matters: the race
// detector would flag reads from cache goroutines otherwise.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func newClock() *fakeClock { return &fakeClock{t: time.Unix(0, 0)} }

func TestGetSet(t *testing.T) {
	c := New[string, int](time.Minute)
	if _, ok := c.Get("missing"); ok {
		t.Fatal("Get on empty cache returned ok")
	}
	c.Set("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("Get(a) = %v, %v", v, ok)
	}
	c.Set("a", 2)
	if v, _ := c.Get("a"); v != 2 {
		t.Fatalf("overwrite: Get(a) = %v", v)
	}
	c.Delete("a")
	if _, ok := c.Get("a"); ok {
		t.Fatal("Get after Delete returned ok")
	}
}

func TestExpiry(t *testing.T) {
	clk := newClock()
	c := New[string, string](10*time.Second, WithClock(clk.Now))
	c.Set("k", "v")

	tests := []struct {
		advance time.Duration
		wantOK  bool
	}{
		{0, true},
		{9 * time.Second, true},        // t=9s
		{999 * time.Millisecond, true}, // t=9.999s
		{1 * time.Millisecond, false},  // t=10s: expiry is exclusive
		{time.Hour, false},             // long gone
	}
	for _, tt := range tests {
		clk.Advance(tt.advance)
		if _, ok := c.Get("k"); ok != tt.wantOK {
			t.Errorf("at %s: ok = %v, want %v", clk.Now().Sub(time.Unix(0, 0)), ok, tt.wantOK)
		}
	}
}

func TestSetResetsTTL(t *testing.T) {
	clk := newClock()
	c := New[string, int](10*time.Second, WithClock(clk.Now))
	c.Set("k", 1)
	clk.Advance(8 * time.Second)
	c.Set("k", 2)
	clk.Advance(8 * time.Second) // 16s after first Set, 8s after second
	if v, ok := c.Get("k"); !ok || v != 2 {
		t.Fatalf("Get = %v, %v; want 2, true", v, ok)
	}
}

func TestDeleteExpired(t *testing.T) {
	clk := newClock()
	c := New[int, int](time.Second, WithClock(clk.Now))
	c.Set(1, 1)
	clk.Advance(2 * time.Second)
	c.Set(2, 2)

	c.DeleteExpired()
	if c.Len() != 1 {
		t.Fatalf("Len = %d, want 1", c.Len())
	}
	if _, ok := c.Get(2); !ok {
		t.Fatal("fresh entry was swept")
	}
}

func TestMaxEntries(t *testing.T) {
	clk := newClock()
	c := New[string, int](time.Minute, WithClock(clk.Now), WithMaxEntries(2))

	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("a", 10) // overwrite must not evict
	if c.Len() != 2 {
		t.Fatalf("Len after overwrite = %d", c.Len())
	}

	c.Set("c", 3) // full: evicts one arbitrary entry
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatal("newest entry missing")
	}

	// When full of expired entries, those go first and nothing live is lost.
	clk.Advance(2 * time.Minute)
	c.Set("d", 4)
	c.Set("e", 5)
	for _, k := range []string{"d", "e"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("%s missing", k)
		}
	}
}

func TestRunJanitor(t *testing.T) {
	clk := newClock()
	c := New[string, int](time.Second, WithClock(clk.Now))
	c.Set("k", 1)
	clk.Advance(time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.RunJanitor(ctx, time.Millisecond)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for c.Len() != 0 {
		select {
		case <-deadline:
			t.Fatal("janitor never swept the expired entry")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done // RunJanitor must return once ctx is cancelled
}

func TestConcurrentAccess(t *testing.T) {
	// Meaningful only under -race: it proves the locking is complete.
	c := New[string, int](time.Minute, WithMaxEntries(50))
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				k := strconv.Itoa((g*500 + i) % 100)
				c.Set(k, i)
				c.Get(k)
				if i%50 == 0 {
					c.DeleteExpired()
					c.Delete(k)
				}
			}
		}()
	}
	wg.Wait()
	if c.Len() > 50 {
		t.Fatalf("Len = %d exceeds max", c.Len())
	}
}
