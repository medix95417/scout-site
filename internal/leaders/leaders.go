// Package leaders manages the public "Our Leaders" page — a simple,
// admin-maintained listing of a unit's adult leaders: name, role title, a
// brief bio, and an optional photo. See migration 0030_leaders.sql.
package leaders

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

// Leader is one entry on the "Our Leaders" page.
type Leader struct {
	ID         string
	UnitID     string
	Name       string
	RoleTitle  string
	Bio        string
	PhotoURL   string
	PhotoFocus string // PhotoFocusTop/Center/Bottom — see NormalizePhotoFocus
	SortOrder  int
	Status     string // "draft" or "published"
	CreatedBy  *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// PhotoFocus presets control which part of a leader's photo stays
// visible when its aspect ratio doesn't match the fixed-height card it
// displays in (see leaders.html) — object-cover crops whatever
// overflows, and a portrait headshot in a wide card can lose the top of
// someone's head or their chin to the default center crop with no way
// to fix it otherwise.
const (
	PhotoFocusTop    = "top"
	PhotoFocusCenter = "center" // default — today's original behavior
	PhotoFocusBottom = "bottom"
)

// NormalizePhotoFocus maps anything other than a recognized preset —
// most commonly "", since older leader rows predate this column being
// set — back to PhotoFocusCenter, so a blank/bad value can't produce a
// broken CSS class in a template.
func NormalizePhotoFocus(focus string) string {
	switch focus {
	case PhotoFocusTop, PhotoFocusBottom:
		return focus
	default:
		return PhotoFocusCenter
	}
}

const selectColumns = `id, unit_id, name, role_title, bio, photo_url, photo_focus, sort_order, status::text, created_by, created_at, updated_at`

func scanLeader(row interface{ Scan(...any) error }) (Leader, error) {
	var l Leader
	err := row.Scan(&l.ID, &l.UnitID, &l.Name, &l.RoleTitle, &l.Bio, &l.PhotoURL, &l.PhotoFocus, &l.SortOrder, &l.Status, &l.CreatedBy, &l.CreatedAt, &l.UpdatedAt)
	return l, err
}

// Create adds a new leader profile as a draft — an admin publishes it
// explicitly via SetPublished once it's ready, so a half-written profile
// is never visible on the public page in between.
func Create(ctx context.Context, pool *pgxpool.Pool, unitID, name, roleTitle, bio, photoURL, photoFocus, actorID string) (Leader, error) {
	l, err := scanLeader(pool.QueryRow(ctx, `
		INSERT INTO leaders (unit_id, name, role_title, bio, photo_url, photo_focus, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+selectColumns,
		unitID, name, roleTitle, bio, photoURL, NormalizePhotoFocus(photoFocus), actorID))
	if err != nil {
		return Leader{}, err
	}

	audit.Log(ctx, pool, audit.Entry{EntityType: "leader", EntityID: l.ID, ActorID: &actorID, Action: "create", After: l})
	return l, nil
}

// Update edits a leader's name, role title, bio, photo, photo focus, and
// sort order. Status (draft/published) is untouched — see SetPublished
// for that.
func Update(ctx context.Context, pool *pgxpool.Pool, id, unitID, name, roleTitle, bio, photoURL, photoFocus string, sortOrder int, actorID string) (Leader, error) {
	before, _, err := Get(ctx, pool, id, unitID)
	if err != nil {
		return Leader{}, err
	}

	l, err := scanLeader(pool.QueryRow(ctx, `
		UPDATE leaders SET name = $1, role_title = $2, bio = $3, photo_url = $4, photo_focus = $5, sort_order = $6, updated_at = now()
		WHERE id = $7 AND unit_id = $8
		RETURNING `+selectColumns,
		name, roleTitle, bio, photoURL, NormalizePhotoFocus(photoFocus), sortOrder, id, unitID))
	if err != nil {
		return Leader{}, err
	}

	audit.Log(ctx, pool, audit.Entry{EntityType: "leader", EntityID: l.ID, ActorID: &actorID, Action: "update", Before: before, After: l})
	return l, nil
}

// SetPublished flips a leader profile between draft and published.
func SetPublished(ctx context.Context, pool *pgxpool.Pool, id, unitID string, publish bool, actorID string) error {
	status, action := "draft", "unpublish"
	if publish {
		status, action = "published", "publish"
	}

	tag, err := pool.Exec(ctx, `UPDATE leaders SET status = $1, updated_at = now() WHERE id = $2 AND unit_id = $3`, status, id, unitID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("leaders: leader %s not found in unit %s", id, unitID)
	}

	audit.Log(ctx, pool, audit.Entry{EntityType: "leader", EntityID: id, ActorID: &actorID, Action: action})
	return nil
}

// Delete removes a leader profile entirely — unlike posts/galleries,
// there's no reason to keep a departed leader's entry around as a
// permanently-hidden draft.
func Delete(ctx context.Context, pool *pgxpool.Pool, id, unitID, actorID string) error {
	before, found, err := Get(ctx, pool, id, unitID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	if _, err := pool.Exec(ctx, `DELETE FROM leaders WHERE id = $1 AND unit_id = $2`, id, unitID); err != nil {
		return err
	}

	audit.Log(ctx, pool, audit.Entry{EntityType: "leader", EntityID: id, ActorID: &actorID, Action: "delete", Before: before})
	return nil
}

// Get fetches one leader profile by ID, scoped to a unit.
func Get(ctx context.Context, pool *pgxpool.Pool, id, unitID string) (Leader, bool, error) {
	l, err := scanLeader(pool.QueryRow(ctx, `SELECT `+selectColumns+` FROM leaders WHERE id = $1 AND unit_id = $2`, id, unitID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Leader{}, false, nil
		}
		return Leader{}, false, err
	}
	return l, true, nil
}

func list(ctx context.Context, pool *pgxpool.Pool, where string, unitID string) ([]Leader, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+selectColumns+`
		FROM leaders
		`+where+`
		ORDER BY sort_order, name
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Leader
	for rows.Next() {
		l, err := scanLeader(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListForUnit returns every leader profile for a unit, draft and published
// alike — what the admin management view shows.
func ListForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Leader, error) {
	return list(ctx, pool, `WHERE unit_id = $1`, unitID)
}

// ListPublishedForUnit returns only published leader profiles — what the
// public "Our Leaders" page shows.
func ListPublishedForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Leader, error) {
	return list(ctx, pool, `WHERE unit_id = $1 AND status = 'published'`, unitID)
}
