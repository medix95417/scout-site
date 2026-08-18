// Package auth handles login, password hashing, and session cookies.
//
// Sessions are opaque random tokens stored server-side in Postgres (not
// signed JWTs) — simpler to revoke (delete the row) and there's no risk of
// a stale claim in a token outliving a permission change, which matters
// once youth-submitted content and approvals are involved.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const (
	// SessionCookieName is the cookie the browser carries the session token in.
	SessionCookieName = "scoutsite_session"

	// PendingTwoFactorCookieName is the cookie carrying a
	// pending-two-factor-login token (see totp.go) — deliberately a
	// separate cookie from SessionCookieName, never containing anything a
	// half-authenticated visitor could use to reach a real session before
	// their TOTP code is verified.
	PendingTwoFactorCookieName = "scoutsite_pending_2fa"

	// SessionDuration is how long a session stays valid after login.
	SessionDuration = 30 * 24 * time.Hour

	// ResetTokenDuration is how long a password reset link stays valid
	// before it must be requested again — long enough to find the email
	// and click it, short enough that an old, unused link sitting in an
	// inbox isn't a standing risk.
	ResetTokenDuration = 1 * time.Hour

	// MaxLoginFailures is how many failed login attempts a single email
	// address can rack up within LoginLockoutWindow before further
	// attempts are rejected outright — without even querying for the user
	// or running a bcrypt comparison. Applied per normalized email address,
	// not per IP: this app has no reverse proxy header it can trust for a
	// real client IP, and an account-level lock is what actually stops a
	// credential-guessing attack against one account regardless of how
	// many source addresses it comes from.
	MaxLoginFailures = 8

	// LoginLockoutWindow is both (a) how long a burst of failures stays
	// "live" for counting purposes — a failure older than this doesn't
	// count toward the next lockout — and (b) how long a triggered
	// lockout lasts before attempts are allowed again.
	LoginLockoutWindow = 15 * time.Minute

	// MaxPasswordResetRequests is how many "forgot password" submissions a
	// single email address can trigger an actual email for within
	// PasswordResetRequestWindow. Requests beyond the cap still show the
	// visitor the exact same "if that email has an account..." response —
	// nothing about the response ever changes — they just silently stop
	// sending more email. This exists to stop one account (adult or, since
	// this site has youth logins too, a Scout's) from having its inbox
	// flooded by repeated reset requests, not to protect against guessing
	// anything, so a generous-but-bounded cap is the right shape here.
	MaxPasswordResetRequests = 3

	// PasswordResetRequestWindow is the rolling window
	// MaxPasswordResetRequests applies over. Matches ResetTokenDuration
	// deliberately — by the time the window resets, any token from the
	// start of the previous burst has expired anyway.
	PasswordResetRequestWindow = 1 * time.Hour
)

var ErrInvalidCredentials = errors.New("auth: invalid email or password")

// ErrAccountLocked is returned by Authenticate when an email address has
// failed to log in MaxLoginFailures times within LoginLockoutWindow.
// Returned uniformly whether or not the email has a real account — see
// RecordLoginFailure — so lockout behavior itself can't be used to test
// which emails are registered.
var ErrAccountLocked = errors.New("auth: too many failed login attempts — please wait a few minutes and try again")

// ErrInvalidResetToken covers every reason a reset link can't be redeemed —
// unknown, expired, or already used — deliberately not distinguished in
// the message shown to the person clicking it, same "don't leak which one
// it was" reasoning as ErrInvalidCredentials.
var ErrInvalidResetToken = errors.New("auth: invalid or expired reset link")

