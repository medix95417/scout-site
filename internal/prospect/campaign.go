package prospect

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
	"github.com/47-yonkers/scout-site/internal/mailer"
)

// Mass email to prospective families — the follow-up half of the join
// form. Somebody enquires in March, a leader writes to everyone still at
// "new" and "contacted" in August when the program year starts.
//
// It is deliberately not the newsletter with a different recipient list.
// A newsletter goes to people who joined a unit and have a login; a
// campaign goes to members of the public who gave an address for one
// stated purpose, which brings obligations a newsletter does not have:
// every message carries a way out, an opt-out is honoured forever, and
// what was sent to whom is kept so the unit can answer for it.

// Campaign is one mass email, drafted then sent.
type Campaign struct {
	ID     string
	UnitID string

	Subject string
	Body    string
	// TargetStatuses is which prospect statuses it was aimed at. Recorded
	// as sent rather than recomputed: a prospect's status moves on, and
	// "who did we write to in March" must not change its answer in April.
	TargetStatuses []string

	Status         string // "draft" | "sending" | "sent"
	CreatedBy      string
	SentAt         *time.Time
	RecipientCount int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// StatusLabels renders TargetStatuses the way the admin page reads them.
func (c Campaign) StatusLabels() string {
	if len(c.TargetStatuses) == 0 {
		return "no one"
	}
	labels := make([]string, 0, len(c.TargetStatuses))
	for _, v := range c.TargetStatuses {
		label := v
		for _, s := range Statuses {
			if s.Value == v {
				label = s.Label
				break
			}
		}
		labels = append(labels, label)
	}
	return strings.Join(labels, ", ")
}

// Sent reports whether this campaign has gone out and is therefore frozen.
func (c Campaign) Sent() bool { return c.Status == "sent" }

// Recipient is one prospect a campaign is addressed to, or was.
type Recipient struct {
	ProspectID string
	Name       string
	Email      string
	// SentAt and Err are populated when reading back a sent campaign's
	// record; both are zero when planning one.
	SentAt *time.Time
	Err    string
}

const campaignColumns = `id::text, unit_id::text, subject, body, target_statuses,
	status::text, created_by::text, sent_at, recipient_count, created_at, updated_at`

func scanCampaign(row interface{ Scan(...any) error }) (Campaign, error) {
	var c Campaign
	err := row.Scan(&c.ID, &c.UnitID, &c.Subject, &c.Body, &c.TargetStatuses,
		&c.Status, &c.CreatedBy, &c.SentAt, &c.RecipientCount, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

var (
	// ErrCampaignNotFound covers both "no such campaign" and "not this
	// unit's", deliberately not distinguishing them.
	ErrCampaignNotFound = errors.New("prospect: no such campaign in this unit")
	// ErrCampaignSent refuses to edit or re-send something already sent.
	// A sent campaign is a record of what went out; changing it would
	// make the record a lie, and re-sending it is a second campaign.
	ErrCampaignSent = errors.New("prospect: that campaign has already been sent")
	// ErrNoCampaignRecipients stops a send that would reach nobody, which
	// is otherwise indistinguishable on screen from one that worked.
	ErrNoCampaignRecipients = errors.New("prospect: no prospects match those statuses — nobody would receive this")
	// ErrMailerDisabled is the "email isn't set up on this site" case,
	// same posture as the newsletter's.
	ErrMailerDisabled = errors.New("prospect: email is not configured for this site")
)

// MaxSubject bounds the subject line. Long enough for anything sensible,
// short enough that it can't be used to smuggle a payload into a header.
const MaxSubject = 200

// CreateCampaign starts a draft.
func CreateCampaign(ctx context.Context, pool *pgxpool.Pool, unitID, subject, body string, statuses []string, actorID string) (Campaign, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return Campaign{}, fmt.Errorf("%w: a subject line is required", ErrInvalid)
	}
	if len(subject) > MaxSubject {
		return Campaign{}, fmt.Errorf("%w: that subject line is too long", ErrInvalid)
	}

	c, err := scanCampaign(pool.QueryRow(ctx, `
		INSERT INTO prospect_campaigns (unit_id, subject, body, target_statuses, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+campaignColumns,
		unitID, subject, body, filterStatuses(statuses), actorID))
	if err != nil {
		return Campaign{}, err
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "prospect_campaign", EntityID: c.ID, ActorID: &actorID, Action: "create",
		After: map[string]any{"subject": c.Subject, "target_statuses": c.TargetStatuses},
	})
	return c, nil
}

// UpdateCampaign edits a draft. Refuses anything already sent.
func UpdateCampaign(ctx context.Context, pool *pgxpool.Pool, id, unitID, subject, body string, statuses []string, actorID string) (Campaign, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return Campaign{}, fmt.Errorf("%w: a subject line is required", ErrInvalid)
	}
	if len(subject) > MaxSubject {
		return Campaign{}, fmt.Errorf("%w: that subject line is too long", ErrInvalid)
	}

	existing, err := GetCampaign(ctx, pool, id, unitID)
	if err != nil {
		return Campaign{}, err
	}
	if existing.Status != "draft" {
		return Campaign{}, ErrCampaignSent
	}

	c, err := scanCampaign(pool.QueryRow(ctx, `
		UPDATE prospect_campaigns
		SET subject = $1, body = $2, target_statuses = $3, updated_at = now()
		WHERE id = $4 AND unit_id = $5 AND status = 'draft'
		RETURNING `+campaignColumns,
		subject, body, filterStatuses(statuses), id, unitID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrCampaignSent
	}
	if err != nil {
		return Campaign{}, err
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "prospect_campaign", EntityID: c.ID, ActorID: &actorID, Action: "update",
		After: map[string]any{"subject": c.Subject, "target_statuses": c.TargetStatuses},
	})
	return c, nil
}

// GetCampaign loads one, scoped to its unit.
func GetCampaign(ctx context.Context, pool *pgxpool.Pool, id, unitID string) (Campaign, error) {
	c, err := scanCampaign(pool.QueryRow(ctx,
		`SELECT `+campaignColumns+` FROM prospect_campaigns WHERE id = $1 AND unit_id = $2`, id, unitID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrCampaignNotFound
	}
	return c, err
}

// ListCampaigns returns a unit's campaigns, newest first.
func ListCampaigns(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Campaign, error) {
	rows, err := pool.Query(ctx,
		`SELECT `+campaignColumns+` FROM prospect_campaigns WHERE unit_id = $1 ORDER BY created_at DESC`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Campaign
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteCampaign removes a draft. A sent campaign is kept: it is the
// record of what the unit sent to members of the public, and deleting it
// would leave the recipients' opt-out rights unaccountable.
func DeleteCampaign(ctx context.Context, pool *pgxpool.Pool, id, unitID, actorID string) error {
	c, err := GetCampaign(ctx, pool, id, unitID)
	if err != nil {
		return err
	}
	if c.Status == "sent" {
		return ErrCampaignSent
	}
	if _, err := pool.Exec(ctx, `DELETE FROM prospect_campaigns WHERE id = $1 AND unit_id = $2`, id, unitID); err != nil {
		return err
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "prospect_campaign", EntityID: id, ActorID: &actorID, Action: "delete",
	})
	return nil
}

// RecipientsForStatuses is who a campaign aimed at these statuses would
// reach right now.
//
// Opted-out prospects are excluded here, in the one query every send and
// every preview goes through, rather than at each call site — an opt-out
// that depends on the caller remembering to check it is not an opt-out.
// Addresses are de-duplicated because one family can enquire twice, and
// being written to twice is how a recruiting email becomes a complaint.
//
// The two interact, and the obvious way to write it is wrong. Filtering
// out opted-out ROWS and then de-duplicating by address lets a family who
// enquired twice keep receiving mail: the opt-out is recorded against one
// row, and the de-duplication happily picks the other. So the exclusion
// is by ADDRESS — if any enquiry from this address has opted out, the
// address is out, including one added after the opt-out. That is what a
// person means when they unsubscribe, and it took a two-enquiry test
// against a real database to notice the difference.
func RecipientsForStatuses(ctx context.Context, pool *pgxpool.Pool, unitID string, statuses []string) ([]Recipient, error) {
	wanted := filterStatuses(statuses)
	if len(wanted) == 0 {
		return nil, nil
	}

	rows, err := pool.Query(ctx, `
		SELECT DISTINCT ON (lower(p.parent_email))
		       p.id::text, p.parent_name, p.parent_email
		FROM prospects p
		WHERE p.unit_id = $1
		  AND p.status::text = ANY($2)
		  AND NOT p.email_opt_out
		  AND NOT EXISTS (
		      SELECT 1 FROM prospects q
		      WHERE q.unit_id = p.unit_id
		        AND lower(q.parent_email) = lower(p.parent_email)
		        AND q.email_opt_out
		  )
		ORDER BY lower(p.parent_email), p.created_at DESC
	`, unitID, wanted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Recipient
	for rows.Next() {
		var r Recipient
		if err := rows.Scan(&r.ProspectID, &r.Name, &r.Email); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CampaignRecipients reads back who a sent campaign actually reached.
func CampaignRecipients(ctx context.Context, pool *pgxpool.Pool, campaignID string) ([]Recipient, error) {
	rows, err := pool.Query(ctx, `
		SELECT COALESCE(prospect_id::text, ''), name, email, sent_at, error
		FROM prospect_campaign_recipients
		WHERE campaign_id = $1
		ORDER BY email
	`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Recipient
	for rows.Next() {
		var r Recipient
		if err := rows.Scan(&r.ProspectID, &r.Name, &r.Email, &r.SentAt, &r.Err); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// abandonedSendAge mirrors internal/newsletter's: a campaign stuck at
// "sending" for longer than any real send could take is treated as one
// whose process died, so it can be sent rather than being stuck forever.
const abandonedSendAge = 40 * time.Minute

// BeginSendCampaign validates and atomically claims a campaign for
// sending, exactly as internal/newsletter.BeginSend does and for the same
// reason: the slow, per-recipient network work must not run inside the
// HTTP request, but two clicks on Send must not produce two sends either.
//
// Returns the campaign and the recipients resolved at claim time.
func BeginSendCampaign(ctx context.Context, pool *pgxpool.Pool, m *mailer.Mailer, id, unitID string) (Campaign, []Recipient, error) {
	if !m.Enabled(ctx) {
		return Campaign{}, nil, ErrMailerDisabled
	}

	c, err := GetCampaign(ctx, pool, id, unitID)
	if err != nil {
		return Campaign{}, nil, err
	}
	if c.Status == "sent" {
		return Campaign{}, nil, ErrCampaignSent
	}
	if c.Status == "sending" && time.Since(c.UpdatedAt) < abandonedSendAge {
		return Campaign{}, nil, ErrCampaignSent
	}

	recipients, err := RecipientsForStatuses(ctx, pool, unitID, c.TargetStatuses)
	if err != nil {
		return Campaign{}, nil, err
	}
	if len(recipients) == 0 {
		return Campaign{}, nil, ErrNoCampaignRecipients
	}

	claimed, err := scanCampaign(pool.QueryRow(ctx, `
		UPDATE prospect_campaigns SET status = 'sending', updated_at = now()
		WHERE id = $1 AND unit_id = $2 AND status != 'sent'
		RETURNING `+campaignColumns, id, unitID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, nil, ErrCampaignSent
	}
	if err != nil {
		return Campaign{}, nil, err
	}
	return claimed, recipients, nil
}

// PersonalizeFunc turns the campaign body into the exact HTML one
// recipient receives — where the unsubscribe link is spliced in.
//
// Passed in rather than built here because the link needs a signing
// secret and the site's own URL, neither of which belongs in a
// business-logic package. See internal/web/prospect_campaigns.go.
type PersonalizeFunc func(body string, r Recipient) string

// SendCampaign does the per-recipient sending and finalizes the campaign.
//
// ctx should outlive the HTTP request that triggered it (see
// internal/newsletter.SendNow for the same arrangement). Every recipient
// gets a row in prospect_campaign_recipients whether or not the send
// succeeded, because "we tried and it bounced" and "we never wrote to
// them" are different answers to give a family who says they heard
// nothing.
func SendCampaign(ctx context.Context, pool *pgxpool.Pool, m *mailer.Mailer, c Campaign, recipients []Recipient, personalize PersonalizeFunc, actorID string) (sent, failed int) {
	// personalize is what adds the unsubscribe link to each copy. A nil
	// one would send a bulk mailing to members of the public with no way
	// out of it, which is the one thing this feature must never do — so
	// it is treated as the programming error it is: nothing is sent, and
	// the campaign goes back to being a draft rather than being stranded
	// at "sending" with no record of why.
	if personalize == nil {
		log.Printf("prospect: refusing to send campaign %s with no unsubscribe link", c.ID)
		if _, err := pool.Exec(ctx,
			`UPDATE prospect_campaigns SET status = 'draft', updated_at = now() WHERE id = $1 AND status = 'sending'`,
			c.ID); err != nil {
			log.Printf("prospect: returning campaign %s to draft: %v", c.ID, err)
		}
		return 0, 0
	}

	for _, r := range recipients {
		body := personalize(c.Body, r)

		var sendErr string
		if err := m.SendHTML(ctx, r.Email, c.Subject, body); err != nil {
			log.Printf("prospect: sending campaign %s to %s: %v", c.ID, r.Email, err)
			sendErr = truncateError(err.Error())
			failed++
		} else {
			sent++
		}

		var prospectID *string
		if r.ProspectID != "" {
			id := r.ProspectID
			prospectID = &id
		}
		var sentAt *time.Time
		if sendErr == "" {
			now := time.Now()
			sentAt = &now
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO prospect_campaign_recipients (campaign_id, prospect_id, email, name, sent_at, error)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (campaign_id, email)
			DO UPDATE SET sent_at = EXCLUDED.sent_at, error = EXCLUDED.error
		`, c.ID, prospectID, r.Email, r.Name, sentAt, sendErr); err != nil {
			log.Printf("prospect: recording campaign %s recipient %s: %v", c.ID, r.Email, err)
		}
	}

	if _, err := pool.Exec(ctx, `
		UPDATE prospect_campaigns
		SET status = 'sent', sent_at = now(), recipient_count = $1, updated_at = now()
		WHERE id = $2
	`, sent, c.ID); err != nil {
		log.Printf("prospect: WARNING campaign %s sent to %d recipients but failed to record it: %v", c.ID, sent, err)
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "prospect_campaign", EntityID: c.ID, ActorID: &actorID, Action: "send",
		After: map[string]any{"sent": sent, "failed": failed, "recipients": len(recipients)},
	})
	return sent, failed
}

// truncateError keeps a delivery failure short enough to sit in a table
// cell without losing the part that says what went wrong.
func truncateError(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// filterStatuses drops anything that isn't a real status, so a
// hand-crafted form post can't widen a campaign's audience past what the
// checkboxes offer.
func filterStatuses(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		if IsStatus(v) && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
