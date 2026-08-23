// Package files manages the file-metadata rows backing the site's general
// file library and event photo galleries (see migration 0012). Actual
// bytes live in internal/storage; this package owns the database side
// (which files exist, what they're linked to) and generates the storage
// key for each upload.
package files

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Categories mirror the file_category enum in migration 0012.
const (
	CategoryGeneral    = "general"
	CategoryEventPhoto = "event_photo"
)

type File struct {
	ID          string
	UnitID      string
	Filename    string
	DisplayName string // "" until a leader sets a friendlier label — see migration 0020 and DisplayLabel
	ContentType string
	SizeBytes   int64
	StorageKey  string
	Category    string
	UploadedBy  *string
	CreatedAt   time.Time
	Public      bool // see migration 0016 — whether FileDownload may serve this one without requiring login
}

// DisplayLabel is what to show for this file wherever it's listed or
// picked — the leader-set DisplayName if there is one, the original
// uploaded filename otherwise. Callers should always show this, never
// Filename directly (Filename remains what FileDownload's
// Content-Disposition header uses, so a saved copy keeps a sensible
// on-disk name regardless of the display label).
func (f File) DisplayLabel() string {
	if f.DisplayName != "" {
		return f.DisplayName
	}
	return f.Filename
}

// NewStorageKey generates a collision-proof object key for a new upload,
// namespaced by unit so two units' files never collide even though they
// share one bucket. The original filename is preserved as a suffix purely
// so a key is human-recognizable
// in the bucket browser — nothing parses it back out; the database row is
// the source of truth for Filename.
func NewStorageKey(unitID, filename string) string {
	return fmt.Sprintf("%s/%s-%s", unitID, uuid.NewString(), filename)
}

// Create inserts a file's metadata row after its bytes have already been
// written to storage (see internal/web/files.go's upload handler, which
// calls storage.Put first — a row with no matching object would be worse
// than an orphaned object with no row, since the latter is at least
// harmless).
func Create(ctx context.Context, pool *pgxpool.Pool, f File) (File, error) {
	err := pool.QueryRow(ctx, `
		INSERT INTO files (unit_id, filename, display_name, content_type, size_bytes, storage_key, category, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at
	`, f.UnitID, f.Filename, f.DisplayName, f.ContentType, f.SizeBytes, f.StorageKey, f.Category, f.UploadedBy,
	).Scan(&f.ID, &f.CreatedAt)
	if err != nil {
		return File{}, err
	}
	return f, nil
}

// CategorySummary is how many files, and how many total bytes, a unit has
// stored in one category — see StorageSummaryForUnit.
type CategorySummary struct {
	Category  string
	FileCount int
	SizeBytes int64
}

// StorageSummaryForUnit reports how much is stored for a unit, broken down
// by category (see CategorySummary) plus a grand total across all of
// them — what /admin/settings shows so a site admin can see what's using
// storage without having to browse the full file list themselves.
// Categories with zero files are omitted rather than shown as a zero row.
func StorageSummaryForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) (byCategory []CategorySummary, total CategorySummary, err error) {
	rows, err := pool.Query(ctx, `
		SELECT category::text, count(*), COALESCE(sum(size_bytes), 0)
		FROM files WHERE unit_id = $1 GROUP BY category ORDER BY category
	`, unitID)
	if err != nil {
		return nil, CategorySummary{}, err
	}
	defer rows.Close()

	for rows.Next() {
		var c CategorySummary
		if err := rows.Scan(&c.Category, &c.FileCount, &c.SizeBytes); err != nil {
			return nil, CategorySummary{}, err
		}
		byCategory = append(byCategory, c)
		total.FileCount += c.FileCount
		total.SizeBytes += c.SizeBytes
	}
	return byCategory, total, rows.Err()
}

// ListForUnit returns every file belonging to a unit, most recent first.
func ListForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]File, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, unit_id, filename, display_name, content_type, size_bytes, storage_key, category::text, uploaded_by, created_at, is_public
		FROM files WHERE unit_id = $1 ORDER BY created_at DESC
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.UnitID, &f.Filename, &f.DisplayName, &f.ContentType, &f.SizeBytes, &f.StorageKey, &f.Category, &f.UploadedBy, &f.CreatedAt, &f.Public); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Get looks up a single file, scoped to a unit — same "scope every lookup
// to the requester's unit" guard as calendar.GetEvent, so a file ID
// guessed/leaked from one unit can't be fetched or deleted through the
// other unit's admin pages.
func Get(ctx context.Context, pool *pgxpool.Pool, fileID, unitID string) (File, bool, error) {
	var f File
	err := pool.QueryRow(ctx, `
		SELECT id, unit_id, filename, display_name, content_type, size_bytes, storage_key, category::text, uploaded_by, created_at, is_public
		FROM files WHERE id = $1 AND unit_id = $2
	`, fileID, unitID).Scan(&f.ID, &f.UnitID, &f.Filename, &f.DisplayName, &f.ContentType, &f.SizeBytes, &f.StorageKey, &f.Category, &f.UploadedBy, &f.CreatedAt, &f.Public)
	if err != nil {
		return File{}, false, nil //nolint:nilerr // "no such file in this unit" is a normal, expected outcome
	}
	return f, true, nil
}

