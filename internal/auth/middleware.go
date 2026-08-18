package auth

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

type contextKey string

const userContextKey contextKey = "auth_user"

// WithUser is middleware that looks up the session cookie (if present) and,
// if valid, attaches the User to the request context. It never blocks the
// request — pages that require login use RequireLogin downstream of this.
func WithUser(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(SessionCookieName)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}

			user, ok, err := UserForSession(r.Context(), pool, cookie.Value)
			if err != nil || !ok {
				next.ServeHTTP(w, r)
				return
			}

			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserFromContext returns the logged-in user for this request, if any.
func UserFromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userContextKey).(User)
	return u, ok
}

// RequireLogin is middleware for routes that must not be reachable by
// unauthenticated visitors (roster, calendar management, etc.). Must be
// mounted downstream of WithUser.
func RequireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFromContext(r.Context()); !ok {
			http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}
