package calendar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This file is the outbound half of calendar sharing: the personal secret
// address a family subscribes to from a phone.
//
// The security model is deliberately simple, and worth stating plainly
// because the URL is handed to third parties (Google's and Apple's
// servers fetch it on the subscriber's behalf, and it will sit in their
// logs):
//
//   - The token IS the authentication. Anyone holding the URL sees
//     exactly what its owner sees, with no login. That is unavoidable for
//     a subscription — a calendar client cannot log in — and is why the
//     link is per-person and revocable rather than one shared address.
//   - It is read-only. The feed endpoint performs no writes beyond
//     stamping last_used_at.
//   - It is scoped at request time, not at generation time, so a Scout
//     who changes patrol, or a parent who stops being a leader, gets the
//     right events on their next refresh without anybody reissuing
//     anything.

// FeedTokenBytes is the entropy behind a feed token. 32 bytes is the same
// as a session cookie: this is a bearer credential that lives in a URL
// indefinitely, so it gets the same treatment.
const FeedTokenBytes = 32

// ErrNoFeedToken means this person has not generated a link yet.
var ErrNoFeedToken = errors.New("calendar: no feed token for this user and unit")

// HashFeedToken is exported because the web layer receives the plaintext
// token from the URL and needs to look up its row.
func HashFeedToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// FeedTokenOwner is who a presented token belongs to.
type FeedTokenOwner struct {
	UserID string
	UnitID string
}

// SetFeedToken stores (or replaces) the token for one person in one unit.
// The caller generates the plaintext with auth.RandomToken(FeedTokenBytes)
// and is responsible for showing it to the owner once — it cannot be
// recovered from here afterwards.
//
// Replacing is the revocation mechanism: the old hash is overwritten, so
// the previous URL stops working the moment this returns.
func SetFeedToken(ctx context.Context, pool *pgxpool.Pool, userID, unitID, token string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO calendar_feed_tokens (user_id, unit_id, token_hash)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, unit_id)
		DO UPDATE SET token_hash = EXCLUDED.token_hash, created_at = now(), last_used_at = NULL
	`, userID, unitID, HashFeedToken(token))
	return err
}

// DeleteFeedToken turns the subscription off entirely, as opposed to
// issuing a new one.
func DeleteFeedToken(ctx context.Context, pool *pgxpool.Pool, userID, unitID string) error {
	_, err := pool.Exec(ctx,
		`DELETE FROM calendar_feed_tokens WHERE user_id = $1 AND unit_id = $2`, userID, unitID)
	return err
}

// FeedTokenExists reports whether a link has been generated, for the
// settings page — which can say "you have a link" but never show it
// again, since only the hash is kept.
func FeedTokenExists(ctx context.Context, pool *pgxpool.Pool, userID, unitID string) (created time.Time, lastUsed *time.Time, ok bool, err error) {
	err = pool.QueryRow(ctx,
		`SELECT created_at, last_used_at FROM calendar_feed_tokens WHERE user_id = $1 AND unit_id = $2`,
		userID, unitID).Scan(&created, &lastUsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil, false, nil
	}
	if err != nil {
		return time.Time{}, nil, false, err
	}
	return created, lastUsed, true, nil
}

// ResolveFeedToken finds who a presented token belongs to, and records
// the fetch.
//
// The unitID argument is the unit the request's hostname resolved to, and
// the token must belong to it: a Troop token presented to the Pack's
// subdomain is not valid there. Without that check a single token would
// serve both subdomains, quietly widening what it grants.
func ResolveFeedToken(ctx context.Context, pool *pgxpool.Pool, token, unitID string) (FeedTokenOwner, error) {
	var owner FeedTokenOwner
	err := pool.QueryRow(ctx, `
		UPDATE calendar_feed_tokens
		SET last_used_at = now()
		WHERE token_hash = $1 AND unit_id = $2
		RETURNING user_id, unit_id
	`, HashFeedToken(token), unitID).Scan(&owner.UserID, &owner.UnitID)
	if errors.Is(err, pgx.ErrNoRows) {
		return FeedTokenOwner{}, ErrNoFeedToken
	}
	return owner, err
}
