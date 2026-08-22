// Package permission implements digital permission slips: a leader
// attaches one consent form to an event, and a parent/guardian signs it
// once per Scout of theirs attending — see
// internal/db/migrations/0011_permission_slips.sql's comment for why this
// is per-Scout rather than per-family like an RSVP. A typed full name is
// the e-signature itself, recorded alongside who signed and when.
package permission

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

// Slip is one event's permission slip.
type Slip struct {
	ID        string
	EventID   string
	UnitID    string
	Title     string
	Body      string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Signature is one Scout's signed consent for a Slip.
type Signature struct {
	ID               string
	PermissionSlipID string
	ScoutMemberID    string
	SignedByMemberID string
	SignerName       string
	SignedAt         time.Time
}

// ErrNotFound covers "no such slip" and "belongs to a different unit"
// alike — same posture every other unit-scoped lookup in this codebase
// takes.
var ErrNotFound = errors.New("permission: slip not found in this unit")

// ErrAlreadyExists is returned by CreateSlip when the event already has
// one — permission_slips.event_id is UNIQUE, so a second attempt would
// otherwise just be a confusing database error; use UpdateSlip to edit
// the existing one instead.
var ErrAlreadyExists = errors.New("permission: this event already has a permission slip")

const slipColumns = `id, event_id, unit_id, title, body, created_by, created_at, updated_at`

func scanSlip(row interface{ Scan(dest ...any) error }) (Slip, error) {
	var s Slip
	err := row.Scan(&s.ID, &s.EventID, &s.UnitID, &s.Title, &s.Body, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

// CreateSlip attaches a new permission slip to an event.
func CreateSlip(ctx context.Context, pool *pgxpool.Pool, eventID, unitID, title, body, actorID string) (Slip, error) {
	s, err := scanSlip(pool.QueryRow(ctx, `
		INSERT INTO permission_slips (event_id, unit_id, title, body, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+slipColumns,
		eventID, unitID, title, body, actorID))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Slip{}, ErrAlreadyExists
		}
		return Slip{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "permission_slip",
		EntityID:   s.ID,
		ActorID:    &actorID,
		Action:     "create",
		After:      s,
	})
	return s, nil
}

// UpdateSlip edits a slip's title/body. Signatures already collected stay
// as-is — a family who already signed shouldn't have their consent
// silently invalidated (or, worse, silently still counted as valid) by a
// later wording change; a leader who needs everyone to re-consent after a
// substantive edit should say so out-of-band and treat old signatures as
// informational.
func UpdateSlip(ctx context.Context, pool *pgxpool.Pool, id, unitID, title, body, actorID string) (Slip, error) {
	before, err := GetSlip(ctx, pool, id, unitID)
	if err != nil {
		return Slip{}, err
	}

	s, err := scanSlip(pool.QueryRow(ctx, `
		UPDATE permission_slips SET title = $1, body = $2, updated_at = now()
		WHERE id = $3 AND unit_id = $4
		RETURNING `+slipColumns,
		title, body, id, unitID))
	if err != nil {
		return Slip{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "permission_slip",
		EntityID:   s.ID,
		ActorID:    &actorID,
		Action:     "update",
		Before:     before,
		After:      s,
	})
	return s, nil
}

// GetSlip looks up a slip by ID, scoped to a unit.
func GetSlip(ctx context.Context, pool *pgxpool.Pool, id, unitID string) (Slip, error) {
	s, err := scanSlip(pool.QueryRow(ctx, `SELECT `+slipColumns+` FROM permission_slips WHERE id = $1 AND unit_id = $2`, id, unitID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Slip{}, ErrNotFound
		}
		return Slip{}, err
	}
	return s, nil
}

// EventIDsWithSlips returns, as a set, every event ID in a unit that
// already has a permission slip attached — what the calendar page uses
// (alongside settings.PermissionSlipEnforcement) to decide whether to
// show the "Permission slip" link: an event a leader already attached a
// real slip to should never lose that link just because it wasn't also
// marked "requires a permission slip" at creation time.
func EventIDsWithSlips(ctx context.Context, pool *pgxpool.Pool, unitID string) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT event_id FROM permission_slips WHERE unit_id = $1`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make(map[string]bool)
	for rows.Next() {
		var eventID string
		if err := rows.Scan(&eventID); err != nil {
			return nil, err
		}
		ids[eventID] = true
	}
	return ids, rows.Err()
}

// GetSlipForEvent looks up an event's permission slip, if it has one.
func GetSlipForEvent(ctx context.Context, pool *pgxpool.Pool, eventID, unitID string) (Slip, bool, error) {
	s, err := scanSlip(pool.QueryRow(ctx, `SELECT `+slipColumns+` FROM permission_slips WHERE event_id = $1 AND unit_id = $2`, eventID, unitID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Slip{}, false, nil
		}
		return Slip{}, false, err
	}
	return s, true, nil
}

// Sign records (or updates, if this Scout already has a signature on this
// slip — a parent re-confirming, e.g. after re-reading an update) a
// parent/guardian's typed-name consent for one Scout.
func Sign(ctx context.Context, pool *pgxpool.Pool, slipID, scoutMemberID, signedByMemberID, signerName, actorID string) (Signature, error) {
	var sig Signature
	err := pool.QueryRow(ctx, `
		INSERT INTO permission_slip_signatures (permission_slip_id, scout_member_id, signed_by_member_id, signer_name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (permission_slip_id, scout_member_id)
		DO UPDATE SET signed_by_member_id = EXCLUDED.signed_by_member_id, signer_name = EXCLUDED.signer_name, signed_at = now()
		RETURNING id, permission_slip_id, scout_member_id, signed_by_member_id, signer_name, signed_at
	`, slipID, scoutMemberID, signedByMemberID, signerName).Scan(
		&sig.ID, &sig.PermissionSlipID, &sig.ScoutMemberID, &sig.SignedByMemberID, &sig.SignerName, &sig.SignedAt,
	)
	if err != nil {
		return Signature{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "permission_slip_signature",
		EntityID:   sig.ID,
		ActorID:    &actorID,
		Action:     "sign",
		After:      sig,
	})
	return sig, nil
}

// SignaturesForSlip lists every signature on a slip — the leader's
// compliance view. Keyed by ScoutMemberID by the caller as needed.
func SignaturesForSlip(ctx context.Context, pool *pgxpool.Pool, slipID string) ([]Signature, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, permission_slip_id, scout_member_id, signed_by_member_id, signer_name, signed_at
		FROM permission_slip_signatures WHERE permission_slip_id = $1
	`, slipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sigs []Signature
	for rows.Next() {
		var sig Signature
		if err := rows.Scan(&sig.ID, &sig.PermissionSlipID, &sig.ScoutMemberID, &sig.SignedByMemberID, &sig.SignerName, &sig.SignedAt); err != nil {
			return nil, err
		}
		sigs = append(sigs, sig)
	}
	return sigs, rows.Err()
}
