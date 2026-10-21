package newsletter

// Emailing something that is not a newsletter: a news post, or an update to a
// den or patrol.
//
// The sending is the newsletter's sending — claim the record, send each
// address on its own, paced, record every one, finish with an audit entry —
// so it is the same code (SendNow) and leaves the same record. What differs
// is only how a record starts (a post's text rather than a draft someone
// edits) and who it is addressed to.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
	"github.com/47-yonkers/scout-site/internal/mailer"
)

var (
	// ErrNoAudience is a group update addressed to no group. A news post may
	// go to the whole unit; a message to "a den or patrol" has to name one,
	// and defaulting an empty choice to everybody is how a note meant for one
	// patrol reaches every family.
	ErrNoAudience = errors.New("newsletter: no den or patrol was chosen")

	// ErrBadAudience is a group that is not this unit's, or is no longer
	// running. Both read the same to the person: that isn't somewhere you can
	// send to.
	ErrBadAudience = errors.New("newsletter: a chosen den or patrol is not one of this unit's")
)

// PostEmail is one email made from a post, or from a message written for a
// den or patrol.
type PostEmail struct {
	UnitID string
	// Kind is KindNews or KindGroupUpdate.
	Kind string
	// PostID is the post it was made from; nil for a message that was only
	// emailed and never posted.
	PostID  *string
	Subject string
	// Body is HTML. It is sanitized again here, whatever produced it: what is
	// stored, and sent, is what Sanitize allows and nothing a caller built.
	Body    string
	ActorID string
	// Groups are the dens/patrols it is addressed to. Empty means the whole
	// unit, which only a news post may mean; see ErrNoAudience.
	Groups []string
}

