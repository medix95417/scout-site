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
	// VerifySecondFactorCode when a user has no second factor at all —
	// no confirmed TOTP credential and no security key.
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
// confirmed — see the step-up check below) secret. Returns the secret so
// the caller can show it (grouped via twofactor.FormatSecretForDisplay)
// and the otpauth:// URI for enrollment.
//
// Enrolling on a login that already has a second factor — a confirmed
// app being replaced, or a security key — requires currentPassword to
// verify against the account's stored hash first (step-up
// authentication) — without this, anyone who merely holds a valid,
// logged-in session (e.g. a stolen session cookie, or a shared/unlocked
// device) could silently add or swap out a Treasurer/super_admin login's
// second factor, defeating the entire point of requiring one. Pass an
// empty currentPassword for a first enrollment (nothing confirmed yet to
// protect, so nothing to step up past).
func BeginTOTPEnrollment(ctx context.Context, pool *pgxpool.Pool, user User, currentPassword string) (secret string, err error) {
	has, err := HasSecondFactor(ctx, pool, user.ID)
	if err != nil {
		return "", err
	}
	if has && !VerifyPassword(user, currentPassword) {
		return "", ErrInvalidCredentials
	}

	secret, err = twofactor.GenerateSecret()
	if err != nil {
		return "", err
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO totp_credentials (user_id, secret, confirmed_at)
		VALUES ($1, $2, NULL)
		ON CONFLICT (user_id) DO UPDATE SET secret = $2, confirmed_at = NULL
	`, user.ID, secret)
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

	// Backup codes belong to the account, not to the app. Confirming an
	// app on a login that already has a security key leaves the batch
	// that key issued alone — replacing it would silently invalidate
	// codes the person wrote down. A re-enrollment of the app on a login
	// with no key still gets a fresh batch, as it always did.
	var codes []string
	var hasKey bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM security_keys WHERE user_id = $1)`, userID).Scan(&hasKey); err != nil {
		return nil, err
	}
	if !hasKey {
		if codes, err = issueBackupCodesTx(ctx, tx, userID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return codes, nil
}

// DisableTOTP removes a user's TOTP credential — available to a logged-in
// user who wants to stop using the app (e.g. stepping down as Treasurer).
// If they still hold a treasury role, internal/web's login flow will
// prompt them to set something up again the next time they sign in.
//
// The backup codes go only if nothing is left: a login that still has a
// security key still has a second step, and the codes are what stands in
// for the key when it is not to hand.
func DisableTOTP(ctx context.Context, pool *pgxpool.Pool, userID string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if already committed

	if _, err := tx.Exec(ctx, `DELETE FROM totp_credentials WHERE user_id = $1`, userID); err != nil {
		return err
	}
	still, err := hasSecondFactorTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	if !still {
		if _, err := tx.Exec(ctx, `DELETE FROM totp_backup_codes WHERE user_id = $1`, userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// VerifySecondFactorCode checks a user-submitted code at login time: a
// current TOTP code first, if an authenticator app is confirmed, then (if
// that doesn't match, or there is no app) an unused backup code. A
// matching backup code is marked used and can never be redeemed again.
//
// A login with a security key and no app reaches this with a backup code
// when the key is not to hand, so "no confirmed app" is not by itself a
// refusal — only "no second factor of any kind" is.
func VerifySecondFactorCode(ctx context.Context, pool *pgxpool.Pool, userID, code string) (bool, error) {
	var secret string
	var confirmedAt *time.Time
	err := pool.QueryRow(ctx,
		`SELECT secret, confirmed_at FROM totp_credentials WHERE user_id = $1`, userID,
	).Scan(&secret, &confirmedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	appConfirmed := err == nil && confirmedAt != nil

	if appConfirmed {
		if ok, err := twofactor.Verify(secret, code); err != nil {
			return false, err
		} else if ok {
			return true, nil
		}
	} else {
		_, keys, err := SecondFactorStatus(ctx, pool, userID)
		if err != nil {
			return false, err
		}
		if keys == 0 {
			return false, ErrTOTPNotEnrolled
		}
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
		hashToken(token), userID, next, expiresAt,
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
		hashToken(token),
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
	`, hashToken(token)).Scan(&userID, &next, &attempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", ErrInvalidPendingLogin
		}
		return "", "", err
	}

	if attempts >= maxPendingTwoFactorAttempts {
		if _, err := tx.Exec(ctx, `DELETE FROM pending_two_factor_logins WHERE token = $1`, hashToken(token)); err != nil {
			return "", "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", "", err
		}
		return "", "", ErrInvalidPendingLogin
	}

	// The actual TOTP/backup-code check happens against `pool`, not `tx` —
	// VerifySecondFactorCode may write a backup-code's used_at, which
	// should stick even though this function's own transaction is about
	// to either commit a small counter update or a delete; there's no
	// correctness reason those two writes need to be atomic with each
	// other.
	ok, verr := VerifySecondFactorCode(ctx, pool, userID, code)
	if verr != nil {
		return "", "", verr
	}

	if !ok {
		if _, err := tx.Exec(ctx,
			`UPDATE pending_two_factor_logins SET attempts = attempts + 1 WHERE token = $1`, hashToken(token),
		); err != nil {
			return "", "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", "", err
		}
		return "", "", ErrInvalidTOTPCode
	}

	if _, err := tx.Exec(ctx, `DELETE FROM pending_two_factor_logins WHERE token = $1`, hashToken(token)); err != nil {
		return "", "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return userID, next, nil
}

// PendingTwoFactorLoginOwner reads who a pending login belongs to, and
// where to send them afterwards, without counting an attempt or
// consuming anything — the security-key path needs this twice (to build
// the challenge and to check the answer) before it is entitled to a
// session.
func PendingTwoFactorLoginOwner(ctx context.Context, pool *pgxpool.Pool, token string) (userID, next string, err error) {
	err = pool.QueryRow(ctx, `
		SELECT user_id, next FROM pending_two_factor_logins
		WHERE token = $1 AND expires_at > now() AND attempts < $2
	`, hashToken(token), maxPendingTwoFactorAttempts).Scan(&userID, &next)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrInvalidPendingLogin
	}
	return userID, next, err
}

// RecordPendingTwoFactorFailure counts a failed security-key answer
// against the pending login, under the same cap as a wrong code. A
// signature is not guessable the way six digits are, so the cap is not
// doing the same work here — but one cap is easier to reason about than
// two, and a pending login that keeps failing is one to discard.
func RecordPendingTwoFactorFailure(ctx context.Context, pool *pgxpool.Pool, token string) error {
	_, err := pool.Exec(ctx,
		`UPDATE pending_two_factor_logins SET attempts = attempts + 1 WHERE token = $1`, hashToken(token))
	return err
}

// ConsumePendingTwoFactorLogin deletes the pending row once a security
// key has answered correctly, so the caller can issue the real session.
// Returns ErrInvalidPendingLogin if it was already gone — two answers to
// one pending login must not both succeed.
func ConsumePendingTwoFactorLogin(ctx context.Context, pool *pgxpool.Pool, token string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM pending_two_factor_logins WHERE token = $1 AND expires_at > now()`, hashToken(token))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidPendingLogin
	}
	return nil
}