// SetPublic flips whether a file may be served by FileDownload without
// requiring login — see migration 0016's doc comment. Scoped to unitID like
// every other file write here.
func SetPublic(ctx context.Context, pool *pgxpool.Pool, fileID, unitID string, public bool) error {
	_, err := pool.Exec(ctx, `UPDATE files SET is_public = $1 WHERE id = $2 AND unit_id = $3`, public, fileID, unitID)
	return err
}

// SetDisplayName sets (or clears, with "") a file's friendlier label — see
// DisplayLabel. Scoped to unitID like every other file write here.
func SetDisplayName(ctx context.Context, pool *pgxpool.Pool, fileID, unitID, displayName string) error {
	_, err := pool.Exec(ctx, `UPDATE files SET display_name = $1 WHERE id = $2 AND unit_id = $3`, displayName, fileID, unitID)
	return err
}

// SetCategory reclassifies a file between the general document library and
// the event-photo category — chosen once at upload time (see
// internal/web/files.go's FileUpload), but sometimes wrong in hindsight
// (a document uploaded as "general" that turns out to be a campout photo,
// or the other way around) and otherwise stuck that way for good. Scoped
// to unitID like every other file write here.
func SetCategory(ctx context.Context, pool *pgxpool.Pool, fileID, unitID, category string) error {
	_, err := pool.Exec(ctx, `UPDATE files SET category = $1 WHERE id = $2 AND unit_id = $3`, category, fileID, unitID)
	return err
}

// PickerImage is a public image plus the calendar event(s) it's linked to
// (if any) — what the thumbnail picker (see internal/web/templates'
// "imagePicker" block) groups/sorts by, so a leader browsing photos to use
// as a hero banner or homepage photo can recognize "these came from the
// campout" instead of hunting through an undifferentiated list.
type PickerImage struct {
	File
	EventNames []string // empty means not linked to any event
}

// PrimaryEventName is the event PickerImage sorts by — the first (soonest-
// starting) event a photo is linked to, or "" if it isn't linked to any.
// A photo linked to several events is rare; sorting by just the first one
// is simpler than a full multi-group display for a niche case.
func (p PickerImage) PrimaryEventName() string {
	if len(p.EventNames) == 0 {
		return ""
	}
	return p.EventNames[0]
}

