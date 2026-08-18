package auth

// This file is the database-backed half of two-factor login — enrollment,
// confirmation, verification at login, and the short-lived "password
// checked out, TOTP code still needed" pending state. The actual TOTP
// algorithm (RFC 6238) lives in internal/twofactor as a small,
// dependency-free package this file calls into; see that package's doc
// comment for why it's split out and how it was verified against the
// official RFC 4226 test vectors.
//
// Enforcement of WHO needs two-factor (Treasurer/super_admin — see
// units.FamilyHasAnyTreasuryRole) lives in internal/web's login handler,
// not here — this file only answers "is 2FA set up and correct for this
// user," it doesn't decide who's required to have it.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/47-yonkers/scout-site/internal/twofactor"
)

// PendingTwoFactorDuration is how long a "password checked out, TOTP code
// still needed" pending login stays valid before it must restart from
// /login — long enough to type a 6-digit code, short enough that an
// abandoned half-login isn't a standing risk.
const PendingTwoFactorDuration = 5 * time.Minute

// NumBackupCodes is how many one-time recovery codes get generated at
// TOTP enrollment.
const NumBackupCodes = 10

var (
	// ErrTOTPNotEnrolled is returned by ConfirmTOTPEnrollment /
	// VerifyTOTPOrBackupCode when a user has no pending or confirmed TOTP
	// credential at all.
	ErrTOTPNotEnrolled = errors.New("auth: no two-factor enrollment in progress for this account")

	// ErrInvalidTOTPCode covers a wrong 6-digit code or a backup code that
	// doesn't match / was already used — deliberately not distinguished,
	// same "don't leak which one it was" reasoning as ErrInvalidCredentials.
	ErrInvalidTOTPCode = errors.New("auth: invalid or expired code")

	// ErrInvalidPendingLogin covers every reason a pending two-factor login
	// token can't be redeemed (unknown, expired) — not distinguished in the
	// message shown to the visitor, for the same reason as
	// ErrInvalidResetToken.
	ErrInvalidPendingLogin = errors.New("auth: your login session expired — please sign in again")
)

// TOTPStatus reports whether a user has ever started TOTP enrollment, and
// whether they've completed it (entered one valid code, so the app knows
// the secret was actually copied into their authenticator app correctly).
// An unconfirmed enrollment is never enforced at login — see
// FamilyHasAnyTreasuryRole's caller in internal/web, which only requires
// a code when confirmed is true.
func TOTPStatus(ctx context.Context, pool *pgxpool.Pool, userID string) (enrolled, confirmed bool, err error) {
	var confirmedAt *time.Time
	err = pool.QueryRow(ctx,
		`SELECT confirmed_at FROM totp_credentials WHERE user_id = $1`, userID,
	).Scan(&confirmedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, false, nil
		}
		return false, false, err
	}
	return true, confirmedAt != nil, nil
}

// PendingTOTPSecret returns the secret for a user's *unconfirmed* TOTP
// enrollment, if one is in progress — used to redisplay the setup key if
// someone reloads the enrollment page instead of losing it.
func PendingTOTPSecret(ctx context.Context, pool *pgxpool.Pool, userID string) (secret string, found bool, err error) {
	err = pool.QueryRow(ctx,
		`SELECT secret FROM totp_credentials WHERE user_id = $1 AND confirmed_at IS NULL`, userID,
	).Scan(&secret)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return secret, true, nil
}