// NormalizeEmail lowercases and trims an email address so lookups and the
// unique constraint on users.email are consistent regardless of how a user
// typed it (see internal/db/migrations/0001_init.sql for why this replaces
// relying on the citext extension).
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// HashPassword bcrypt-hashes a plaintext password for storage.
func HashPassword(plaintext string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// VerifyPassword checks a plaintext password against a user's stored
// bcrypt hash — the step-up check for sensitive in-session actions (e.g.
// re-enrolling two-factor over an already-confirmed credential, see
// BeginTOTPEnrollment) where "has a still-valid session" alone isn't
// treated as a strong enough bar.
func VerifyPassword(u User, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(plaintext)) == nil
}

// User is the minimal shape auth needs; the family package owns the fuller
// record. Kept separate so this package doesn't import family (avoids an
// import cycle now that family will need auth for password verification
// during account creation).
//
// MemberID is nil for an ordinary family-wide login (the original,
// still-default shape: one shared login per family). It's set only for a
// login created for one specific member (e.g. an individual Scout login —
// see internal/roster.CreateMemberLogin) — callers that need to resolve
// permissions or an "acting member" for the current request should check
// MemberID first and, when set, scope strictly to that one member rather
// than falling back to family-wide resolution (see internal/web's
// rolesFor/actingMember helpers).
type User struct {
	ID           string
	FamilyID     string
	MemberID     *string
	Email        string
	PasswordHash string
}

// Authenticate checks a plaintext password against a stored user, returning
// ErrInvalidCredentials (deliberately generic, not "wrong password" vs.
// "no such user" — don't leak which one it was) on any mismatch, or
// ErrAccountLocked if this email has racked up too many recent failures
// (see MaxLoginFailures / LoginLockoutWindow). The lockout check happens
// before the user lookup and before the bcrypt comparison, so a locked-out
// attempt doesn't pay for (or leak any timing from) either.
func Authenticate(ctx context.Context, pool *pgxpool.Pool, email, password string) (User, error) {
	normalized := NormalizeEmail(email)

	locked, err := CheckLockout(ctx, pool, normalized)
	if err != nil {
		return User{}, err
	}
	if locked {
		return User{}, ErrAccountLocked
	}

	var u User
	err = pool.QueryRow(ctx,
		`SELECT id, family_id, member_id, email, password_hash FROM users WHERE email = $1`,
		normalized,
	).Scan(&u.ID, &u.FamilyID, &u.MemberID, &u.Email, &u.PasswordHash)
	if err != nil {
		recordLoginFailureBestEffort(ctx, pool, normalized)
		return User{}, ErrInvalidCredentials
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		recordLoginFailureBestEffort(ctx, pool, normalized)
		return User{}, ErrInvalidCredentials
	}

	// Successful login — this account is in good standing again, so any
	// earlier failed attempts shouldn't count against it going forward.
	if err := ClearLoginFailures(ctx, pool, normalized); err != nil {
		log.Printf("auth: clearing login failures for %s: %v", normalized, err)
	}

	return u, nil
}

// recordLoginFailureBestEffort records a failed attempt and logs (rather
// than propagates) any error — a database hiccup while recording a
// failure shouldn't turn into an unrelated 500 for someone who just typed
// their password wrong.
func recordLoginFailureBestEffort(ctx context.Context, pool *pgxpool.Pool, normalizedEmail string) {
	if err := RecordLoginFailure(ctx, pool, normalizedEmail); err != nil {
		log.Printf("auth: recording login failure for %s: %v", normalizedEmail, err)
	}
}

