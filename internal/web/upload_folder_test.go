package web

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/db"
	"github.com/47-yonkers/scout-site/internal/files"
)

// Which folder an upload lands in is decided by a database lookup — the
// event has to exist, and has to belong to THIS unit — so it can only be
// checked against Postgres. Skipped without TEST_DATABASE_URL, same
// harness as the other integration tests.
func uploadFolderPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping upload-folder tests")
	}
	// db.Connect + db.Migrate rather than a bare pgxpool.New: `go test
	// ./...` runs packages concurrently, so this package may be the first
	// to reach the database and cannot assume another has migrated it.
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrating test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type folderFixture struct {
	h      *Handlers
	unitID string
}

func newFolderFixture(t *testing.T, name string) folderFixture {
	t.Helper()
	ctx := context.Background()
	pool := uploadFolderPool(t)

	var unitID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO units (slug, name, unit_type, hostname)
		VALUES ($1, $1, 'troop', $1 || '.test.invalid') RETURNING id::text
	`, name).Scan(&unitID); err != nil {
		t.Fatalf("creating unit: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM units WHERE id = $1`, unitID) })

	return folderFixture{h: &Handlers{Pool: pool}, unitID: unitID}
}

func (f folderFixture) event(t *testing.T, title string, when time.Time) string {
	t.Helper()
	ctx := context.Background()
	var familyID, actor, id string
	if err := f.h.Pool.QueryRow(ctx,
		`INSERT INTO families (name) VALUES ($1) RETURNING id::text`, title+" fam").Scan(&familyID); err != nil {
		t.Fatalf("creating family: %v", err)
	}
	t.Cleanup(func() { _, _ = f.h.Pool.Exec(ctx, `DELETE FROM families WHERE id = $1`, familyID) })
	if err := f.h.Pool.QueryRow(ctx, `
		INSERT INTO members (family_id, first_name, last_name, member_type)
		VALUES ($1, 'Test', 'Leader', 'adult') RETURNING id::text
	`, familyID).Scan(&actor); err != nil {
		t.Fatalf("creating member: %v", err)
	}
	if err := f.h.Pool.QueryRow(ctx, `
		INSERT INTO events (unit_id, title, starts_at, ends_at, visibility, status, created_by)
		VALUES ($1, $2, $3, $4, 'members', 'published', $5) RETURNING id::text
	`, f.unitID, title, when, when.Add(time.Hour), actor).Scan(&id); err != nil {
		t.Fatalf("creating event %q: %v", title, err)
	}
	return id
}

func TestUploadAttachedToAnEventIsFiledUnderIt(t *testing.T) {
	ctx := context.Background()
	f := newFolderFixture(t, "folder-event")
	id := f.event(t, "Summer Camp", time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC))

	got := f.h.uploadFolder(ctx, f.unitID, files.CategoryEventPhoto, []string{id})
	if got != "summer-camp-2026-07-15" {
		t.Errorf("folder = %q, want summer-camp-2026-07-15", got)
	}
	// The category no longer decides once an event is named.
	if got := f.h.uploadFolder(ctx, f.unitID, files.CategoryGeneral, []string{id}); got != "summer-camp-2026-07-15" {
		t.Errorf("a document attached to an event went to %q instead of the event's folder", got)
	}
}

func TestUnattachedUploadsAreFiledByCategory(t *testing.T) {
	ctx := context.Background()
	f := newFolderFixture(t, "folder-category")

	if got := f.h.uploadFolder(ctx, f.unitID, files.CategoryGeneral, nil); got != files.DocumentsFolder {
		t.Errorf("a general upload went to %q, want %q", got, files.DocumentsFolder)
	}
	if got := f.h.uploadFolder(ctx, f.unitID, files.CategoryEventPhoto, nil); got != files.PhotosFolder {
		t.Errorf("a loose photo went to %q, want %q", got, files.PhotosFolder)
	}
	// An empty list is the same as none — an unchecked checkbox group.
	if got := f.h.uploadFolder(ctx, f.unitID, files.CategoryGeneral, []string{}); got != files.DocumentsFolder {
		t.Errorf("an empty event list went to %q", got)
	}
}

// TestAnotherUnitsEventCannotNameTheFolder. Event ids arrive in the form
// body, so they are the uploader's to choose. Naming the folder after the
// other unit's event would both misfile the photo and confirm that
// event exists.
func TestAnotherUnitsEventCannotNameTheFolder(t *testing.T) {
	ctx := context.Background()
	troop := newFolderFixture(t, "folder-troop")
	pack := newFolderFixture(t, "folder-pack")
	troopEvent := troop.event(t, "Troop Only Campout", time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC))

	got := pack.h.uploadFolder(ctx, pack.unitID, files.CategoryEventPhoto, []string{troopEvent})
	if got != files.PhotosFolder {
		t.Errorf("the Pack's upload was filed as %q using the Troop's event", got)
	}
}

// TestAnUnknownEventFallsBackRatherThanFailing — a file in a slightly
// wrong folder is a tidiness problem; a refused upload is a lost photo.
func TestAnUnknownEventFallsBackRatherThanFailing(t *testing.T) {
	ctx := context.Background()
	f := newFolderFixture(t, "folder-unknown")

	for _, id := range []string{
		"00000000-0000-0000-0000-000000000000", // well-formed, no such event
		"not-a-uuid",                           // malformed, so the query itself errors
		"",
	} {
		if got := f.h.uploadFolder(ctx, f.unitID, files.CategoryGeneral, []string{id}); got != files.DocumentsFolder {
			t.Errorf("event id %q produced folder %q, want the fallback %q", id, got, files.DocumentsFolder)
		}
	}
}

// TestSeveralEventsPickTheFirstThatResolves — a joint campout links to
// two events; the file still lives in one place.
func TestSeveralEventsPickTheFirstThatResolves(t *testing.T) {
	ctx := context.Background()
	f := newFolderFixture(t, "folder-several")
	first := f.event(t, "Joint Campout", time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC))
	second := f.event(t, "Webelos Overnight", time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC))

	if got := f.h.uploadFolder(ctx, f.unitID, files.CategoryEventPhoto, []string{first, second}); got != "joint-campout-2026-09-12" {
		t.Errorf("folder = %q, want the first event's", got)
	}
	// An unresolvable id ahead of a good one must not lose the good one.
	if got := f.h.uploadFolder(ctx, f.unitID, files.CategoryEventPhoto, []string{"nope", second}); got != "webelos-overnight-2026-09-12" {
		t.Errorf("folder = %q, want it to skip the bad id and use the real event", got)
	}
}

// TestTheKeyItselfLandsWhereTheFolderSays ties the two halves together.
func TestTheKeyItselfLandsWhereTheFolderSays(t *testing.T) {
	ctx := context.Background()
	f := newFolderFixture(t, "folder-key")
	id := f.event(t, "Court of Honor", time.Date(2026, 5, 2, 19, 0, 0, 0, time.UTC))

	folder := f.h.uploadFolder(ctx, f.unitID, files.CategoryEventPhoto, []string{id})
	key := files.NewStorageKey(f.unitID, folder, "group photo.JPG")

	want := f.unitID + "/court-of-honor-2026-05-02/"
	if !strings.HasPrefix(key, want) {
		t.Errorf("key %q is not under %q", key, want)
	}
	if !strings.HasSuffix(key, "-group-photo.jpg") {
		t.Errorf("key %q does not keep a recognizable name", key)
	}
}