// BeginTOTPEnrollment generates a new random TOTP secret for a user and
// stores it unconfirmed, replacing any previous unconfirmed (or
// confirmed — see the doc note below) secret. Returns the secret so the
// caller can show it (grouped via twofactor.FormatSecretForDisplay) and
// the otpauth:// URI for enrollment.
//
// Re-enrolling over an already-*confirmed* credential is allowed here
// deliberately kept simple for this pass: anyone with a valid, logged-in
// session for this account can start over (e.g. after losing their
// device). That session already required the account's password to
// create, which is the same bar every other sensitive action in this
// codebase (e.g. a leader resetting a family's password from
// /admin/roster) is held to — but unlike those, this one can silently
// turn a Treasurer/super_admin login's two-factor protection back off
// mid-enrollment if it's abandoned. Worth hardening later (e.g. requiring
// the current TOTP code or a fresh password entry to re-enroll) if this
// account tier ever needs a stronger bar than "has an active session."
func BeginTOTPEnrollment(ctx context.Context, pool *pgxpool.Pool, userID string) (secret string, err error) {
	secret, err = twofactor.GenerateSecret()
	if err != nil {
		return "", err
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO totp_credentials (user_id, secret, confirmed_at)
		VALUES ($1, $2, NULL)
		ON CONFLICT (user_id) DO UPDATE SET secret = $2, confirmed_at = NULL
	`, userID, secret)
	if err != nil {
		return "", err
	}
	return secret, nil
}

// ConfirmTOTPEnrollment checks a user-submitted code against their
// in-progress enrollment; on success, marks it confirmed and generates a
// fresh batch of backup codes (replacing any from a previous enrollment),
// returned once in plaintext — the caller must show these to the user
// immediately, since only their bcrypt hashes are kept afterward.
func ConfirmTOTPEnrollment(ctx context.Context, pool *pgxpool.Pool, userID, code string) (backupCodes []string, err error) {
	var secret string
	err = pool.QueryRow(ctx,
		`SELECT secret FROM totp_credentials WHERE user_id = $1`, userID,
	).Scan(&secret)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTOTPNotEnrolled
		}
		return nil, err
	}

	ok, err := twofactor.Verify(secret, code)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvalidTOTPCode
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if already committed

	if _, err := tx.Exec(ctx,
		`UPDATE totp_credentials SET confirmed_at = now() WHERE user_id = $1`, userID,
	); err != nil {
		return nil, err
	}

	// Replace any previous batch of backup codes — old ones from a prior
	// enrollment shouldn't keep working once someone re-enrolls.
	if _, err := tx.Exec(ctx, `DELETE FROM totp_backup_codes WHERE user_id = $1`, userID); err != nil {
		return nil, err
	}

	codes := make([]string, 0, NumBackupCodes)
	for i := 0; i < NumBackupCodes; i++ {
		plain, err := twofactor.GenerateBackupCode()
		if err != nil {
			return nil, err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO totp_backup_codes (user_id, code_hash) VALUES ($1, $2)`, userID, string(hash),
		); err != nil {
			return nil, err
		}
		codes = append(codes, plain)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return codes, nil
}

