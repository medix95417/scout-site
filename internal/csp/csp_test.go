package csp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPolicy_ScriptSrcIsNonceBasedNotUnsafeInline is the point of the
// whole exercise. A script-src containing 'unsafe-inline' permits any
// inline script — which is exactly what injected script is — so it would
// read as protection while providing almost none.
func TestPolicy_ScriptSrcIsNonceBasedNotUnsafeInline(t *testing.T) {
	p := Policy("abc123")

	var scriptSrc string
	for _, directive := range strings.Split(p, "; ") {
		if strings.HasPrefix(directive, "script-src ") {
			scriptSrc = directive
		}
	}
	if scriptSrc == "" {
		t.Fatalf("no script-src in policy: %s", p)
	}
	if !strings.Contains(scriptSrc, "'nonce-abc123'") {
		t.Errorf("script-src should carry the request's nonce, got: %s", scriptSrc)
	}
	if strings.Contains(scriptSrc, "'unsafe-inline'") {
		t.Errorf("script-src must not allow unsafe-inline — that defeats the nonce: %s", scriptSrc)
	}
	if strings.Contains(scriptSrc, "'unsafe-eval'") {
		t.Errorf("script-src must not allow unsafe-eval: %s", scriptSrc)
	}
}

// TestPolicy_KeepsTheDirectivesThatWereAlreadyThere — the previous policy
// locked down framing, plugins and <base>. Adding script-src must not
// quietly drop them.
func TestPolicy_KeepsTheDirectivesThatWereAlreadyThere(t *testing.T) {
	p := Policy("n")
	for _, want := range []string{
		"frame-ancestors 'none'",
		"object-src 'none'",
		"base-uri 'self'",
		"default-src 'self'",
		"form-action 'self'",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("policy is missing %q: %s", want, p)
		}
	}
}

// TestPolicy_AllowsTheCDNsTheSiteActuallyUses — htmx, Tailwind, Quill and
// QRious are loaded from these two origins. A policy that blocks them
// breaks the site, which is worse than no policy at all.
func TestPolicy_AllowsTheCDNsTheSiteActuallyUses(t *testing.T) {
	p := Policy("n")
	for _, want := range []string{"https://cdn.tailwindcss.com", "https://cdnjs.cloudflare.com"} {
		if !strings.Contains(p, want) {
			t.Errorf("policy blocks %s, which the templates load scripts from: %s", want, p)
		}
	}
}

// TestMiddleware_IssuesAFreshUnguessableNoncePerRequest — a nonce reused
// across responses is a nonce an attacker can look up in a previous page
// and reuse.
func TestMiddleware_IssuesAFreshUnguessableNoncePerRequest(t *testing.T) {
	seen := map[string]bool{}
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := NonceFromContext(r.Context())
		if n == "" {
			t.Error("handler saw an empty nonce")
		}
		if seen[n] {
			t.Errorf("nonce %q was reused across requests", n)
		}
		seen[n] = true
		if got := w.Header().Get("Content-Security-Policy"); !strings.Contains(got, "'nonce-"+n+"'") {
			t.Errorf("header nonce doesn't match the one handlers see:\n header: %s\n context: %s", got, n)
		}
	}))

	for i := 0; i < 100; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	}
	if len(seen) != 100 {
		t.Errorf("got %d distinct nonces across 100 requests", len(seen))
	}
}

// TestNonceFromContext_EmptyWithoutMiddleware — failing closed. A template
// that renders nonce="" produces a script the browser refuses to run,
// which is a visible break rather than a silent security hole.
func TestNonceFromContext_EmptyWithoutMiddleware(t *testing.T) {
	if got := NonceFromContext(httptest.NewRequest("GET", "/", nil).Context()); got != "" {
		t.Errorf("NonceFromContext without the middleware = %q, want empty", got)
	}
}