// ListPublicImagesForUnit lists a unit's public, image-content-type files,
// each decorated with the event(s) it's linked to — what the "choose from
// library" picker on /admin/home offers for the hero/program/gallery photo
// slots. Only ever returns what a leader has already explicitly marked
// public (SetPublic); never all of ListForUnit. Sorted by linked event
// (soonest-starting first, unlinked photos last), then by upload recency
// within each group.
func ListPublicImagesForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]PickerImage, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			f.id, f.unit_id, f.filename, f.display_name, f.content_type, f.size_bytes, f.storage_key, f.category::text, f.uploaded_by, f.created_at, f.is_public,
			COALESCE(array_agg(e.title ORDER BY e.starts_at) FILTER (WHERE e.title IS NOT NULL), '{}'),
			MIN(e.starts_at)
		FROM files f
		LEFT JOIN event_files ef ON ef.file_id = f.id
		LEFT JOIN events e ON e.id = ef.event_id
		WHERE f.unit_id = $1 AND f.is_public = true AND f.content_type LIKE 'image/%'
		GROUP BY f.id, f.unit_id, f.filename, f.display_name, f.content_type, f.size_bytes, f.storage_key, f.category, f.uploaded_by, f.created_at, f.is_public
		ORDER BY (MIN(e.starts_at) IS NULL), MIN(e.starts_at), f.created_at DESC
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PickerImage
	for rows.Next() {
		var p PickerImage
		var firstEventStart *time.Time
		if err := rows.Scan(&p.ID, &p.UnitID, &p.Filename, &p.DisplayName, &p.ContentType, &p.SizeBytes, &p.StorageKey, &p.Category, &p.UploadedBy, &p.CreatedAt, &p.Public, &p.EventNames, &firstEventStart); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// EventFileGroup is one event's linked files — see
// ListForUnitGroupedByEvents and ListEventPhotoGroupsForUnit, its
// images-only sibling.
type EventFileGroup struct {
	EventID    string
	EventTitle string
	Files      []File
}

// ListForUnitGroupedByEvents lists every file linked to any of eventIDs,
// grouped by which event(s) it's linked to (a file linked to more than
// one appears once per group) — what /files shows instead of its
// ordinary flat list once a leader has filtered to specific events, so
// files from the same campout read together instead of scattered through
// upload order. Groups come back ordered by the event's start date, most
// recent first; eventIDs not actually in this unit (or with no linked
// files) simply produce no group.
func ListForUnitGroupedByEvents(ctx context.Context, pool *pgxpool.Pool, unitID string, eventIDs []string) ([]EventFileGroup, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT e.id, e.title, f.id, f.unit_id, f.filename, f.display_name, f.content_type, f.size_bytes, f.storage_key, f.category::text, f.uploaded_by, f.created_at, f.is_public
		FROM events e
		JOIN event_files ef ON ef.event_id = e.id
		JOIN files f ON f.id = ef.file_id
		WHERE e.unit_id = $1 AND e.id = ANY($2)
		ORDER BY e.starts_at DESC, f.created_at DESC
	`, unitID, eventIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []EventFileGroup
	index := map[string]int{}
	for rows.Next() {
		var eventID, eventTitle string
		var f File
		if err := rows.Scan(&eventID, &eventTitle, &f.ID, &f.UnitID, &f.Filename, &f.DisplayName, &f.ContentType, &f.SizeBytes, &f.StorageKey, &f.Category, &f.UploadedBy, &f.CreatedAt, &f.Public); err != nil {
			return nil, err
		}
		i, ok := index[eventID]
		if !ok {
			i = len(groups)
			index[eventID] = i
			groups = append(groups, EventFileGroup{EventID: eventID, EventTitle: eventTitle})
		}
		groups[i].Files = append(groups[i].Files, f)
	}
	return groups, rows.Err()
}

// ListEventPhotoGroupsForUnit is ListForUnitGroupedByEvents' image-only
// sibling, covering every event in the unit rather than a caller-chosen
// subset — what the Gallery editor's "add an event's photos" picker
// offers, so a leader building a members-only album can pull in a
// private event's photos directly instead of copying each download link
// by hand. Deliberately not filtered to is_public = true the way
// ListPublicImagesForUnit is: the leader looking at this picker already
// has file-library access to every photo regardless, and mixing public
// and private photos from the same event is fine — a private one just
// won't actually render for a logged-out visitor even inside a "Public"
// album, since FileDownload's own access check still applies wherever
// the photo's URL ends up embedded.
func ListEventPhotoGroupsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]EventFileGroup, error) {
	rows, err := pool.Query(ctx, `
		SELECT e.id, e.title, f.id, f.unit_id, f.filename, f.display_name, f.content_type, f.size_bytes, f.storage_key, f.category::text, f.uploaded_by, f.created_at, f.is_public
		FROM events e
		JOIN event_files ef ON ef.event_id = e.id
		JOIN files f ON f.id = ef.file_id
		WHERE e.unit_id = $1 AND f.content_type LIKE 'image/%'
		ORDER BY e.starts_at DESC, f.created_at DESC
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []EventFileGroup
	index := map[string]int{}
	for rows.Next() {
		var eventID, eventTitle string
		var f File
		if err := rows.Scan(&eventID, &eventTitle, &f.ID, &f.UnitID, &f.Filename, &f.DisplayName, &f.ContentType, &f.SizeBytes, &f.StorageKey, &f.Category, &f.UploadedBy, &f.CreatedAt, &f.Public); err != nil {
			return nil, err
		}
		i, ok := index[eventID]
		if !ok {
			i = len(groups)
			index[eventID] = i
			groups = append(groups, EventFileGroup{EventID: eventID, EventTitle: eventTitle})
		}
		groups[i].Files = append(groups[i].Files, f)
	}
	return groups, rows.Err()
}

// Delete removes a file's metadata row, scoped to a unit. The caller is
// responsible for also deleting the underlying object from storage — see
// internal/web/files.go, which does both under one handler.
func Delete(ctx context.Context, pool *pgxpool.Pool, fileID, unitID string) error {
	_, err := pool.Exec(ctx, `DELETE FROM files WHERE id = $1 AND unit_id = $2`, fileID, unitID)
	return err
}

// SetEventLinks replaces the full set of events a file is linked to with
// exactly eventIDs — a leader managing a file's "linked events" checkbox
// list expects submitting the form to set the list, not merge into it.
// Scoped to unitID via a join so an event ID from another unit can't be
// slipped into the link set.
func SetEventLinks(ctx context.Context, pool *pgxpool.Pool, fileID, unitID string, eventIDs []string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM event_files WHERE file_id = $1`, fileID); err != nil {
		return err
	}
	for _, eventID := range eventIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO event_files (event_id, file_id)
			SELECT $1, $2 WHERE EXISTS (SELECT 1 FROM events WHERE id = $1 AND unit_id = $3)
			ON CONFLICT DO NOTHING
		`, eventID, fileID, unitID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// EventIDsForFile returns the IDs of every event a file is currently
// linked to — pre-checking the right boxes on the "link to events" form.
func EventIDsForFile(ctx context.Context, pool *pgxpool.Pool, fileID string) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT event_id FROM event_files WHERE file_id = $1`, fileID)
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

// ListForEvent returns every file linked to one event, most recent first —
// what the calendar page shows inline under an event (documents and any
// event_photo-category uploads alike; the template distinguishes them by
// Category to render photos as thumbnails and everything else as a plain
// link).
func ListForEvent(ctx context.Context, pool *pgxpool.Pool, eventID string) ([]File, error) {
	rows, err := pool.Query(ctx, `
		SELECT f.id, f.unit_id, f.filename, f.display_name, f.content_type, f.size_bytes, f.storage_key, f.category::text, f.uploaded_by, f.created_at
		FROM files f
		JOIN event_files ef ON ef.file_id = f.id
		WHERE ef.event_id = $1
		ORDER BY f.created_at DESC
	`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.UnitID, &f.Filename, &f.DisplayName, &f.ContentType, &f.SizeBytes, &f.StorageKey, &f.Category, &f.UploadedBy, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListForEvents batches ListForEvent across many events in one query — what
// the calendar page's event list needs, so showing N events' attached
// files doesn't cost N round trips.
func ListForEvents(ctx context.Context, pool *pgxpool.Pool, eventIDs []string) (map[string][]File, error) {
	out := make(map[string][]File)
	if len(eventIDs) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT ef.event_id, f.id, f.unit_id, f.filename, f.display_name, f.content_type, f.size_bytes, f.storage_key, f.category::text, f.uploaded_by, f.created_at
		FROM files f
		JOIN event_files ef ON ef.file_id = f.id
		WHERE ef.event_id = ANY($1)
		ORDER BY f.created_at DESC
	`, eventIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var eventID string
		var f File
		if err := rows.Scan(&eventID, &f.ID, &f.UnitID, &f.Filename, &f.DisplayName, &f.ContentType, &f.SizeBytes, &f.StorageKey, &f.Category, &f.UploadedBy, &f.CreatedAt); err != nil {
			return nil, err
		}
		out[eventID] = append(out[eventID], f)
	}
	return out, rows.Err()
}