// DisableTOTP removes a user's TOTP credential and every backup code —
// available to a logged-in user who wants to turn two-factor back off
// (e.g. stepping down as Treasurer). If they still hold a TreasuryRoles
// role, internal/web's login flow will prompt them to set it up again
// the next time they sign in.
func DisableTOTP(ctx context.Context, pool *pgxpool.Pool, userID string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if already committed

	if _, err := tx.Exec(ctx, `DELETE FROM totp_credentials WHERE user_id = $1`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM totp_backup_codes WHERE user_id = $1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// VerifyTOTPOrBackupCode checks a user-submitted code at login time: a
// current TOTP code first, then (if that doesn't match) an unused backup
// code. A matching backup code is marked used and can never be redeemed
// again.
func VerifyTOTPOrBackupCode(ctx context.Context, pool *pgxpool.Pool, userID, code string) (bool, error) {
	var secret string
	var confirmedAt *time.Time
	err := pool.QueryRow(ctx,
		`SELECT secret, confirmed_at FROM totp_credentials WHERE user_id = $1`, userID,
	).Scan(&secret, &confirmedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrTOTPNotEnrolled
		}
		return false, err
	}
	if confirmedAt == nil {
		return false, ErrTOTPNotEnrolled
	}

	if ok, err := twofactor.Verify(secret, code); err != nil {
		return false, err
	} else if ok {
		return true, nil
	}

	// Not a valid TOTP code — try it as a backup code instead. Backup
	// codes are short and hyphenated (see twofactor.GenerateBackupCode);
	// a 6-digit TOTP guess will never accidentally match a bcrypt-hashed
	// backup code, so trying both in sequence is safe and simple.
	rows, err := pool.Query(ctx,
		`SELECT id, code_hash FROM totp_backup_codes WHERE user_id = $1 AND used_at IS NULL`, userID,
	)
	if err != nil {
		return false, err
	}
	type candidate struct{ id, hash string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.hash); err != nil {
			rows.Close()
			return false, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	for _, c := range candidates {
		if bcrypt.CompareHashAndPassword([]byte(c.hash), []byte(code)) == nil {
			if _, err := pool.Exec(ctx,
				`UPDATE totp_backup_codes SET used_at = now() WHERE id = $1`, c.id,
			); err != nil {
				return false, err
			}
			return true, nil
		}
	}

	return false, nil
}

// CreatePendingTwoFactorLogin records that a login has passed the
// password check and is waiting on a TOTP code, returning an opaque
// single-use token carried in its own short-lived cookie (see
// internal/web) — deliberately not the real session token, so a
// half-authenticated visitor never holds anything that grants access
// before the second factor succeeds.
func CreatePendingTwoFactorLogin(ctx context.Context, pool *pgxpool.Pool, userID, next string) (token string, expiresAt time.Time, err error) {
	token, err = RandomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt = time.Now().Add(PendingTwoFactorDuration)

	_, err = pool.Exec(ctx,
		`INSERT INTO pending_two_factor_logins (token, user_id, next, expires_at) VALUES ($1, $2, $3, $4)`,
		token, userID, next, expiresAt,
	)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// maxPendingTwoFactorAttempts caps how many wrong codes one pending login
// (one cookie) can try before it's discarded and the visitor has to sign
// in from scratch. A TOTP code is only 6 digits (1-in-a-million per
// guess, 1-in-~333,333 counting the ±30s window this app accepts) — a
// short attempt cap plus the 5-minute overall expiry is what keeps that
// from being brute-forceable within one pending login's lifetime.
const maxPendingTwoFactorAttempts = 5

// PendingTwoFactorLoginExists reports whether a pending-login token is
// still valid (exists, unexpired) without consuming an attempt or
// deleting it — used to render /login/2fa's form (and to bounce a visitor
// back to /login if their pending-login cookie is missing or stale)
// without that page-load itself counting as a guess.
func PendingTwoFactorLoginExists(ctx context.Context, pool *pgxpool.Pool, token string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pending_two_factor_logins WHERE token = $1 AND expires_at > now())`,
		token,
	).Scan(&exists)
	return exists, err
}

// VerifyPendingTwoFactorLogin checks a submitted code against the pending
// login a token identifies. On a correct code, the pending row is deleted
// and (userID, next) is returned so the caller can create the real
// session. On a wrong code, the attempt is counted against
// maxPendingTwoFactorAttempts; exceeding it deletes the pending row
// outright (forcing a fresh /login) rather than leaving it guessable
// indefinitely within its expiry window.
func VerifyPendingTwoFactorLogin(ctx context.Context, pool *pgxpool.Pool, token, code string) (userID, next string, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if already committed

	var attempts int
	err = tx.QueryRow(ctx, `
		SELECT user_id, next, attempts FROM pending_two_factor_logins
		WHERE token = $1 AND expires_at > now()
		FOR UPDATE
	`, token).Scan(&userID, &next, &attempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", ErrInvalidPendingLogin
		}
		return "", "", err
	}

	if attempts >= maxPendingTwoFactorAttempts {
		if _, err := tx.Exec(ctx, `DELETE FROM pending_two_factor_logins WHERE token = $1`, token); err != nil {
			return "", "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", "", err
		}
		return "", "", ErrInvalidPendingLogin
	}

	// The actual TOTP/backup-code check happens against `pool`, not `tx` —
	// VerifyTOTPOrBackupCode may write a backup-code's used_at, which
	// should stick even though this function's own transaction is about
	// to either commit a small counter update or a delete; there's no
	// correctness reason those two writes need to be atomic with each
	// other.
	ok, verr := VerifyTOTPOrBackupCode(ctx, pool, userID, code)
	if verr != nil {
		return "", "", verr
	}

	if !ok {
		if _, err := tx.Exec(ctx,
			`UPDATE pending_two_factor_logins SET attempts = attempts + 1 WHERE token = $1`, token,
		); err != nil {
			return "", "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", "", err
		}
		return "", "", ErrInvalidTOTPCode
	}

	if _, err := tx.Exec(ctx, `DELETE FROM pending_two_factor_logins WHERE token = $1`, token); err != nil {
		return "", "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return userID, next, nil
}
