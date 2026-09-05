package calendar

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
	"github.com/47-yonkers/scout-site/internal/icalendar"
)

// Imported events that clash with the unit's own.
//
// A council calendar and a unit calendar overlap: the district camporee
// is on both, typed by hand here in January and appearing again in the
// council's feed in February. Before this, both simply arrived and the
// duplicate was somebody's problem to notice — usually a family, looking
// at two entries for the same weekend with slightly different times and
// wondering which one is right.
//
// So an incoming event that clashes with something already on the
// calendar is held here instead of being imported, and the calendar stays
// as it was until a leader rules on it. Three rulings are possible, and
// they are genuinely different: keep both (the overlap is a coincidence),
// keep ours (the feed's copy is wrong or redundant), or take theirs (ours
// was a placeholder). Guessing between them is exactly what this feature
// exists not to do.

// conflictSlack widens the overlap test at both ends.
//
// Two events for the same outing rarely carry identical times — one says
// 9am because that's when to arrive, the other 9:30 because that's when
// it starts. Treating those as unrelated would let the duplicate through,
// which is the failure this is for. Half an hour is wide enough to catch
// that and narrow enough that two genuinely different Saturday events
// don't trip it.
const conflictSlack = 30 * time.Minute

// assumedDuration is how long an event with no end time is treated as
// lasting, for the overlap test only. Nothing is written with it.
const assumedDuration = time.Hour

// Conflict is one held-back import and what it clashes with.
type Conflict struct {
	ID          string
	FeedID      string
	FeedName    string
	UnitID      string
	ExternalUID string

	// The incoming event, as the source offered it.
	Title       string
	Description string
	Location    string
	StartsAt    time.Time
	EndsAt      *time.Time

	// The event already on this unit's calendar that it clashes with.
	ExistingID       string
	ExistingTitle    string
	ExistingStartsAt time.Time
	ExistingEndsAt   *time.Time
	// ExistingIsImported distinguishes "clashes with something a leader
	// typed" from "clashes with something another feed brought in", which
	// changes which ruling makes sense.
	ExistingIsImported bool

	DetectedAt time.Time
}

// ErrConflictNotFound covers both "no such conflict" and "not this
// unit's".
var ErrConflictNotFound = errors.New("calendar: no such import conflict in this unit")

const conflictColumns = `c.id::text, c.feed_id::text, f.name, c.unit_id::text, c.external_uid,
	c.title, c.description, c.location, c.starts_at, c.ends_at,
	c.existing_event_id::text, e.title, e.starts_at, e.ends_at, (e.feed_id IS NOT NULL),
	c.detected_at`

func scanConflict(row interface{ Scan(...any) error }) (Conflict, error) {
	var c Conflict
	err := row.Scan(&c.ID, &c.FeedID, &c.FeedName, &c.UnitID, &c.ExternalUID,
		&c.Title, &c.Description, &c.Location, &c.StartsAt, &c.EndsAt,
		&c.ExistingID, &c.ExistingTitle, &c.ExistingStartsAt, &c.ExistingEndsAt, &c.ExistingIsImported,
		&c.DetectedAt)
	return c, err
}

const conflictFrom = `
	FROM calendar_feed_conflicts c
	JOIN calendar_feeds f ON f.id = c.feed_id
	JOIN events e ON e.id = c.existing_event_id`

