// Package csp issues a per-request nonce and builds the Content-Security-
// Policy header around it.
//
// Why a nonce rather than 'unsafe-inline': the point of a script-src is to
// say which scripts may run. 'unsafe-inline' says "any inline script may
// run", which is exactly what injected script is — so a policy containing
// it looks stricter than the old no-script-src policy while stopping
// almost nothing. A nonce is a fresh random value per response that the
// browser requires on every inline <script>; an attacker who manages to
// inject markup can't guess it, so their script doesn't execute.
//
// The cost is that nonces don't cover inline event handlers — onclick=,
// onsubmit= and friends are blocked outright, nonce or not. Those were
// converted to data attributes handled by one delegated listener in
// base.html, which is why this policy can be as tight as it is.
package csp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
)

type contextKey string

const nonceContextKey contextKey = "csp_nonce"

// NonceFromContext returns this request's script nonce, for templates to
// put on their inline <script> tags. Empty if the middleware didn't run —
// which fails safe: a script tag with an empty nonce attribute simply
// won't match the policy and won't execute, rather than silently being
// allowed.
func NonceFromContext(ctx context.Context) string {
	nonce, _ := ctx.Value(nonceContextKey).(string)
	return nonce
}

// scriptSources are the external origins allowed to serve JavaScript:
// Tailwind's play CDN, and cdnjs for htmx, Quill and QRious. Everything
// else has to be same-origin or carry the nonce.
var scriptSources = []string{
	"https://cdn.tailwindcss.com",
	"https://cdnjs.cloudflare.com",
}

// Policy returns the header value for a request carrying nonce.
//
// style-src keeps 'unsafe-inline', and that's deliberate rather than an
// oversight. Tailwind's play CDN generates CSS at runtime and injects
// <style> elements the server never sees, so it can't nonce them. Inline
// style is also a far weaker foothold than inline script — it can't call
// anything — so the trade is worth making to keep script-src strict.
func Policy(nonce string) string {
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'nonce-" + nonce + "' " + strings.Join(scriptSources, " "),
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com https://cdnjs.cloudflare.com",
		"font-src 'self' https://fonts.gstatic.com",
		// data: covers inline SVG/preview images; https: covers a hero or
		// leader photo pointed at an external URL, which the image picker
		// explicitly supports.
		"img-src 'self' data: https:",
		"connect-src 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
		"object-src 'none'",
		"base-uri 'self'",
	}, "; ")
}

// Middleware issues a nonce, puts it in the request context, and sets the
// policy. Must run before any handler that renders a template.
//
// Deliberately does NOT set the header for responses that carry
// user-uploaded bytes — those handlers override it with a stricter
// sandbox policy of their own (see internal/web.writeUserFileHeaders),
// and they run after this, so their Set wins.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			// Without a nonce, no inline script would run and every page
			// would be subtly broken. Failing the request outright is the
			// honest outcome, and this can't realistically happen.
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		nonce := base64.RawStdEncoding.EncodeToString(b)

		w.Header().Set("Content-Security-Policy", Policy(nonce))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nonceContextKey, nonce)))
	})
}