// CheckLockout reports whether email is currently locked out from further
// login attempts due to MaxLoginFailures-or-more failures within the last
// LoginLockoutWindow.
func CheckLockout(ctx context.Context, pool *pgxpool.Pool, email string) (locked bool, err error) {
	normalized := NormalizeEmail(email)

	var failureCount int
	var lastFailureAt time.Time
	err = pool.QueryRow(ctx,
		`SELECT failure_count, last_failure_at FROM login_failures WHERE email = $1`,
		normalized,
	).Scan(&failureCount, &lastFailureAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	if time.Since(lastFailureAt) > LoginLockoutWindow {
		// The burst that tripped (or was building toward) a lockout is
		// stale — treat it as not locked out. RecordLoginFailure resets
		// the counter the next time this email actually fails again.
		return false, nil
	}

	return failureCount >= MaxLoginFailures, nil
}

// RecordLoginFailure increments the failure counter for an email address,
// resetting the count first if the previous failure fell outside
// LoginLockoutWindow (a cold trail of old failures shouldn't count against
// a fresh attempt). Called on every failed login attempt — including ones
// against an email with no account at all — precisely so lockout timing
// can't be used to distinguish "wrong password" from "no such user".
func RecordLoginFailure(ctx context.Context, pool *pgxpool.Pool, email string) error {
	normalized := NormalizeEmail(email)
	cutoff := time.Now().Add(-LoginLockoutWindow)

	_, err := pool.Exec(ctx, `
		INSERT INTO login_failures (email, failure_count, first_failure_at, last_failure_at)
		VALUES ($1, 1, now(), now())
		ON CONFLICT (email) DO UPDATE SET
			failure_count = CASE
				WHEN login_failures.last_failure_at < $2 THEN 1
				ELSE login_failures.failure_count + 1
			END,
			first_failure_at = CASE
				WHEN login_failures.last_failure_at < $2 THEN now()
				ELSE login_failures.first_failure_at
			END,
			last_failure_at = now()
	`, normalized, cutoff)
	return err
}

// ClearLoginFailures deletes the failure-tracking row for an email address —
// called after a successful login so a legitimate sign-in resets the
// account's standing immediately, rather than making someone who mistyped
// a password a few times wait out the full window even after getting it
// right.
func ClearLoginFailures(ctx context.Context, pool *pgxpool.Pool, email string) error {
	_, err := pool.Exec(ctx, `DELETE FROM login_failures WHERE email = $1`, NormalizeEmail(email))
	return err
}

// RecordAndCheckPasswordResetRequest atomically records a "forgot password"
// submission for an email address and reports whether it's still within
// the allowed rate (MaxPasswordResetRequests per PasswordResetRequestWindow).
//
// Like RecordLoginFailure, this always records the attempt — including for
// an email with no account at all — and the caller must show the exact
// same response either way (see ForgotPasswordSubmit in internal/web),
// same "don't leak which one it was" reasoning UserByEmail already
// documents. The only thing an "not allowed" result changes is whether an
// email actually gets sent; nothing about the HTTP response differs. That's
// what makes this safe to use as inbox-flood protection (including for a
// Scout's own login, not just an adult's) without turning the rate-limit
// state itself into a way to test which emails are registered.
//
// A failure to even record the attempt (e.g. a database hiccup) is
// returned as an error so the caller can fail open — a tracking problem
// on this table should never be the reason a real password reset doesn't
// go out.
func RecordAndCheckPasswordResetRequest(ctx context.Context, pool *pgxpool.Pool, email string) (allowed bool, err error) {
	normalized := NormalizeEmail(email)
	cutoff := time.Now().Add(-PasswordResetRequestWindow)

	var count int
	err = pool.QueryRow(ctx, `
		INSERT INTO password_reset_requests (email, request_count, first_request_at, last_request_at)
		VALUES ($1, 1, now(), now())
		ON CONFLICT (email) DO UPDATE SET
			request_count = CASE
				WHEN password_reset_requests.last_request_at < $2 THEN 1
				ELSE password_reset_requests.request_count + 1
			END,
			first_request_at = CASE
				WHEN password_reset_requests.last_request_at < $2 THEN now()
				ELSE password_reset_requests.first_request_at
			END,
			last_request_at = now()
		RETURNING request_count
	`, normalized, cutoff).Scan(&count)
	if err != nil {
		return false, err
	}

	return count <= MaxPasswordResetRequests, nil
}

// UserByEmail looks up a user by email for the "forgot password" flow.
// Returns (User{}, false, nil) if there's no account with that email — an
// expected outcome the caller must NOT reveal to the person submitting the
// form (see the handler in internal/web), to avoid letting the forgot-
// password form be used to test which emails have accounts.
func UserByEmail(ctx context.Context, pool *pgxpool.Pool, email string) (User, bool, error) {
	var u User
	err := pool.QueryRow(ctx,
		`SELECT id, family_id, member_id, email, password_hash FROM users WHERE email = $1`,
		NormalizeEmail(email),
	).Scan(&u.ID, &u.FamilyID, &u.MemberID, &u.Email, &u.PasswordHash)
	if err != nil {
		return User{}, false, nil //nolint:nilerr // "no such user" is a normal, expected outcome
	}
	return u, true, nil
}

// CreateResetToken issues a new password-reset token for a user. Any
// previously-issued, still-unused tokens for the same user are left in
// place — they'll simply fail to authorize anything useful once this one
// is used (each redemption is single-use), and letting them expire
// naturally is simpler than hunting them down to invalidate early.
func CreateResetToken(ctx context.Context, pool *pgxpool.Pool, userID string) (token string, expiresAt time.Time, err error) {
	token, err = RandomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt = time.Now().Add(ResetTokenDuration)

	_, err = pool.Exec(ctx,
		`INSERT INTO password_reset_tokens (token, user_id, expires_at) VALUES ($1, $2, $3)`,
		token, userID, expiresAt,
	)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// ConsumeResetToken redeems a password-reset token: if it exists, hasn't
// expired, and hasn't already been used, it sets the new password hash and
// marks the token used, atomically. Returns ErrInvalidResetToken for every
// failure mode (unknown/expired/already-used token) — see that error's
// comment for why they're not distinguished.
func ConsumeResetToken(ctx context.Context, pool *pgxpool.Pool, token, newPasswordHash string) (userID string, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if already committed

	err = tx.QueryRow(ctx, `
		SELECT user_id FROM password_reset_tokens
		WHERE token = $1 AND used_at IS NULL AND expires_at > now()
		FOR UPDATE
	`, token).Scan(&userID)
	if err != nil {
		return "", ErrInvalidResetToken
	}

	if _, err := tx.Exec(ctx,
		`UPDATE password_reset_tokens SET used_at = now() WHERE token = $1`, token,
	); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users SET password_hash = $1 WHERE id = $2`, newPasswordHash, userID,
	); err != nil {
		return "", err
	}

	// Sign out every device this account was already logged into — a
	// session cookie that leaked or was left on a shared/stolen device
	// shouldn't survive the very password reset meant to shut it out.
	if err := DestroySessionsForUserTx(ctx, tx, userID); err != nil {
		return "", err
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return userID, nil
}

// CreateSession issues a new session token for a user and persists it.
func CreateSession(ctx context.Context, pool *pgxpool.Pool, userID string) (token string, expiresAt time.Time, err error) {
	token, err = RandomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt = time.Now().Add(SessionDuration)

	_, err = pool.Exec(ctx,
		`INSERT INTO sessions (token, user_id, expires_at) VALUES ($1, $2, $3)`,
		token, userID, expiresAt,
	)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// DestroySession deletes a session (used on logout).
func DestroySession(ctx context.Context, pool *pgxpool.Pool, token string) error {
	_, err := pool.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, token)
	return err
}

// DestroySessionsForUserTx deletes every active session for a single user,
// as part of an existing transaction — called from ConsumeResetToken's
// password-change transaction so a session that leaked or was left signed
// in on a shared/stolen device doesn't survive the very password reset
// meant to shut it out.
func DestroySessionsForUserTx(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	return err
}

// DestroySessionsForFamily deletes every active session for a family's
// shared, family-wide login — used by a leader-triggered password reset
// (internal/roster.ResetFamilyPassword). Deliberately scoped to
// member_id IS NULL: a family can now also have individual member logins
// (see User.MemberID) that ResetFamilyPassword doesn't touch, so
// resetting the family-wide login shouldn't sign those out too — see
// DestroySessionsForMember for resetting one specific member's own
// login.
func DestroySessionsForFamily(ctx context.Context, pool *pgxpool.Pool, familyID string) error {
	_, err := pool.Exec(ctx, `
		DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE family_id = $1 AND member_id IS NULL)
	`, familyID)
	return err
}

// DestroySessionsForMember deletes every active session for one specific
// member's individual login — the member-scoped sibling of
// DestroySessionsForFamily, used by
// internal/roster.ResetMemberLoginPassword.
func DestroySessionsForMember(ctx context.Context, pool *pgxpool.Pool, memberID string) error {
	_, err := pool.Exec(ctx, `
		DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE member_id = $1)
	`, memberID)
	return err
}

// UserForSession looks up the user a valid, unexpired session token belongs
// to. Returns (User{}, false, nil) if the token is missing/expired/unknown —
// that's an expected outcome (logged-out visitor), not an error.
func UserForSession(ctx context.Context, pool *pgxpool.Pool, token string) (User, bool, error) {
	var u User
	err := pool.QueryRow(ctx, `
		SELECT users.id, users.family_id, users.member_id, users.email, users.password_hash
		FROM sessions
		JOIN users ON users.id = sessions.user_id
		WHERE sessions.token = $1 AND sessions.expires_at > now()
	`, token).Scan(&u.ID, &u.FamilyID, &u.MemberID, &u.Email, &u.PasswordHash)

	if err != nil {
		return User{}, false, nil //nolint:nilerr // "not found" is not an error case here
	}
	return u, true, nil
}

// SetSessionCookie writes the session cookie, scoped to cookieDomain so it's
// shared across every subdomain under the parent domain (e.g.
// ".47-yonkers.org" makes the cookie valid on both troop.47-yonkers.org and
// pack.47-yonkers.org — this is what makes single sign-on work with zero
// extra infrastructure). Leave cookieDomain empty for local development
// against a single host like localhost.
func SetSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time, cookieDomain string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Domain:   cookieDomain,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie expires the cookie immediately (used on logout).
func ClearSessionCookie(w http.ResponseWriter, cookieDomain string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Domain:   cookieDomain,
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// SetPendingTwoFactorCookie writes the short-lived pending-2FA-login
// cookie — same shape as SetSessionCookie, but the caller is expected to
// pass an expiry a few minutes out (PendingTwoFactorDuration), not
// SessionDuration.
func SetPendingTwoFactorCookie(w http.ResponseWriter, token string, expiresAt time.Time, cookieDomain string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     PendingTwoFactorCookieName,
		Value:    token,
		Domain:   cookieDomain,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearPendingTwoFactorCookie expires the pending-2FA-login cookie
// immediately — called once a code is verified (or rejected past the
// attempt cap) so a stale pending-login cookie doesn't linger in the
// browser after it's no longer usable.
func ClearPendingTwoFactorCookie(w http.ResponseWriter, cookieDomain string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     PendingTwoFactorCookieName,
		Value:    "",
		Domain:   cookieDomain,
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// RandomToken returns a cryptographically random, URL-safe token —
// numBytes of entropy from crypto/rand, base64-encoded. Exported so other
// packages that need the same "unguessable opaque value" property (e.g.
// internal/csrf's CSRF cookie) don't need their own copy of this logic.
func RandomToken(numBytes int) (string, error) {
	b := make([]byte, numBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// GenerateTemporaryPassword returns a random, reasonably strong password for
// a leader-created family login — shown once at creation time so a leader
// can hand it to the family. There's no self-service "forgot password" flow
// yet, so this is also what backs a leader-triggered password reset from
// /admin/roster if a family gets locked out.
func GenerateTemporaryPassword() (string, error) {
	return RandomToken(9) // ~12 URL-safe chars — plenty of entropy for a one-time value meant to be changed
}
