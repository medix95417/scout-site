package mailer

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Pacing is about elapsed time, so these measure it — with generous
// margins, since a loaded CI runner's timers are not precise.

func TestPacerSpacesMessages(t *testing.T) {
	// 600 a minute = 100ms apart. Four messages means three gaps.
	p := NewPacer(600)
	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := p.Wait(t.Context()); err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)

	if elapsed < 250*time.Millisecond {
		t.Errorf("four messages took %v, want at least ~300ms — they are not being spaced", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("four messages took %v, far longer than the 300ms of pacing asked for", elapsed)
	}
}

// The first message of a batch must not wait — a leader clicking Send
// should see it start, not sit for an interval first.
func TestPacerDoesNotDelayTheFirstMessage(t *testing.T) {
	p := NewPacer(6) // ten seconds apart
	start := time.Now()
	if err := p.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the first message waited %v; it should go straight out", elapsed)
	}
}

// A site that knows it doesn't need pacing can turn it off, and the zero
// value must be harmless.
func TestPacerDisabled(t *testing.T) {
	for name, p := range map[string]*Pacer{
		"negative rate": NewPacer(-1),
		"zero rate":     NewPacer(0),
		"zero value":    {},
		"nil":           nil,
	} {
		start := time.Now()
		for i := 0; i < 50; i++ {
			if err := p.Wait(t.Context()); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("%s: 50 waits took %v, expected no pacing", name, elapsed)
		}
	}
}

// A batch that outlives its context must stop, not keep sleeping through
// a deadline that has already passed.
func TestPacerStopsWhenTheContextEnds(t *testing.T) {
	p := NewPacer(1) // a minute apart
	if err := p.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := p.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Wait returned %v, want a deadline error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Wait slept %v past a 50ms deadline", elapsed)
	}
}

// Being told "too fast" is the provider's own signal, and is the case the
// pacing exists to avoid — but if it happens anyway, the message should
// still go out rather than being recorded as a failure.
func TestRateLimitedSendBacksOffAndRetries(t *testing.T) {
	srv := newCountingJMAPServer(t, "sender@example.com")
	cfg := testJMAPConfig()
	m := New(cfg, nil)

	orig := rateLimitBackoff
	rateLimitBackoff = 10 * time.Millisecond
	defer func() { rateLimitBackoff = orig }()

	srv.rateLimitOnce.Store(true)
	if err := m.SendHTMLPaced(t.Context(), NewPacer(0), "a@example.com", "S", "B"); err != nil {
		t.Fatalf("a rate-limited message was not retried: %v", err)
	}
	if got := srv.submits.Load(); got != 2 {
		t.Errorf("submit attempts = %d, want 2 (the 429, then the retry)", got)
	}
}

// One retry, not a loop: if a single backoff isn't enough, the configured
// rate is wrong and the fix is configuration, not a stuck send.
func TestRateLimitedSendRetriesOnlyOnce(t *testing.T) {
	srv := newCountingJMAPServer(t, "sender@example.com")
	cfg := testJMAPConfig()
	m := New(cfg, nil)

	orig := rateLimitBackoff
	rateLimitBackoff = 10 * time.Millisecond
	defer func() { rateLimitBackoff = orig }()

	srv.rateLimitAlways.Store(true)
	if err := m.SendHTMLPaced(t.Context(), NewPacer(0), "a@example.com", "S", "B"); err == nil {
		t.Fatal("a permanently rate-limited send reported success")
	}
	if got := srv.submits.Load(); got != 2 {
		t.Errorf("submit attempts = %d, want exactly 2", got)
	}
}

// 429 is retried because the request was refused before the server acted.
// Anything that might already have delivered must not be.
func TestOnlyRateLimitsTriggerBackoff(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"429", &jmapStatusError{StatusCode: http.StatusTooManyRequests}, true},
		{"500", &jmapStatusError{StatusCode: 500}, false},
		{"401", &jmapStatusError{StatusCode: 401}, false},
		{"network", errors.New("connection reset"), false},
		{"nil", nil, false},
	} {
		if got := rateLimited(tc.err); got != tc.want {
			t.Errorf("%s: rateLimited = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Transactional mail must never queue behind a batch: a password reset
// is sent while somebody waits on the page.
//
// Checked at the source rather than by timing. A timing test can't tell
// "not paced" from "paced with a fresh pacer each call" — a new Pacer
// never delays its first message, by design — so it would pass against
// the very change it is supposed to forbid. What actually matters is
// structural: the transactional entry points must not reach for a pacer
// at all.
func TestTransactionalEntryPointsDoNotPace(t *testing.T) {
	src, err := os.ReadFile("mailer.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "mailer.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Send and SendHTML are what password resets, event reminders and the
	// welcome email go through. SendHTMLPaced (pace.go) is the bulk one
	// and is deliberately not checked here.
	transactional := map[string]bool{"Send": true, "SendHTML": true, "deliver": true}

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !transactional[fn.Name.Name] {
			continue
		}
		body := string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
		for _, banned := range []string{"Pacer", "pacer", ".Wait("} {
			if strings.Contains(body, banned) {
				t.Errorf("%s mentions %q — transactional mail must not be paced, or a password reset "+
					"can queue behind a newsletter", fn.Name.Name, banned)
			}
		}
	}
}

// Sending three transactional messages back to back must not take the
// bulk interval. Weaker than the structural check above, but it covers
// the plumbing rather than the source text.
func TestTransactionalSendsAreFast(t *testing.T) {
	newCountingJMAPServer(t, "sender@example.com")
	cfg := testJMAPConfig()
	cfg.BulkPerMinute = 1 // a minute between bulk messages
	m := New(cfg, nil)

	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := m.Send(t.Context(), "a@example.com", "Reset your password", "link"); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("three transactional sends took %v — they are being paced with the bulk rate", elapsed)
	}
}

// An unset rate must still pace, so a site that never configures it is
// protected by default rather than by luck.
func TestBulkPacingIsOnByDefault(t *testing.T) {
	m := New(Config{Host: "smtp.example.com", From: "a@example.com"}, nil)
	if m.cfg.BulkPerMinute != defaultBulkPerMinute {
		t.Errorf("BulkPerMinute = %d, want the default %d", m.cfg.BulkPerMinute, defaultBulkPerMinute)
	}
	if p := m.BulkPacer(); p.interval <= 0 {
		t.Error("the default pacer does no pacing")
	}

	off := New(Config{Host: "smtp.example.com", From: "a@example.com", BulkPerMinute: -1}, nil)
	if p := off.BulkPacer(); p.interval > 0 {
		t.Error("a negative rate should disable pacing")
	}
}
