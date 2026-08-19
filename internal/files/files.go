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
	ContentType string
	SizeBytes   int64
	StorageKey  string
	Category    string
	UploadedBy  *string
	CreatedAt   time.Time
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
		INSERT INTO files (unit_id, filename, content_type, size_bytes, storage_key, category, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at
	`, f.UnitID, f.Filename, f.ContentType, f.SizeBytes, f.StorageKey, f.Category, f.UploadedBy,
	).Scan(&f.ID, &f.CreatedAt)
	if err != nil {
		return File{}, err
	}
	return f, nil
}

// ListForUnit returns every file belonging to a unit, most recent first.
func ListForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]File, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, unit_id, filename, content_type, size_bytes, storage_key, category::text, uploaded_by, created_at
		FROM files WHERE unit_id = $1 ORDER BY created_at DESC
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.UnitID, &f.Filename, &f.ContentType, &f.SizeBytes, &f.StorageKey, &f.Category, &f.UploadedBy, &f.CreatedAt); err != nil {
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
		SELECT id, unit_id, filename, content_type, size_bytes, storage_key, category::text, uploaded_by, created_at
		FROM files WHERE id = $1 AND unit_id = $2
	`, fileID, unitID).Scan(&f.ID, &f.UnitID, &f.Filename, &f.ContentType, &f.SizeBytes, &f.StorageKey, &f.Category, &f.UploadedBy, &f.CreatedAt)
	if err != nil {
		return File{}, false, nil //nolint:nilerr // "no such file in this unit" is a normal, expected outcome
	}
	return f, true, nil
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
		SELECT f.id, f.unit_id, f.filename, f.content_type, f.size_bytes, f.storage_key, f.category::text, f.uploaded_by, f.created_at
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
		if err := rows.Scan(&f.ID, &f.UnitID, &f.Filename, &f.ContentType, &f.SizeBytes, &f.StorageKey, &f.Category, &f.UploadedBy, &f.CreatedAt); err != nil {
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
		SELECT ef.event_id, f.id, f.unit_id, f.filename, f.content_type, f.size_bytes, f.storage_key, f.category::text, f.uploaded_by, f.created_at
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
		if err := rows.Scan(&eventID, &f.ID, &f.UnitID, &f.Filename, &f.ContentType, &f.SizeBytes, &f.StorageKey, &f.Category, &f.UploadedBy, &f.CreatedAt); err != nil {
			return nil, err
		}
		out[eventID] = append(out[eventID], f)
	}
	return out, rows.Err()
}