// ConflictsForUnit lists everything waiting on a decision, oldest first —
// the order they need dealing with.
func ConflictsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Conflict, error) {
	rows, err := pool.Query(ctx,
		`SELECT `+conflictColumns+conflictFrom+` WHERE c.unit_id = $1 ORDER BY c.detected_at, c.starts_at`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Conflict
	for rows.Next() {
		c, err := scanConflict(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountConflictsForUnit is how many decisions are outstanding, for the
// badge on the calendar-feeds admin page.
func CountConflictsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM calendar_feed_conflicts WHERE unit_id = $1`, unitID).Scan(&n)
	return n, err
}

// GetConflict loads one, scoped to its unit.
func GetConflict(ctx context.Context, pool *pgxpool.Pool, id, unitID string) (Conflict, error) {
	c, err := scanConflict(pool.QueryRow(ctx,
		`SELECT `+conflictColumns+conflictFrom+` WHERE c.id = $1 AND c.unit_id = $2`, id, unitID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Conflict{}, ErrConflictNotFound
	}
	return c, err
}

// Resolution is what a leader decided about one conflict.
type Resolution string

const (
	// ResolveImport keeps both: the overlap was a coincidence, or the two
	// entries are genuinely different things at the same time.
	ResolveImport Resolution = "import"
	// ResolveSkip keeps ours and refuses theirs, permanently — recorded
	// in calendar_feed_ignores so the next refresh doesn't ask again.
	ResolveSkip Resolution = "skip"
	// ResolveReplace takes theirs: the existing event is deleted and the
	// incoming one imported in its place, for when ours was a placeholder
	// and the feed is now authoritative.
	ResolveReplace Resolution = "replace"
)

// ValidResolution reports whether v is one of the three decisions — the
// server-side check behind the buttons, since a form value can be
// anything.
func ValidResolution(v string) bool {
	switch Resolution(v) {
	case ResolveImport, ResolveSkip, ResolveReplace:
		return true
	}
	return false
}

// ResolveConflict carries out a leader's decision.
//
// Done in one transaction because two of the three rulings are two
// changes that must not half-happen: "replace" deletes an event and
// creates another, and a crash between them would lose the unit's own
// event and gain nothing. The conflict row is removed in the same
// transaction, so a decision is never recorded twice.
func ResolveConflict(ctx context.Context, pool *pgxpool.Pool, id, unitID string, decision Resolution, actorID string) error {
	c, err := GetConflict(ctx, pool, id, unitID)
	if err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The feed's visibility decides the imported event's, same as an
	// ordinary import — read inside the transaction so a feed deleted
	// mid-decision fails the whole thing rather than importing with a
	// guessed value.
	var visibility, createdBy string
	if decision == ResolveImport || decision == ResolveReplace {
		if err := tx.QueryRow(ctx,
			`SELECT visibility::text, created_by::text FROM calendar_feeds WHERE id = $1 AND unit_id = $2`,
			c.FeedID, unitID).Scan(&visibility, &createdBy); err != nil {
			return err
		}
	}

	switch decision {
	case ResolveSkip:
		if _, err := tx.Exec(ctx, `
			INSERT INTO calendar_feed_ignores (feed_id, external_uid, decided_by)
			VALUES ($1, $2, $3)
			ON CONFLICT (feed_id, external_uid) DO UPDATE SET decided_by = EXCLUDED.decided_by, decided_at = now()
		`, c.FeedID, c.ExternalUID, actorID); err != nil {
			return err
		}

	case ResolveReplace:
		// Deleting the existing event cascades this conflict row away
		// (calendar_feed_conflicts.existing_event_id is ON DELETE
		// CASCADE), which is why the delete below is harmless rather
		// than redundant.
		if _, err := tx.Exec(ctx,
			`DELETE FROM events WHERE id = $1 AND unit_id = $2`, c.ExistingID, unitID); err != nil {
			return err
		}
		fallthrough

	case ResolveImport:
		if _, err := tx.Exec(ctx, `
			INSERT INTO events (unit_id, title, description, location, starts_at, ends_at,
			                    visibility, status, created_by, feed_id, external_uid)
			VALUES ($1, $2, $3, $4, $5, $6, $7::visibility, 'published', $8, $9, $10)
			ON CONFLICT (feed_id, external_uid) WHERE feed_id IS NOT NULL DO NOTHING
		`, unitID, c.Title, c.Description, c.Location, c.StartsAt, c.EndsAt,
			visibility, createdBy, c.FeedID, c.ExternalUID); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM calendar_feed_conflicts WHERE id = $1 AND unit_id = $2`, id, unitID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "calendar_feed", EntityID: c.FeedID, ActorID: &actorID,
		Action: "resolve_conflict",
		After: map[string]any{
			"decision": string(decision),
			"incoming": c.Title,
			"clashed_with": map[string]any{
				"title": c.ExistingTitle,
				"id":    c.ExistingID,
			},
		},
	})
	return nil
}

// findConflict looks for an event already on this unit's calendar that
// the incoming one clashes with.
//
// Events belonging to the same feed are excluded: those are matched by
// external_uid and updated in place, so an event overlapping its own
// previous copy is not a conflict. Events from a *different* feed are
// included — two councils both publishing the same camporee is exactly
// the duplicate a family would notice.
//
// Returns "" when there is nothing to hold the import back for.
func findConflict(ctx context.Context, tx queryer, unitID, feedID string, ev icalendar.Event) (string, error) {
	end := ev.End
	if end.IsZero() {
		end = ev.Start.Add(assumedDuration)
	}

	var id string
	err := tx.QueryRow(ctx, `
		SELECT id FROM events
		WHERE unit_id = $1
		  AND (feed_id IS NULL OR feed_id <> $2)
		  AND starts_at < $3
		  AND COALESCE(ends_at, starts_at + $6) > $4
		  AND id NOT IN (SELECT existing_event_id FROM calendar_feed_conflicts WHERE feed_id = $2 AND external_uid <> $5)
		ORDER BY starts_at
		LIMIT 1
	`, unitID, feedID, end.Add(conflictSlack), ev.Start.Add(-conflictSlack), ev.UID, assumedDuration).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// queryer is the little bit of pgx both a pool and a transaction satisfy,
// so findConflict can be called from either.
type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// recordConflict holds an incoming event back for a decision.
//
// Upsert on (feed_id, external_uid) so a refresh every hour updates the
// pending conflict rather than stacking twenty copies of it by morning.
func recordConflict(ctx context.Context, pool *pgxpool.Pool, unitID, feedID, existingID string, ev icalendar.Event, title string) error {
	var endsAt *time.Time
	if !ev.End.IsZero() {
		e := ev.End
		endsAt = &e
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO calendar_feed_conflicts
			(feed_id, unit_id, external_uid, title, description, location, starts_at, ends_at, existing_event_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (feed_id, external_uid) DO UPDATE SET
			title = EXCLUDED.title,
			description = EXCLUDED.description,
			location = EXCLUDED.location,
			starts_at = EXCLUDED.starts_at,
			ends_at = EXCLUDED.ends_at,
			existing_event_id = EXCLUDED.existing_event_id,
			detected_at = now()
	`, feedID, unitID, ev.UID, title, ev.Description, ev.Location, ev.Start, endsAt, existingID)
	return err
}

// ignoredUIDs is the set of events this feed has been told to stop
// offering. Loaded once per refresh rather than queried per event.
func ignoredUIDs(ctx context.Context, pool *pgxpool.Pool, feedID string) (map[string]bool, error) {
	rows, err := pool.Query(ctx,
		`SELECT external_uid FROM calendar_feed_ignores WHERE feed_id = $1`, feedID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out[uid] = true
	}
	return out, rows.Err()
}
