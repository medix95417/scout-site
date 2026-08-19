// Package newsletter lets a leader draft a subject/body, edit it freely,
// and then send it once — by email, via internal/mailer — to every family
// currently in the unit's roster. Deliberately a one-way draft-to-sent
// transition (unlike internal/content's draft/published toggle): once a
// newsletter has actually gone out, editing or "unsending" it wouldn't
// un-deliver the emails already sitting in people's inboxes, so the UI
// (see internal/web/newsletter.go) doesn't offer to.
package newsletter

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
	"github.com/47-yonkers/scout-site/internal/mailer"
)

// Newsletter is one draft or sent newsletter.
type Newsletter struct {
	ID             string
	UnitID         string
	Subject        string
	Body           string
	Status         string // "draft" | "sent"
	CreatedBy      string
	SentAt         *time.Time
	RecipientCount *int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

var (
	// ErrNotFound covers "no such newsletter" and "exists but belongs to
	// a different unit" alike — same "don't distinguish" posture every
	// other unit-scoped lookup in this codebase takes (see
	// internal/ledger's package comment).
	ErrNotFound = errors.New("newsletter: not found in this unit")

	// ErrAlreadySent guards both UpdateDraft and Send against a
	// newsletter that's already gone out — editing or re-sending
	// wouldn't affect the copies already delivered, so both are refused
	// outright rather than silently no-op'd.
	ErrAlreadySent = errors.New("newsletter: already sent")

	// ErrNoRecipients is returned by Send when the unit's roster
	// resolves to zero email addresses — sending "to nobody" almost
	// certainly means the roster is empty or misconfigured, not that the
	// leader meant to send nothing.
	ErrNoRecipients = errors.New("newsletter: no recipients found for this unit")
)

const columns = `id, unit_id, subject, body, status::text, created_by, sent_at, recipient_count, created_at, updated_at`

func scan(row interface{ Scan(dest ...any) error }) (Newsletter, error) {
	var n Newsletter
	err := row.Scan(&n.ID, &n.UnitID, &n.Subject, &n.Body, &n.Status, &n.CreatedBy, &n.SentAt, &n.RecipientCount, &n.CreatedAt, &n.UpdatedAt)
	return n, err
}

// CreateDraft creates a new newsletter as a draft.
func CreateDraft(ctx context.Context, pool *pgxpool.Pool, unitID, subject, body, actorID string) (Newsletter, error) {
	n, err := scan(pool.QueryRow(ctx, `
		INSERT INTO newsletters (unit_id, subject, body, created_by)
		VALUES ($1, $2, $3, $4)
		RETURNING `+columns,
		unitID, subject, body, actorID))
	if err != nil {
		return Newsletter{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "newsletter",
		EntityID:   n.ID,
		ActorID:    &actorID,
		Action:     "create",
		After:      n,
	})
	return n, nil
}

// UpdateDraft edits a still-unsent newsletter's subject/body. Returns
// ErrAlreadySent (rather than silently applying the edit) if it's since
// been sent — the WHERE clause below is what actually enforces that
// atomically, not a separate read-then-write check.
func UpdateDraft(ctx context.Context, pool *pgxpool.Pool, id, unitID, subject, body, actorID string) (Newsletter, error) {
	before, err := GetNewsletter(ctx, pool, id, unitID)
	if err != nil {
		return Newsletter{}, err
	}

	n, err := scan(pool.QueryRow(ctx, `
		UPDATE newsletters SET subject = $1, body = $2, updated_at = now()
		WHERE id = $3 AND unit_id = $4 AND status = 'draft'
		RETURNING `+columns,
		subject, body, id, unitID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Newsletter{}, ErrAlreadySent
		}
		return Newsletter{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "newsletter",
		EntityID:   n.ID,
		ActorID:    &actorID,
		Action:     "update",
		Before:     before,
		After:      n,
	})
	return n, nil
}

// GetNewsletter looks up a newsletter, scoped to a unit — ErrNotFound
// covers both "doesn't exist" and "belongs to a different unit."
func GetNewsletter(ctx context.Context, pool *pgxpool.Pool, id, unitID string) (Newsletter, error) {
	n, err := scan(pool.QueryRow(ctx, `SELECT `+columns+` FROM newsletters WHERE id = $1 AND unit_id = $2`, id, unitID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Newsletter{}, ErrNotFound
		}
		return Newsletter{}, err
	}
	return n, nil
}

// ListForUnit lists a unit's newsletters, most recently created first.
func ListForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Newsletter, error) {
	rows, err := pool.Query(ctx, `SELECT `+columns+` FROM newsletters WHERE unit_id = $1 ORDER BY created_at DESC`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Newsletter
	for rows.Next() {
		n, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// RecipientEmailsForUnit returns the distinct login email addresses for
// every family with at least one member on the unit's roster — the same
// "family has anyone with a role in this unit" reach units.RolesForFamilyInUnit
// and internal/auth.FamilyHasAnyTreasuryRole already use, just widened
// from "one family" to "every family in the unit." This naturally
// includes both family-wide logins and any individual member logins
// within those families (see migration 0009) — every users row carries
// family_id regardless of which kind of login it is.
func RecipientEmailsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT users.email
		FROM users
		WHERE users.family_id IN (
			SELECT DISTINCT members.family_id
			FROM role_assignments
			JOIN members ON members.id = role_assignments.member_id
			WHERE role_assignments.unit_id = $1
		)
		ORDER BY users.email
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var emails []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		emails = append(emails, email)
	}
	return emails, rows.Err()
}

// Result summarizes one Send call.
type Result struct {
	Sent   int
	Failed int
}

// Send delivers a draft newsletter to every recipient RecipientEmailsForUnit
// resolves for its unit, then marks it sent with the count that actually
// went out (Sent, not len(recipients) — a handful of bounced/invalid
// addresses shouldn't be hidden behind a count that implies full
// delivery). A per-recipient send failure is logged and counted but
// doesn't stop the rest of the batch, same posture as
// internal/reminders.Send. The newsletter is marked sent even if every
// single send failed (Sent == 0) — see the doc note on that below.
func Send(ctx context.Context, pool *pgxpool.Pool, m *mailer.Mailer, id, unitID, actorID string) (Result, error) {
	var res Result
	if !m.Enabled(ctx) {
		return res, fmt.Errorf("newsletter: mailer not configured — set SMTP_HOST etc. to enable email")
	}

	n, err := GetNewsletter(ctx, pool, id, unitID)
	if err != nil {
		return res, err
	}
	if n.Status != "draft" {
		return res, ErrAlreadySent
	}

	emails, err := RecipientEmailsForUnit(ctx, pool, unitID)
	if err != nil {
		return res, fmt.Errorf("newsletter: resolving recipients: %w", err)
	}
	if len(emails) == 0 {
		return res, ErrNoRecipients
	}

	for _, email := range emails {
		if err := m.Send(ctx, email, n.Subject, n.Body); err != nil {
			log.Printf("newsletter: sending %s to %s: %v", id, email, err)
			res.Failed++
			continue
		}
		res.Sent++
	}

	// Marked sent regardless of how many of the individual sends
	// succeeded — once an attempt has gone out to the list, "draft"
	// would be actively misleading (a second click would re-send to
	// everyone who *did* receive it the first time, not just retry the
	// failures). recipient_count records what actually succeeded so the
	// admin list can show "sent to 11 (2 failed)" rather than a number
	// that overstates delivery.
	if _, err := pool.Exec(ctx, `
		UPDATE newsletters SET status = 'sent', sent_at = now(), recipient_count = $1, updated_at = now()
		WHERE id = $2
	`, res.Sent, id); err != nil {
		log.Printf("newsletter: WARNING sent to %d recipients but failed to record it: %v", res.Sent, err)
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "newsletter",
		EntityID:   id,
		ActorID:    &actorID,
		Action:     "send",
		After:      map[string]any{"sent": res.Sent, "failed": res.Failed, "recipients": len(emails)},
	})

	return res, nil
}
