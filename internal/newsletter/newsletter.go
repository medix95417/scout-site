// Package newsletter lets a leader draft a subject/body, edit it freely,
// and then send it once — by email, via internal/mailer — to every family
// currently in the unit's roster. Deliberately a one-way draft-to-sent
// transition (unlike internal/content's draft/published toggle): once a
// newsletter has actually gone out, editing or "unsending" it wouldn't
// un-deliver the emails already sitting in people's inboxes, so the UI
// (see internal/web/newsletter.go) doesn't offer to.
//
// body is real HTML, authored via a WYSIWYG editor (see
// admin-newsletter-form.html) — the one place in this codebase raw HTML
// from a form is stored and rendered/emailed rather than escaped as plain
// text. See Sanitize: every write path (CreateDraft, UpdateDraft) runs body
// through it before it ever reaches the database.
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

// Newsletter is one draft, sending, or sent newsletter.
type Newsletter struct {
	ID             string
	UnitID         string
	Subject        string
	Body           string
	Status         string // "draft" | "sending" | "sent"
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

	// ErrAlreadySent guards UpdateDraft and BeginSend against a
	// newsletter that's already sending or has already gone out —
	// editing or re-sending wouldn't affect the copies already delivered
	// (or already queued to go out), so both are refused outright rather
	// than silently no-op'd.
	ErrAlreadySent = errors.New("newsletter: already sending or already sent")

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

// CreateDraft creates a new newsletter as a draft. body is expected to be
// HTML authored via the WYSIWYG editor (see admin-newsletter-form.html) —
// it's run through Sanitize before it's ever written to the database, not
// just before it's sent, so a stored draft is exactly what the editor will
// safely reload later too.
func CreateDraft(ctx context.Context, pool *pgxpool.Pool, unitID, subject, body, actorID string) (Newsletter, error) {
	body = Sanitize(body)
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
	body = Sanitize(body)

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

// Result summarizes one SendNow call.
type Result struct {
	Sent   int
	Failed int
}

// abandonedSendAge is how long a newsletter can sit at "sending" before
// BeginSend treats it as abandoned (the process that started it crashed
// or was redeployed mid-send, without ever reaching SendNow's finalizing
// UPDATE) and allows a fresh send to reclaim it — otherwise a newsletter
// orphaned that way could never be sent again. Set comfortably past
// internal/web's own bound on how long the background send is allowed to
// run, so a send that's still genuinely in progress is never reclaimed
// out from under itself.
const abandonedSendAge = 40 * time.Minute

// BeginSend validates that id is sendable (mail is configured, it's
// still a draft — or a "sending" one abandoned long enough ago to treat
// as dead, see abandonedSendAge — and the unit's roster resolves to at
// least one recipient) and atomically claims it by transitioning its
// status to "sending". It does none of the actual, slow network sending
// itself — that's SendNow's job, meant to be run separately (in a
// goroutine detached from the HTTP request that called BeginSend — see
// internal/web/newsletter.go) so a browser disconnect or reverse-proxy
// timeout can never cut a send short.
func BeginSend(ctx context.Context, pool *pgxpool.Pool, m *mailer.Mailer, id, unitID string) (Newsletter, error) {
	if !m.Enabled(ctx) {
		return Newsletter{}, fmt.Errorf("newsletter: mailer not configured — set SMTP_HOST etc. to enable email")
	}

	n, err := GetNewsletter(ctx, pool, id, unitID)
	if err != nil {
		return Newsletter{}, err
	}
	if n.Status == "sent" {
		return Newsletter{}, ErrAlreadySent
	}
	if n.Status == "sending" && time.Since(n.UpdatedAt) < abandonedSendAge {
		return Newsletter{}, ErrAlreadySent
	}

	emails, err := RecipientEmailsForUnit(ctx, pool, unitID)
	if err != nil {
		return Newsletter{}, fmt.Errorf("newsletter: resolving recipients: %w", err)
	}
	if len(emails) == 0 {
		return Newsletter{}, ErrNoRecipients
	}

	claimed, err := scan(pool.QueryRow(ctx, `
		UPDATE newsletters SET status = 'sending', updated_at = now()
		WHERE id = $1 AND unit_id = $2 AND status != 'sent'
		RETURNING `+columns,
		id, unitID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Newsletter{}, ErrAlreadySent // sent (or claimed by a concurrent send) between the check above and this UPDATE
		}
		return Newsletter{}, err
	}
	return claimed, nil
}

// SendNow does the actual per-recipient sending and finalizes the
// newsletter's status once done — the slow half of sending, deliberately
// separate from BeginSend so it can run on a context that outlives the
// HTTP request that triggered it (ctx should be context.Background(), or
// a generous context.WithTimeout of one — never r.Context()). n must
// already be in "sending" status (see BeginSend); its Subject/Body are
// used as passed in rather than re-fetched, since they can't change once
// sending has started (UpdateDraft refuses to touch anything but a
// "draft"). A per-recipient send failure is logged and counted but
// doesn't stop the rest of the batch, same posture as
// internal/reminders.Send. Marked sent regardless of how many of the
// individual sends succeeded — recipient_count records what actually
// succeeded so the admin list can show "sent to 11 (2 failed)" rather
// than a count that overstates delivery.
func SendNow(ctx context.Context, pool *pgxpool.Pool, m *mailer.Mailer, n Newsletter, actorID string) Result {
	var res Result

	emails, err := RecipientEmailsForUnit(ctx, pool, n.UnitID)
	if err != nil {
		// The roster resolved fine moments ago in BeginSend — a failure
		// here is a transient DB hiccup, not "there are no recipients."
		// Logged and treated as zero recipients rather than leaving the
		// newsletter stuck at "sending" forever with no finalizing
		// UPDATE ever reached.
		log.Printf("newsletter: re-resolving recipients for %s: %v", n.ID, err)
	}

	for _, email := range emails {
		var sendErr string
		if err := m.SendHTML(ctx, email, n.Subject, n.Body); err != nil {
			log.Printf("newsletter: sending %s to %s: %v", n.ID, email, err)
			sendErr = truncateError(err.Error())
			res.Failed++
		} else {
			res.Sent++
		}

		// Recorded per address, not just counted.
		//
		// recipient_count already said how many; it could never say
		// which, so "did our newsletter reach the Okonkwos?" had no
		// answer, and neither did "which addresses are bouncing". A
		// failure to write this row is logged but doesn't stop the send:
		// the email has already gone, and abandoning the rest of the
		// batch over a bookkeeping error would be the worse outcome.
		var sentAt *time.Time
		if sendErr == "" {
			now := time.Now()
			sentAt = &now
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO newsletter_recipients (newsletter_id, email, sent_at, error)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (newsletter_id, email)
			DO UPDATE SET sent_at = EXCLUDED.sent_at, error = EXCLUDED.error
		`, n.ID, email, sentAt, sendErr); err != nil {
			log.Printf("newsletter: recording %s recipient %s: %v", n.ID, email, err)
		}
	}

	if _, err := pool.Exec(ctx, `
		UPDATE newsletters SET status = 'sent', sent_at = now(), recipient_count = $1, updated_at = now()
		WHERE id = $2
	`, res.Sent, n.ID); err != nil {
		log.Printf("newsletter: WARNING sent to %d recipients but failed to record it: %v", res.Sent, err)
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "newsletter",
		EntityID:   n.ID,
		ActorID:    &actorID,
		Action:     "send",
		After:      map[string]any{"sent": res.Sent, "failed": res.Failed, "recipients": len(emails)},
	})

	return res
}

// Recipient is one address a newsletter went to, and what happened.
type Recipient struct {
	Email  string
	SentAt *time.Time
	Err    string
}

// RecipientsFor reads back who a newsletter actually reached.
//
// Ordered by address rather than by send time: a leader looking at this
// is nearly always checking whether one particular family got it, and a
// list in alphabetical order is one they can scan.
func RecipientsFor(ctx context.Context, pool *pgxpool.Pool, newsletterID string) ([]Recipient, error) {
	rows, err := pool.Query(ctx, `
		SELECT email, sent_at, error FROM newsletter_recipients
		WHERE newsletter_id = $1
		ORDER BY email
	`, newsletterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Recipient
	for rows.Next() {
		var r Recipient
		if err := rows.Scan(&r.Email, &r.SentAt, &r.Err); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// truncateError keeps a delivery failure short enough for a table cell
// without losing the part that says what went wrong.
func truncateError(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	return s[:max] + "\u2026"
}
