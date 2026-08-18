// Package approval implements the generic approval-request workflow used by
// SPL/Patrol Leader calendar submissions in Phase 1. It's intentionally
// generic (entity_type + entity_id, not "event_id") so Phase 2 can reuse it
// unchanged for things like a treasurer sign-off on a trip-fund transfer —
// see scout-website-architecture-phase1.md Section 5.
package approval

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

type Request struct {
	ID          string
	EntityType  string
	EntityID    string
	UnitID      string
	SubmittedBy string
	Status      string // "pending" | "approved" | "rejected"
	DecidedBy   *string
	DecidedAt   *time.Time
}

// Submit creates a pending approval request for an entity (e.g. an event a
// Patrol Leader just created) and logs the submission to the audit trail.
func Submit(ctx context.Context, pool *pgxpool.Pool, entityType, entityID, unitID, submittedBy string) (Request, error) {
	var req Request
	err := pool.QueryRow(ctx, `
		INSERT INTO approval_requests (entity_type, entity_id, unit_id, submitted_by)
		VALUES ($1, $2, $3, $4)
		RETURNING id, entity_type, entity_id, unit_id, submitted_by, status::text
	`, entityType, entityID, unitID, submittedBy).Scan(
		&req.ID, &req.EntityType, &req.EntityID, &req.UnitID, &req.SubmittedBy, &req.Status,
	)
	if err != nil {
		return Request{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: entityType,
		EntityID:   entityID,
		ActorID:    &submittedBy,
		Action:     "submit_for_approval",
	})

	return req, nil
}

// ErrNotFound is returned by Decide when requestID doesn't identify a
// pending request in unitID — either it doesn't exist, was already
// decided, or (the case this exists to guard against) it belongs to a
// different unit. Callers should treat this as "not found" (404/400), not
// a server error.
var ErrNotFound = errors.New("approval: no pending request with that id in this unit")

// ErrInsufficientFunds is returned by Decide when approving a
// "ledger_transaction" request (a Phase 2 trip-fund push transfer) whose
// source account no longer has enough posted balance to cover it — the
// balance is re-checked here, at decision time, since it can drop between
// when a transfer is requested and when a Treasurer gets to it (e.g.
// another transfer clearing first). The request is left pending on this
// error — nothing is decided — so the Treasurer can ask the family to
// resubmit for a smaller amount, or reject it outright.
var ErrInsufficientFunds = errors.New("approval: insufficient balance to approve this transfer")

// Decide approves or rejects a pending request. approverID must hold an
// approver role in unitID — callers are expected to have already checked
// units.CanApprove (or, for a ledger transfer, units.CanManageLedger)
// before calling this; Decide itself doesn't re-check roles, to keep the
// two concerns (workflow vs. permissions) separate. It does, however,
// require the request to belong to unitID: without that check, a Troop
// leader (who legitimately holds CanApprove for the Troop) could approve
// or reject a *Pack* submission by guessing its request ID, since a bare
// "am I an approver of some unit?" check says nothing about which unit a
// specific request belongs to.
//
// The approval-request update and the entity-specific side effect (flip
// an event's status, or a ledger transaction's) happen inside one
// database transaction — deliberately, so a crash between the two can
// never leave an approval marked "approved" while the thing it approved
// never actually took effect. This matters more now that one of the
// entity types this reaches is real money.
func Decide(ctx context.Context, pool *pgxpool.Pool, requestID, unitID, approverID string, approve bool) error {
	status := "rejected"
	if approve {
		status = "approved"
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if already committed

	var entityType, entityID string
	err = tx.QueryRow(ctx, `
		UPDATE approval_requests
		SET status = $1, decided_by = $2, decided_at = now()
		WHERE id = $3 AND unit_id = $4 AND status = 'pending'
		RETURNING entity_type, entity_id
	`, status, approverID, requestID, unitID).Scan(&entityType, &entityID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}

	switch entityType {
	case "event":
		// Flip the calendar event from pending_approval to its resolved
		// status so it actually shows up (or doesn't) on the calendar.
		newStatus := "published"
		if !approve {
			newStatus = "rejected"
		}
		if _, err := tx.Exec(ctx,
			`UPDATE events SET status = $1, updated_at = now() WHERE id = $2`,
			newStatus, entityID,
		); err != nil {
			return err
		}

	case "ledger_transaction":
		// A Phase 2 trip-fund push transfer (internal/ledger.
		// RequestTripFundTransfer) — its postings already exist but are
		// excluded from every balance calculation while status stays
		// 'pending_approval'. Approving it is what makes it count.
		if approve {
			var sufficient bool
			if err := tx.QueryRow(ctx, `
				SELECT COALESCE((
					SELECT SUM(other.amount_cents)
					FROM ledger_postings other
					JOIN ledger_transactions ot ON ot.id = other.transaction_id
					WHERE other.account_id = debit.account_id AND ot.status = 'posted'
				), 0) + debit.amount_cents >= 0
				FROM ledger_postings debit
				WHERE debit.transaction_id = $1 AND debit.amount_cents < 0
			`, entityID).Scan(&sufficient); err != nil {
				return err
			}
			if !sufficient {
				return ErrInsufficientFunds
			}
			if _, err := tx.Exec(ctx, `UPDATE ledger_transactions SET status = 'posted' WHERE id = $1`, entityID); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE ledger_transactions SET status = 'rejected' WHERE id = $1`, entityID); err != nil {
				return err
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	action := "reject"
	if approve {
		action = "approve"
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType:        entityType,
		EntityID:          entityID,
		ActorID:           &approverID,
		Action:            action,
		ApprovalRequestID: &requestID,
	})

	return nil
}

// PendingForUnit lists approval requests awaiting a decision in a unit —
// what an SM/ASM sees on their "needs your approval" view.
func PendingForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Request, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, entity_type, entity_id, unit_id, submitted_by, status::text
		FROM approval_requests
		WHERE unit_id = $1 AND status = 'pending'
		ORDER BY created_at
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reqs []Request
	for rows.Next() {
		var r Request
		if err := rows.Scan(&r.ID, &r.EntityType, &r.EntityID, &r.UnitID, &r.SubmittedBy, &r.Status); err != nil {
			return nil, err
		}
		reqs = append(reqs, r)
	}
	return reqs, rows.Err()
}