// SetSubGroupLinks replaces the full set of files linked to one patrol's/
// den's page with exactly fileIDs — the reverse direction of SetEventLinks
// (one file linked to many events); here it's one sub-group linked to many
// files, since a sub-group's page picks its photos from a checkbox list
// (see admin-group.html). Scoped to unitID via a join so a file ID from
// another unit can't be slipped into the link set.
func SetSubGroupLinks(ctx context.Context, pool *pgxpool.Pool, subGroupID, unitID string, fileIDs []string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM sub_group_files WHERE sub_group_id = $1`, subGroupID); err != nil {
		return err
	}
	for _, fileID := range fileIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO sub_group_files (sub_group_id, file_id)
			SELECT $1, $2 WHERE EXISTS (SELECT 1 FROM files WHERE id = $2 AND unit_id = $3)
			ON CONFLICT DO NOTHING
		`, subGroupID, fileID, unitID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SubGroupIDsForFile returns the IDs of every patrol/den a file is
// currently linked to — mirrors EventIDsForFile.
func SubGroupIDsForFile(ctx context.Context, pool *pgxpool.Pool, fileID string) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT sub_group_id FROM sub_group_files WHERE file_id = $1`, fileID)
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

// ListForSubGroup returns every file linked to one patrol/den, most recent
// first — what that sub-group's own members-only page shows as its photo
// grid. Mirrors ListForEvent.
func ListForSubGroup(ctx context.Context, pool *pgxpool.Pool, subGroupID string) ([]File, error) {
	rows, err := pool.Query(ctx, `
		SELECT f.id, f.unit_id, f.filename, f.display_name, f.content_type, f.size_bytes, f.storage_key, f.category::text, f.uploaded_by, f.created_at, f.is_public
		FROM files f
		JOIN sub_group_files sgf ON sgf.file_id = f.id
		WHERE sgf.sub_group_id = $1
		ORDER BY f.created_at DESC
	`, subGroupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.UnitID, &f.Filename, &f.DisplayName, &f.ContentType, &f.SizeBytes, &f.StorageKey, &f.Category, &f.UploadedBy, &f.CreatedAt, &f.Public); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
