package mailer

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Staying under the mail provider's sending rate.
//
// Fastmail counts one message per recipient, and enforces a daily limit,
// an hourly limit at half of that, and a ten-minute limit at half again —
// so the ten-minute window, roughly a quarter of the daily allowance, is
// what a burst actually runs into. A unit newsletter is nowhere near a
// paid plan's daily cap; sending it as fast as the network allows is what
// could trip the short window.
//
// There is a second reason to pace that has nothing to do with Fastmail:
// a hundred messages fired off in a few seconds, to a mix of Gmail and
// Outlook addresses, is shaped like a spam burst to the RECEIVING side
// too. Spreading them out is better for whether the mail arrives at all,
// which for a recruiting campaign is the entire point.
//
// Transactional mail — password resets, event reminders, the welcome
// email — deliberately does NOT go through this. Those are one message,
// sent while somebody waits, and making a password reset queue behind a
// newsletter would be a bad trade. Only the two bulk senders pace.

// defaultBulkPerMinute is the send rate used when nothing is configured.
//
// Chosen to be obviously safe rather than optimal: at 60 a minute a
// hundred-family newsletter takes under two minutes, which no leader will
// notice, while staying far below any paid plan's ten-minute allowance.
// A site that knows its own limits can raise it; see config.MailBulkPerMinute.
const defaultBulkPerMinute = 60

// Pacer spaces out the messages of one bulk send.
//
// One per batch, not one shared globally: two campaigns running at once
// is not a case worth the coordination, and the 429 handling below is
// what actually catches it if the combined rate is ever too high.
type Pacer struct {
	interval time.Duration
	mu       sync.Mutex
	last     time.Time
}

// NewPacer builds a pacer for perMinute messages a minute. Zero or
// negative means "no pacing" — Wait returns immediately, which is what a
// site that has turned it off gets.
func NewPacer(perMinute int) *Pacer {
	if perMinute <= 0 {
		return &Pacer{}
	}
	return &Pacer{interval: time.Minute / time.Duration(perMinute)}
}

// BulkPacer returns a pacer for one batch, at this Mailer's configured rate.
func (m *Mailer) BulkPacer() *Pacer { return NewPacer(m.cfg.BulkPerMinute) }

// Wait blocks until the next message is due, or until ctx is done.
//
// Returns ctx.Err() on cancellation so a caller can stop a part-sent
// batch cleanly rather than carrying on past its deadline.
func (p *Pacer) Wait(ctx context.Context) error {
	if p == nil || p.interval <= 0 {
		return nil
	}

	p.mu.Lock()
	now := time.Now()
	next := p.last.Add(p.interval)
	if next.Before(now) {
		next = now
	}
	p.last = next
	p.mu.Unlock()

	delay := time.Until(next)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// rateLimitBackoff is how long to wait before retrying a message the
// provider rate-limited. Deliberately longer than the pacer's own
// interval: being told to slow down means the current rate is wrong, so
// the answer is to actually stop for a moment, not to try again straight
// away. A var so a test doesn't have to wait it out.
var rateLimitBackoff = 30 * time.Second

// rateLimited reports whether an error is the provider saying "too fast".
//
// Safe to retry for the same reason a 401 is: 429 means the request was
// refused before the server acted on it, so no message was delivered and
// a second attempt cannot duplicate one.
func rateLimited(err error) bool {
	var se *jmapStatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusTooManyRequests
}

// SendHTMLPaced is SendHTML for one message of a bulk send: it waits its
// turn, and if the provider rate-limits it anyway, backs off once and
// tries again.
//
// One retry, not a loop. If a single backoff isn't enough the configured
// rate is wrong and the fix is configuration, not an unbounded retry that
// turns a slow send into a stuck one.
func (m *Mailer) SendHTMLPaced(ctx context.Context, p *Pacer, to, subject, body string) error {
	if err := p.Wait(ctx); err != nil {
		return err
	}

	err := m.SendHTML(ctx, to, subject, body)
	if !rateLimited(err) {
		return err
	}

	timer := time.NewTimer(rateLimitBackoff)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	return m.SendHTML(ctx, to, subject, body)
}
