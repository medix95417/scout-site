package prospect

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

// Withdrawing consent to be emailed.
//
// The join form says the address will only be used to talk about
// membership and that this can be revoked at any time. That promise needs
// two things to be real: a way for the family to revoke it themselves,
// without asking the unit's permission, and a way for a leader to record
// a revocation that arrived some other way — a reply, a conversation at
// pickup.
//
// Both set the same flag. RecipientsForStatuses is the single query every
// send goes through, and it excludes the flag, so an opt-out cannot be
// forgotten by a caller.

// UnsubscribeToken is the proof carried by the link in a campaign email.
//
// It is an HMAC of the prospect's id under the site's signing secret, not
// a random string in a database column, for one practical reason: a
// stored token has to be readable in the clear at send time, so it would
// sit in the database next to the address it protects. Deriving it means
// there is nothing to leak — the same id always produces the same link,
// the secret never leaves the server, and nobody can forge one for a
// prospect they don't already know the id of.
//
// It is not a login. The only thing it authorizes is "stop emailing this
// one address", which is a request the site should honour from anyone
// holding the link, since holding the link means holding the email.
func UnsubscribeToken(secret []byte, prospectID string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("prospect-unsubscribe:"))
	mac.Write([]byte(prospectID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ErrBadUnsubscribeToken is returned for a link that doesn't verify —
// mistyped, truncated by a mail client, or made up.
var ErrBadUnsubscribeToken = errors.New("prospect: that unsubscribe link is not valid")

// Unsubscribe honours the link in a campaign email.
//
// Verifying with hmac.Equal rather than == is the usual constant-time
// comparison; the practical risk here is slight, but there is no reason
// to write the version that leaks timing.
//
// Deliberately idempotent: mail clients pre-fetch links, people click
// twice, and "you have already unsubscribed" is the same outcome as
// "you are now unsubscribed" as far as the person is concerned.
func Unsubscribe(ctx context.Context, pool *pgxpool.Pool, secret []byte, prospectID, token string) (Prospect, error) {
	want := UnsubscribeToken(secret, prospectID)
	if !hmac.Equal([]byte(want), []byte(token)) {
		return Prospect{}, ErrBadUnsubscribeToken
	}

	p, err := scan(pool.QueryRow(ctx, `
		UPDATE prospects
		SET email_opt_out = true,
		    opt_out_at = COALESCE(opt_out_at, now()),
		    updated_at = now()
		WHERE id = $1
		RETURNING `+columns, prospectID))
	if err != nil {
		return Prospect{}, err
	}

	// No actor: nobody on the roster did this, the family did. Recorded
	// all the same, because "when did they opt out" is exactly what a
	// leader needs when someone asks why they stopped hearing from us.
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "prospect", EntityID: p.ID, Action: "email_opt_out",
		After: map[string]any{"email_opt_out": true, "by": "the family, via the link in a campaign email"},
	})
	return p, nil
}

// SetEmailOptOut is the leader-operated form of the same switch, for a
// family who asked to be taken off the list some other way — and for
// putting somebody back on at their request.
func SetEmailOptOut(ctx context.Context, pool *pgxpool.Pool, unitID, id string, optOut bool, actorID string) (Prospect, error) {
	before, err := Get(ctx, pool, unitID, id)
	if err != nil {
		return Prospect{}, err
	}

	after, err := scan(pool.QueryRow(ctx, `
		UPDATE prospects
		SET email_opt_out = $1,
		    opt_out_at = CASE WHEN $1 THEN COALESCE(opt_out_at, now()) ELSE NULL END,
		    updated_at = now()
		WHERE id = $2 AND unit_id = $3
		RETURNING `+columns, optOut, id, unitID))
	if err != nil {
		return Prospect{}, err
	}

	action := "email_opt_in"
	if optOut {
		action = "email_opt_out"
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "prospect", EntityID: after.ID, ActorID: &actorID, Action: action,
		Before: map[string]any{"email_opt_out": before.EmailOptOut},
		After:  map[string]any{"email_opt_out": after.EmailOptOut},
	})
	return after, nil
}

// GetAnyUnit loads a prospect without knowing which unit it belongs to —
// only for the unsubscribe page, which is reached from an email link and
// has no signed-in leader or admin context to scope by. The HMAC is what
// authorizes it; the unit is read from the row rather than trusted from
// the request.
func GetAnyUnit(ctx context.Context, pool *pgxpool.Pool, id string) (Prospect, error) {
	return scan(pool.QueryRow(ctx, `SELECT `+columns+` FROM prospects WHERE id = $1`, id))
}
