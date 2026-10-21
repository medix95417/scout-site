// Package reminders emails everyone who RSVP'd yes/maybe to an event that's
// coming up soon. It's meant to be run periodically (e.g. hourly, via cron)
// as a one-off CLI invocation — see cmd/server's -send-event-reminders flag
// and DEPLOY.md — not as an in-process scheduler, so there's no goroutine
// lifecycle to manage and no risk of double-sending across multiple app
// replicas. Idempotency instead comes from the event_reminders_sent table:
// a reminder is only ever recorded (and therefore only ever sent) once per
// (event, member) pair, so re-running the job on a schedule is always safe.
package reminders

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/mailer"
)

// candidate is one (event, RSVP'd member) pair that's due a reminder.
type candidate struct {
	EventID     string
	UnitID      string
	Title       string
	Location    string
	StartsAt    time.Time
	EndsAt      *time.Time
	UnitName    string
	UnitHost    string
	MemberID    string
	MemberFirst string
	Response    string
	Email       string
}

// Result summarizes one run, for the CLI to report.
type Result struct {
	Sent   int
	Failed int
}

// Send finds events starting within `window` from now that haven't had a
// reminder sent for a given RSVP'd member yet, emails each of them, and
// records the send so it isn't repeated on the next run. A failed send is
// logged and skipped (not recorded), so it's naturally retried on the next
// invocation — a transient SMTP outage shouldn't cost anyone their
// reminder.
func Send(ctx context.Context, pool *pgxpool.Pool, m *mailer.Mailer, window time.Duration) (Result, error) {
	var res Result

	candidates, err := dueCandidates(ctx, pool, window)
	if err != nil {
		return res, fmt.Errorf("reminders: finding due reminders: %w", err)
	}

	// Each reminder is sent as the unit its event belongs to, so whether mail
	// can go out is a per-unit question too — a site whose only configured From
	// addresses are per unit is configured for those units. Asked once per
	// unit, not once per reminder.
	notConfigured := errors.New("reminders: mailer not configured — set SMTP_HOST etc. to enable email")
	if len(candidates) == 0 && !m.Enabled(ctx) {
		return res, notConfigured
	}
	canSend := map[string]bool{}
	skipped := 0

	for _, c := range candidates {
		unitCtx := mailer.WithUnit(ctx, c.UnitID)
		ok, asked := canSend[c.UnitID]
		if !asked {
			ok = m.Enabled(unitCtx)
			canSend[c.UnitID] = ok
		}
		if !ok {
			// Not recorded as sent, so it goes out on the first run after mail
			// is set up for this unit.
			skipped++
			continue
		}

		body := fmt.Sprintf(
			"Hi %s,\n\n"+
				"This is a reminder that you're signed up (%s) for an upcoming %s event:\n\n"+
				"  %s\n"+
				"  %s%s\n\n"+
				"View or update your RSVP: https://%s/calendar\n\n"+
				"— %s",
			c.MemberFirst, c.Response, c.UnitName,
			c.Title,
			calendar.FormatDateRange(c.StartsAt, c.EndsAt), locationSuffix(c.Location),
			c.UnitHost, c.UnitName,
		)
		subject := fmt.Sprintf("Reminder: %s — %s", c.Title, c.StartsAt.Format("Mon Jan 2"))

		if err := m.Send(unitCtx, c.Email, subject, body); err != nil {
			log.Printf("reminders: sending to %s for event %s: %v", c.Email, c.EventID, err)
			res.Failed++
			continue
		}

		if _, err := pool.Exec(ctx, `
			INSERT INTO event_reminders_sent (event_id, member_id) VALUES ($1, $2)
			ON CONFLICT (event_id, member_id) DO NOTHING
		`, c.EventID, c.MemberID); err != nil {
			// The email already went out — log this loudly since a failure
			// here risks a duplicate send next run, but don't treat it as a
			// reason to stop processing the rest of the batch.
			log.Printf("reminders: WARNING sent to %s but failed to record it (may re-send next run): %v", c.Email, err)
		}
		res.Sent++
	}

	if skipped > 0 {
		log.Printf("reminders: %d reminder(s) not sent because email isn't configured for their unit — they will go out once it is", skipped)
		if res.Sent == 0 {
			return res, notConfigured
		}
	}
	return res, nil
}

func locationSuffix(location string) string {
	if location == "" {
		return ""
	}
	return " · " + location
}

func dueCandidates(ctx context.Context, pool *pgxpool.Pool, window time.Duration) ([]candidate, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			events.id, events.unit_id::text, events.title, COALESCE(events.location, ''), events.starts_at, events.ends_at,
			units.name, units.hostname,
			members.id, members.first_name,
			rsvps.response::text,
			users.email
		FROM events
		JOIN units ON units.id = events.unit_id
		JOIN rsvps ON rsvps.event_id = events.id AND rsvps.response IN ('yes', 'maybe')
		JOIN members ON members.id = rsvps.member_id
		JOIN users ON users.family_id = members.family_id
		LEFT JOIN event_reminders_sent
			ON event_reminders_sent.event_id = events.id AND event_reminders_sent.member_id = members.id
		WHERE events.status = 'published'
			AND events.starts_at BETWEEN now() AND now() + make_interval(secs => $1)
			AND event_reminders_sent.event_id IS NULL
		ORDER BY events.starts_at
	`, window.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(
			&c.EventID, &c.UnitID, &c.Title, &c.Location, &c.StartsAt, &c.EndsAt,
			&c.UnitName, &c.UnitHost,
			&c.MemberID, &c.MemberFirst,
			&c.Response,
			&c.Email,
		); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}
