// Package resources manages the curated public/private resources page —
// documents and links leaders want visitors to find easily, distinct from
// the general file library (internal/files), which is an upload/management
// surface aimed at leaders rather than a curated, visitor-facing list. See
// migration 0019_resources.sql.
package resources

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

// Resource is one entry on the resources page — either a document (FileID
// set) or a link (URL set), never both (see the database CHECK
// constraint).
type Resource struct {
	ID          string
	UnitID      string
	Title       string
	Description string
	FileID      *string
	URL         *string
	IsPublic    bool
	CreatedBy   *string
	CreatedAt   time.Time

	// Set only when FileID is non-nil — joined in from files so rendering
	// the list doesn't cost one query per document resource.
	FileFilename    string
	FileDisplayName string
	FileSizeBytes   int64

	// FileIsPublic is the underlying file's OWN public flag, which is a
	// separate thing from IsPublic above and can disagree with it.
	//
	// IsPublic controls this entry: whether it's listed on the public
	// resources page and downloadable at /resources/{id}/download by a
	// logged-out visitor. files.is_public controls the file itself, at
	// /files/{id}/download. A leader who marks a resource private can
	// therefore still be leaving the document downloadable by anyone, if
	// that file was also published to the library — which is not what
	// "private" looks like it means.
	//
	// The two are deliberately NOT synced: one file can be a resource, a
	// homepage hero, and an event photo at once, so flipping the library
	// flag from here would silently un-publish it elsewhere. Instead the
	// admin page surfaces the mismatch and offers to fix it — see
	// PubliclyReachableButPrivate.
	FileIsPublic bool
}

// PubliclyReachableButPrivate reports the case worth warning about: a
// resource its leader has marked private, whose document anybody can
// still download through the file library.
func (r Resource) PubliclyReachableButPrivate() bool {
	return r.FileID != nil && !r.IsPublic && r.FileIsPublic
}

// FileDisplayLabel is what to show for this resource's underlying file —
// mirrors files.File.DisplayLabel without needing a second query to fetch
// the full File row.
func (r Resource) FileDisplayLabel() string {
	if r.FileDisplayName != "" {
		return r.FileDisplayName
	}
	return r.FileFilename
}

const selectColumns = `
	resources.id, resources.unit_id, resources.title, resources.description,
	resources.file_id, resources.url, resources.is_public, resources.created_by, resources.created_at,
	COALESCE(files.filename, ''), COALESCE(files.display_name, ''), COALESCE(files.size_bytes, 0),
	COALESCE(files.is_public, false)
`

func scanResource(row interface{ Scan(...any) error }) (Resource, error) {
	var r Resource
	err := row.Scan(&r.ID, &r.UnitID, &r.Title, &r.Description, &r.FileID, &r.URL, &r.IsPublic, &r.CreatedBy, &r.CreatedAt, &r.FileFilename, &r.FileDisplayName, &r.FileSizeBytes, &r.FileIsPublic)
	return r, err
}

// CreateFileInput creates a document resource pointing at an already-
// uploaded file (see internal/files) — the resources page never handles
// uploads itself, it just curates from what's already in the library.
type CreateFileInput struct {
	UnitID      string
	Title       string
	Description string
	FileID      string
	IsPublic    bool
	CreatedBy   string
}

// CreateFile records a new document resource and logs it to the audit
// trail.
func CreateFile(ctx context.Context, pool *pgxpool.Pool, in CreateFileInput) (Resource, error) {
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO resources (unit_id, title, description, file_id, is_public, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, in.UnitID, in.Title, in.Description, in.FileID, in.IsPublic, in.CreatedBy).Scan(&id)
	if err != nil {
		return Resource{}, err
	}
	return getAndLog(ctx, pool, id, in.UnitID, in.CreatedBy)
}

// CreateLinkInput creates a link resource pointing at an external URL.
type CreateLinkInput struct {
	UnitID      string
	Title       string
	Description string
	URL         string
	IsPublic    bool
	CreatedBy   string
}

// CreateLink records a new link resource and logs it to the audit trail.
func CreateLink(ctx context.Context, pool *pgxpool.Pool, in CreateLinkInput) (Resource, error) {
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO resources (unit_id, title, description, url, is_public, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, in.UnitID, in.Title, in.Description, in.URL, in.IsPublic, in.CreatedBy).Scan(&id)
	if err != nil {
		return Resource{}, err
	}
	return getAndLog(ctx, pool, id, in.UnitID, in.CreatedBy)
}

func getAndLog(ctx context.Context, pool *pgxpool.Pool, id, unitID, actorID string) (Resource, error) {
	r, found, err := Get(ctx, pool, id, unitID)
	if err != nil {
		return Resource{}, err
	}
	if !found {
		return Resource{}, nil
	}
	audit.Log(ctx, pool, audit.Entry{EntityType: "resource", EntityID: r.ID, ActorID: &actorID, Action: "create", After: r})
	return r, nil
}

// Get looks up a single resource, scoped to a unit — same "scope every
// lookup to the requester's unit" guard used throughout this codebase, so a
// resource ID from another unit can't be fetched or deleted through this
// unit's page.
func Get(ctx context.Context, pool *pgxpool.Pool, id, unitID string) (Resource, bool, error) {
	row := pool.QueryRow(ctx, `
		SELECT `+selectColumns+`
		FROM resources LEFT JOIN files ON files.id = resources.file_id
		WHERE resources.id = $1 AND resources.unit_id = $2
	`, id, unitID)
	r, err := scanResource(row)
	if err != nil {
		return Resource{}, false, nil //nolint:nilerr // "no such resource in this unit" is a normal, expected outcome
	}
	return r, true, nil
}

// ListForUnit returns every resource for a unit — public and members-only
// alike — for a logged-in visitor and the admin management view. Most
// recent first.
func ListForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Resource, error) {
	return list(ctx, pool, `WHERE resources.unit_id = $1`, unitID)
}

// ListPublicForUnit returns only the resources marked public — what a
// logged-out visitor may see. Kept as a separate query (rather than
// filtering ListForUnit's result in Go) so a members-only resource is never
// even pulled into a request that will discard it.
func ListPublicForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Resource, error) {
	return list(ctx, pool, `WHERE resources.unit_id = $1 AND resources.is_public = true`, unitID)
}

func list(ctx context.Context, pool *pgxpool.Pool, where string, unitID string) ([]Resource, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+selectColumns+`
		FROM resources LEFT JOIN files ON files.id = resources.file_id
		`+where+`
		ORDER BY resources.created_at DESC
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Resource
	for rows.Next() {
		r, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetPublic flips whether a resource is visible to logged-out visitors,
// scoped to unitID like every other write here.
func SetPublic(ctx context.Context, pool *pgxpool.Pool, id, unitID string, public bool) error {
	_, err := pool.Exec(ctx, `UPDATE resources SET is_public = $1 WHERE id = $2 AND unit_id = $3`, public, id, unitID)
	return err
}

// Delete removes a resource, scoped to a unit. This only removes the
// curated entry — the underlying uploaded file (if any) stays in the file
// library untouched, since other resources or event links may still
// reference it.
func Delete(ctx context.Context, pool *pgxpool.Pool, id, unitID string) error {
	_, err := pool.Exec(ctx, `DELETE FROM resources WHERE id = $1 AND unit_id = $2`, id, unitID)
	return err
}
