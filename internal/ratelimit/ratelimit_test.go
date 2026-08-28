package ratelimit

import (
	"sync"
	"testing"
	"time"
)

func TestLimiter_AllowsUpToLimitThenBlocks(t *testing.T) {
	l := New(3, time.Hour)

	for i := 1; i <= 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("request %d should have been allowed", i)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Error("the 4th request should have been blocked")
	}
	// Still blocked on subsequent tries — not just the one over.
	if l.Allow("1.2.3.4") {
		t.Error("the 5th request should also have been blocked")
	}
}

func TestLimiter_KeysAreIndependent(t *testing.T) {
	l := New(2, time.Hour)

	for i := 0; i < 2; i++ {
		l.Allow("1.1.1.1")
	}
	if l.Allow("1.1.1.1") {
		t.Fatal("first key should be exhausted")
	}
	// One visitor hitting their limit must not lock anybody else out.
	if !l.Allow("2.2.2.2") {
		t.Error("a different key should be unaffected")
	}
}

func TestLimiter_WindowResets(t *testing.T) {
	l := New(2, time.Hour)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	l.Allow("k")
	l.Allow("k")
	if l.Allow("k") {
		t.Fatal("should be limited within the window")
	}

	now = now.Add(61 * time.Minute)
	if !l.Allow("k") {
		t.Error("the window should have reset after it elapsed")
	}
}

func TestLimiter_RetryAfter(t *testing.T) {
	l := New(1, time.Hour)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	if d := l.RetryAfter("unseen"); d != 0 {
		t.Errorf("an unseen key should report 0, got %v", d)
	}
	l.Allow("k")
	now = now.Add(20 * time.Minute)
	if d := l.RetryAfter("k"); d != 40*time.Minute {
		t.Errorf("RetryAfter = %v, want 40m", d)
	}
	now = now.Add(2 * time.Hour)
	if d := l.RetryAfter("k"); d != 0 {
		t.Errorf("a lapsed window should report 0, got %v", d)
	}
}

// TestLimiter_SweepsExpiredKeys guards the memory bound: this map is fed
// by anonymous requests, so a stream of one-off keys must not grow it
// without limit.
func TestLimiter_SweepsExpiredKeys(t *testing.T) {
	l := New(5, time.Minute)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	for i := 0; i < 500; i++ {
		l.Allow(string(rune('a'+i%26)) + time.Duration(i).String())
		now = now.Add(time.Second)
	}
	// Everything older than a minute should have been dropped along the
	// way, so the map holds roughly one window's worth, not all 500.
	l.mu.Lock()
	size := len(l.windows)
	l.mu.Unlock()
	if size > 100 {
		t.Errorf("limiter is holding %d keys after 500 one-off requests — expired entries aren't being swept", size)
	}
}

// TestLimiter_ConcurrentUseIsSafe — every request handler shares one
// Limiter, so this runs under -race in CI.
func TestLimiter_ConcurrentUseIsSafe(t *testing.T) {
	l := New(1000, time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				l.Allow("shared")
				l.RetryAfter("shared")
			}
		}(i)
	}
	wg.Wait()

	l.mu.Lock()
	count := l.windows["shared"].count
	l.mu.Unlock()
	if count != 1000 {
		t.Errorf("counted %d of 1000 concurrent requests — updates were lost", count)
	}
}
