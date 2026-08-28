// Package ratelimit is a small in-memory request limiter for the handful
// of endpoints an anonymous visitor can reach.
//
// Deliberately in-process rather than Redis or a database table: this app
// runs as a single container (see docker-compose.yml), the state is
// worthless if lost, and a limiter that needs a round trip per request to
// decide whether to allow a request has the shape of the problem it's
// meant to solve. If this ever runs as more than one instance, each will
// enforce its own share of the limit — which degrades gracefully rather
// than failing open.
//
// This is not a general defence against a determined attacker; it's there
// so an ordinary script can't fill a unit's order queue or hammer a
// public form. Anything larger belongs at the network edge, in front of
// the app.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter allows at most Limit requests per key within Window, using a
// fixed window: the count resets in full once a key's window elapses.
//
// A fixed window is chosen over a sliding one on purpose. It's a few
// lines, its memory is bounded by the number of distinct keys seen in one
// window, and its known weakness — up to 2×Limit across a window
// boundary — doesn't matter for the thing this protects. A sliding window
// would mean storing a timestamp per request, which is more memory and
// more code for a distinction nobody here would notice.
type Limiter struct {
	mu      sync.Mutex
	windows map[string]*window
	limit   int
	window  time.Duration
	// now is swappable so tests can advance time without sleeping.
	now func() time.Time
}

type window struct {
	count     int
	expiresAt time.Time
}

// New returns a Limiter allowing limit requests per key per window.
func New(limit int, per time.Duration) *Limiter {
	return &Limiter{
		windows: make(map[string]*window),
		limit:   limit,
		window:  per,
		now:     time.Now,
	}
}

// Allow records a request against key and reports whether it's within the
// limit. A key with an expired window starts fresh.
//
// Sweeping expired entries happens here rather than in a background
// goroutine: this is the only place the map is touched, so there's no
// timer to leak and nothing to shut down. The sweep is bounded — see
// sweepLocked.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	w, ok := l.windows[key]
	if !ok || now.After(w.expiresAt) {
		l.sweepLocked(now)
		l.windows[key] = &window{count: 1, expiresAt: now.Add(l.window)}
		return true
	}

	w.count++
	return w.count <= l.limit
}

// Blocked reports whether key is already over its limit, WITHOUT counting
// this call against it.
//
// This is what lets a caller charge only for the attempts it cares about.
// The login form is the case that needs it: a Scout meeting is thirty
// families behind one router, so counting every login would let ordinary
// use exhaust a shared address. Checking Blocked first and recording only
// on a failed password means legitimate traffic is free and only guessing
// costs anything.
func (l *Limiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	w, ok := l.windows[key]
	if !ok || l.now().After(w.expiresAt) {
		return false
	}
	return w.count >= l.limit
}

// RetryAfter reports how long until key's current window resets. Zero if
// the key isn't currently limited — used to set a Retry-After header.
func (l *Limiter) RetryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	w, ok := l.windows[key]
	if !ok {
		return 0
	}
	if d := w.expiresAt.Sub(l.now()); d > 0 {
		return d
	}
	return 0
}

// sweepLocked drops expired entries so a stream of one-off keys can't grow
// the map without bound. Called only when a new or expired window is being
// written, so the cost is amortised against real traffic rather than a
// ticker, and never runs on the hot path of an already-tracked key.
func (l *Limiter) sweepLocked(now time.Time) {
	for k, w := range l.windows {
		if now.After(w.expiresAt) {
			delete(l.windows, k)
		}
	}
}
