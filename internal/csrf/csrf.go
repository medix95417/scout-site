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
	"errors"
	"mime"
	"net/http"
	"strings"
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
// every POST request already passes through. This is the TOTAL size of a
// request, not any one file — a multi-file upload (see internal/web/files.go's
// FileUpload, which accepts a batch via <input multiple>) needs enough
// headroom for several files at once, e.g. a phone's whole camera roll for
// a campout, not just one. 250 MB comfortably fits a few dozen photos
// (each individually capped at 50 MB — see files.go's maxUploadFileSize)
// while still bounding how much one request can make the server buffer/
// spill to disk.
//
// This number is quoted verbatim in the too-large error below and is what
// http.Server's ReadTimeout has to be able to accommodate (see the timeout
// comment in cmd/server/main.go): a cap the server hangs up before anyone
// can reach is not really a cap. Change all three together or they drift
// — the constant said 500 MB while the message said 250 MB for a while,
// so whichever a leader believed, one of them was lying.
const maxRequestBodySize = 250 << 20 // 250 MB

// maxAnonymousRequestBodySize is the cap for a POST arriving without a
// session.
//
// Parsing happens before the CSRF token can be checked — for a multipart
// body the token is a field inside the body, so there is no reading one
// without the other — which means an anonymous request with a junk token
// still costs a full parse, buffering to memory and spilling to disk,
// before it is refused. At the upload cap that is a quarter of a gigabyte
// of disk per request, repeatable and concurrent, from anyone at all: a
// cheap way to fill the disk of a 1 GB VPS.
//
// Nothing legitimate needs the big cap without a session. Every upload
// path on this site is behind a login, so a signed-out POST is a login
// attempt, a password-reset request, or a join enquiry — all of them a
// few kilobytes of form fields.
const maxAnonymousRequestBodySize = 1 << 20 // 1 MB

// isMultipart reports whether this request carries a file upload, read
// from the Content-Type rather than discovered by trying to parse one.
func isMultipart(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	return err == nil && strings.HasPrefix(mediaType, "multipart/")
}

// tooLargeMessage explains the limit that was actually applied. The two
// limits differ (see maxAnonymousRequestBodySize), and telling a
// signed-out visitor they exceeded 250 MB when they were stopped at 1 MB
// would send them hunting for a problem that isn't there.
func tooLargeMessage(limit int64) string {
	if limit < maxRequestBodySize {
		return "that submission is too large. Please sign in first — uploads are only accepted from a signed-in account, and signed-out forms are limited to 1 MB."
	}
	return "that upload is too large — 250 MB total per submission (each file is also capped individually; try uploading in smaller batches)"
}

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
// Middleware issues and checks the CSRF token.
//
// authenticated reports whether the request carries a valid session. It is
// passed in rather than read directly so this package stays independent of
// internal/auth; it decides only how large a body this request is allowed
// to make the server buffer (see maxAnonymousRequestBodySize). A nil
// predicate is treated as "never authenticated", which fails safe.
//
// This middleware must run AFTER the one that attaches the user, or the
// predicate can never return true and every upload is capped at the
// anonymous limit. cmd/server/main.go wires that order deliberately.
func Middleware(secureCookie bool, authenticated func(*http.Request) bool) func(http.Handler) http.Handler {
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
				limit := int64(maxAnonymousRequestBodySize)
				if authenticated != nil && authenticated(r) {
					limit = maxRequestBodySize
				}
				r.Body = http.MaxBytesReader(w, r.Body, limit)

				// Parse the body so the token inside it can be read.
				//
				// The two content types are handled separately rather than
				// leaning on ParseMultipartForm to cover both. It does call
				// ParseForm internally, but it DISCARDS ParseForm's error
				// and returns ErrNotMultipart for a urlencoded body — so an
				// over-limit form arrived here as "not multipart", fell
				// through with nothing parsed, and was reported as an
				// expired form session. The size limit still bit (nothing
				// was buffered), but the person was told the wrong thing.
				//
				// maxUploadMemory bounds how much of a multipart body is
				// held in memory before spilling to temp files on disk.
				var parseErr error
				if isMultipart(r) {
					parseErr = r.ParseMultipartForm(maxUploadMemory)
				} else {
					parseErr = r.ParseForm()
				}
				if parseErr != nil {
					var tooLarge *http.MaxBytesError
					if errors.As(parseErr, &tooLarge) || parseErr.Error() == "http: request body too large" {
						http.Error(w, tooLargeMessage(limit), http.StatusRequestEntityTooLarge)
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