// StartPostEmail checks that an email can go out and starts it: mail is
// configured, the audience is real, it reaches somebody, and then a record is
// written — already claimed ("sending"), in one step.
//
// The record is created in the state a send has just claimed rather than as a
// draft that is then claimed, because a draft that nothing will ever send is
// what is left behind if the second step fails or the process stops between
// them, and a list of those would read as unfinished business. Every refusal
// below happens before anything is written.
//
// It does none of the sending. Run SendNow on the result, detached from the
// request, exactly as a newsletter is.
func StartPostEmail(ctx context.Context, pool *pgxpool.Pool, m *mailer.Mailer, in PostEmail) (Newsletter, error) {
	if !m.Enabled(mailer.WithUnit(ctx, in.UnitID)) {
		return Newsletter{}, fmt.Errorf("newsletter: mailer not configured — set SMTP_HOST etc. to enable email")
	}

	audience := AudienceUnit
	switch in.Kind {
	case KindNews:
		if len(in.Groups) > 0 {
			audience = AudienceGroups
		}
	case KindGroupUpdate:
		if len(in.Groups) == 0 {
			return Newsletter{}, ErrNoAudience
		}
		audience = AudienceGroups
	default:
		return Newsletter{}, fmt.Errorf("newsletter: %q is not a kind of email that is made from a post", in.Kind)
	}

	// Distinct, and every one this unit's own and still running.
	groups := distinct(in.Groups)
	if audience == AudienceGroups {
		var found int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM sub_groups
			WHERE unit_id = $1 AND active AND id = ANY($2::uuid[])
		`, in.UnitID, groups).Scan(&found); err != nil {
			return Newsletter{}, err
		}
		if found != len(groups) {
			return Newsletter{}, ErrBadAudience
		}
	}

	var emails []string
	var err error
	if audience == AudienceGroups {
		emails, err = RecipientEmailsForGroups(ctx, pool, in.UnitID, groups)
	} else {
		emails, err = RecipientEmailsForUnit(ctx, pool, in.UnitID)
	}
	if err != nil {
		return Newsletter{}, fmt.Errorf("newsletter: resolving recipients: %w", err)
	}
	if len(emails) == 0 {
		return Newsletter{}, ErrNoRecipients
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Newsletter{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op once committed

	n, err := scan(tx.QueryRow(ctx, `
		INSERT INTO newsletters (unit_id, subject, body, status, created_by, kind, post_id, audience)
		VALUES ($1, $2, $3, 'sending', $4, $5, $6, $7)
		RETURNING `+columns,
		in.UnitID, in.Subject, Sanitize(in.Body), in.ActorID, in.Kind, in.PostID, audience))
	if err != nil {
		return Newsletter{}, err
	}
	for _, g := range groups {
		if audience != AudienceGroups {
			break
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO newsletter_audience_groups (newsletter_id, sub_group_id) VALUES ($1, $2)`, n.ID, g); err != nil {
			return Newsletter{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Newsletter{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "newsletter",
		EntityID:   n.ID,
		ActorID:    &in.ActorID,
		Action:     "create",
		After:      map[string]any{"kind": n.Kind, "subject": n.Subject, "audience": n.Audience, "groups": groups, "recipients": len(emails)},
	})
	return n, nil
}

// RecipientEmailsFor resolves who a record is addressed to.
//
// A record addressed to groups whose group rows have since gone reaches
// nobody — it never falls back to the whole unit. That is what the audience
// column is for (see migration 0053): "no rows" must not be able to mean
// "everyone".
func RecipientEmailsFor(ctx context.Context, pool *pgxpool.Pool, n Newsletter) ([]string, error) {
	if n.Audience != AudienceGroups {
		return RecipientEmailsForUnit(ctx, pool, n.UnitID)
	}
	groups, err := GroupIDsFor(ctx, pool, n.ID)
	if err != nil {
		return nil, err
	}
	return RecipientEmailsForGroups(ctx, pool, n.UnitID, groups)
}

// RecipientEmailsForGroups returns the distinct login addresses for the
// families of the dens/patrols in groups: every family with an active member
// who holds a role in one of them.
//
// Which of a family's logins is a narrower question than for the whole unit,
// and is answered deliberately:
//
//   - The family-wide login is always included. It is the household's address.
//   - An adult's own login is included. They are a parent or guardian of the
//     household whether or not they hold a role in this particular group.
//   - A youth's own login is included only if that youth is in one of these
//     groups themselves. Otherwise a message to the Eagle Patrol would land in
//     the own inbox of a younger brother in the Wolf Patrol — a family-level
//     reach is right for the household's address and wrong for a child's own,
//     the same split the roster draws everywhere else ("an individual login
//     sees just their own stuff").
//
// Only active members count, and only groups that are still running.
func RecipientEmailsForGroups(ctx context.Context, pool *pgxpool.Pool, unitID string, groups []string) ([]string, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT u.email
		FROM users u
		LEFT JOIN members um ON um.id = u.member_id
		WHERE u.family_id IN (
				SELECT m.family_id
				FROM role_assignments ra
				JOIN members m ON m.id = ra.member_id
				JOIN sub_groups sg ON sg.id = ra.sub_group_id
				WHERE ra.unit_id = $1 AND sg.unit_id = $1 AND sg.active
				  AND ra.sub_group_id = ANY($2::uuid[]) AND m.active
			)
		  AND (
				u.member_id IS NULL
			 OR (um.active AND (
					um.member_type = 'adult'
				 OR u.member_id IN (
						SELECT ra2.member_id
						FROM role_assignments ra2
						JOIN sub_groups sg2 ON sg2.id = ra2.sub_group_id
						WHERE ra2.unit_id = $1 AND sg2.unit_id = $1 AND sg2.active
						  AND ra2.sub_group_id = ANY($2::uuid[])
					)
				))
			)
		ORDER BY u.email
	`, unitID, groups)
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

// GroupIDsFor lists the dens/patrols a record is addressed to.
func GroupIDsFor(ctx context.Context, pool *pgxpool.Pool, newsletterID string) ([]string, error) {
	rows, err := pool.Query(ctx,
		`SELECT sub_group_id::text FROM newsletter_audience_groups WHERE newsletter_id = $1 ORDER BY sub_group_id`, newsletterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GroupNamesFor names the dens/patrols a record is addressed to, for showing
// "sent to Eagle Patrol and Wolf Patrol" rather than a count.
func GroupNamesFor(ctx context.Context, pool *pgxpool.Pool, newsletterID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT sub_groups.name
		FROM newsletter_audience_groups
		JOIN sub_groups ON sub_groups.id = newsletter_audience_groups.sub_group_id
		WHERE newsletter_audience_groups.newsletter_id = $1
		ORDER BY sub_groups.name
	`, newsletterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// ListForPost lists the emails that have been made from one post, newest
// first — what lets the page say "you emailed this on Tuesday" before
// someone sends it a second time.
func ListForPost(ctx context.Context, pool *pgxpool.Pool, unitID, postID string) ([]Newsletter, error) {
	return list(ctx, pool, `
		SELECT `+columns+` FROM newsletters
		WHERE unit_id = $1 AND post_id = $2
		ORDER BY created_at DESC
	`, unitID, postID)
}

// ListForGroup lists the emails that were addressed to a den or patrol,
// newest first, up to limit — what a group's own page shows its leaders of
// what has already gone out.
func ListForGroup(ctx context.Context, pool *pgxpool.Pool, unitID, groupID string, limit int) ([]Newsletter, error) {
	return list(ctx, pool, `
		SELECT `+columns+` FROM newsletters
		WHERE unit_id = $1
		  AND audience = 'groups'
		  AND id IN (SELECT newsletter_id FROM newsletter_audience_groups WHERE sub_group_id = $2)
		ORDER BY created_at DESC
		LIMIT $3
	`, unitID, groupID, limit)
}

func list(ctx context.Context, pool *pgxpool.Pool, query string, args ...any) ([]Newsletter, error) {
	rows, err := pool.Query(ctx, query, args...)
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

func distinct(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
