// Package csrf implements CSRF protection via the double-submit-cookie
// pattern: every visitor (logged in or not) gets a random, unguessable
// token in a cookie; every page render embeds that same value as a hidden
// form field; every state-changing (POST) request must submit a matching
// value. An attacker's cross-site form can make the browser send the
// cookie automatically, but has no way to read its value to also supply a
// matching csrf_token field — same-origin policy blocks that — so a forged
// submission fails the comparison.
//
// This is deliberately stateless (no server-side token table, no
// dependency on a logged-in session) specifically so it protects the
// pre-login forms (login, forgot-password) the same way it protects every
// admin action — those requests have no session cookie yet to tie a
// server-side token to.
//
// See SECURITY_AUDIT.md for why this was added: the app's prior CSRF
// posture (POST-only mutations + SameSite=Lax session cookies) was already
// a meaningful mitigation, but not a substitute for an actual token, since
// it depends entirely on correct browser behavior rather than the server
// verifying anything itself.
package csrf

import (
	"context"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/47-yonkers/scout-site/internal/auth"
)

// CookieName is the cookie carrying the CSRF token. It's separate from
// auth.SessionCookieName on purpose — this one exists (and is checked)
// whether or not the visitor is logged in.
const CookieName = "scoutsite_csrf"

// cookieLifetime is generous since a stale token just means one extra
// cookie-reissue on the visitor's next visit, not a security concern —
// unlike the session cookie, this token authorizes nothing by itself
// without a matching form submission from the same browser.
const cookieLifetime = 180 * 24 * time.Hour

// maxUploadMemory bounds how much of a multipart request body
// ParseMultipartForm buffers in memory (anything past this spills to a
// temp file on disk, which the net/http machinery cleans up).
const maxUploadMemory = 10 << 20 // 10 MB

// maxRequestBodySize caps every POST body this middleware parses,
// multipart file uploads included — applied here (before ParseMultipartForm
// ever reads the body) rather than per-route, since it's the one place
// every POST request already passes through. 25 MB comfortably fits a
// phone photo or a scanned form while still bounding how much a single
// request can make the server buffer/spill to disk.
const maxRequestBodySize = 25 << 20 // 25 MB

type contextKey string

const tokenContextKey contextKey = "csrf_token"

// TokenFromContext returns the CSRF token for the current request, for
// embedding in a rendered page's forms. Empty if Middleware hasn't run
// (which shouldn't happen in production — see cmd/server's middleware
// chain).
func TokenFromContext(ctx context.Context) string {
	token, _ := ctx.Value(tokenContextKey).(string)
	return token
}

// Middleware ensures every request carries a CSRF cookie (issuing one on
// first visit if missing) and rejects any POST request whose csrf_token
// form value doesn't match it. GET/HEAD requests are never rejected — they
// shouldn't have side effects, and this app's routes confirm that (every
// state-changing route is POST-only; see internal/web.Handlers.Routes).
func Middleware(secureCookie bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ""
			if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
				token = c.Value
			} else {
				fresh, genErr := auth.RandomToken(32)
				if genErr != nil {
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				token = fresh
				http.SetCookie(w, &http.Cookie{
					Name:     CookieName,
					Value:    token,
					Path:     "/",
					MaxAge:   int(cookieLifetime.Seconds()),
					HttpOnly: true, // the server both sets and reads this cookie — no JS needs to touch it
					Secure:   secureCookie,
					SameSite: http.SameSiteLaxMode,
				})
			}

			if r.Method == http.MethodPost {
				r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)

				// r.ParseForm() only parses application/x-www-form-urlencoded
				// bodies — for multipart/form-data (file uploads) it leaves
				// PostForm empty without erroring, which would make every
				// upload fail this check with "form session expired" no
				// matter how valid the token. ParseMultipartForm handles
				// both: it calls ParseForm internally for other content
				// types, and for multipart parses the non-file fields into
				// r.Form/r.PostForm too. maxUploadMemory bounds how much of
				// a multipart body is buffered in memory before spilling to
				// temp files on disk.
				if err := r.ParseMultipartForm(maxUploadMemory); err != nil && err != http.ErrNotMultipart {
					if err.Error() == "http: request body too large" {
						http.Error(w, "that file is too large (25 MB max)", http.StatusRequestEntityTooLarge)
						return
					}
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				submitted := r.FormValue("csrf_token")
				if submitted == "" || subtle.ConstantTimeCompare([]byte(submitted), []byte(token)) != 1 {
					http.Error(w, "your form session expired or is invalid — please reload the page and try again", http.StatusForbidden)
					return
				}
			}

			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenContextKey, token)))
		})
	}
}
