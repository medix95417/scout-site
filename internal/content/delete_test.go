package content

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
	"github.com/47-yonkers/scout-site/internal/db"
)

// Deleting a post is the one content action that cannot be undone, which
// makes its audit entry the one that matters most — and the one most
// easily lost, because the activity log resolves an entry by joining its
// entity_id back to the row that owns it. Point a delete's entry at the
// row it just deleted and the entry is written and immediately
// invisible. That is the property these tests exist for; the deleting
// itself is one line of SQL.

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping content delete tests")
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

type deleteFixture struct {
	pool   *pgxpool.Pool
	unitID string
	actor  string
}

func newDeleteFixture(t *testing.T, name string) deleteFixture {
	t.Helper()
	ctx := context.Background()
	pool := testPool(t)

	var unitID, familyID, actor string
	if err := pool.QueryRow(ctx, `
		INSERT INTO units (slug, name, unit_type, hostname)
		VALUES ($1, $1, 'troop', $1 || '.test.invalid') RETURNING id::text
	`, name).Scan(&unitID); err != nil {
		t.Fatalf("creating unit: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM units WHERE id = $1`, unitID) })

	if err := pool.QueryRow(ctx,
		`INSERT INTO families (name) VALUES ($1) RETURNING id::text`, name).Scan(&familyID); err != nil {
		t.Fatalf("creating family: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM families WHERE id = $1`, familyID) })
	if err := pool.QueryRow(ctx, `
		INSERT INTO members (family_id, first_name, last_name, member_type)
		VALUES ($1, 'Ada', 'Leader', 'adult') RETURNING id::text
	`, familyID).Scan(&actor); err != nil {
		t.Fatalf("creating member: %v", err)
	}
	return deleteFixture{pool: pool, unitID: unitID, actor: actor}
}

func (f deleteFixture) post(t *testing.T, title string) Post {
	t.Helper()
	p, err := CreatePost(context.Background(), f.pool, f.unitID, "post", title, "Body of "+title, "public", "", f.actor)
	if err != nil {
		t.Fatalf("creating post %q: %v", title, err)
	}
	return p
}

func TestDeletePostRemovesIt(t *testing.T) {
	ctx := context.Background()
	f := newDeleteFixture(t, "content-del")
	doomed := f.post(t, "Posted by mistake")
	keeper := f.post(t, "Pack meeting Tuesday")

	if err := DeletePost(ctx, f.pool, doomed.ID, f.unitID, f.actor); err != nil {
		t.Fatalf("DeletePost: %v", err)
	}

	if _, found, err := GetPostAnyType(ctx, f.pool, doomed.ID, f.unitID); err != nil || found {
		t.Errorf("the post is still there: found=%v err=%v", found, err)
	}
	// Really gone, not merely unpublished — which is the whole point.
	var status string
	err := f.pool.QueryRow(ctx, `SELECT status::text FROM content_pages WHERE id = $1`, doomed.ID).Scan(&status)
	if err == nil {
		t.Errorf("the row survives with status %q; this was supposed to be a delete", status)
	}
	if _, found, _ := GetPostAnyType(ctx, f.pool, keeper.ID, f.unitID); !found {
		t.Error("deleting one post took another with it")
	}
}

// TestDeletePostIsScopedToTheUnit — a post id from the other unit must
// not be deletable, the same guard every other write in this package has.
func TestDeletePostIsScopedToTheUnit(t *testing.T) {
	ctx := context.Background()
	troop := newDeleteFixture(t, "content-del-troop")
	pack := newDeleteFixture(t, "content-del-pack")
	troopPost := troop.post(t, "Troop only")

	if err := DeletePost(ctx, pack.pool, troopPost.ID, pack.unitID, pack.actor); err == nil {
		t.Error("the Pack deleted the Troop's post")
	}
	if _, found, _ := GetPostAnyType(ctx, troop.pool, troopPost.ID, troop.unitID); !found {
		t.Error("the Troop's post is gone")
	}
}

// TestTheDeleteStaysInTheActivityLog is the property worth the extra
// code: after the post is gone, the record that somebody deleted it must
// still be readable.
func TestTheDeleteStaysInTheActivityLog(t *testing.T) {
	ctx := context.Background()
	f := newDeleteFixture(t, "content-del-audit")
	p := f.post(t, "Wrong date on this one")

	if err := DeletePost(ctx, f.pool, p.ID, f.unitID, f.actor); err != nil {
		t.Fatalf("DeletePost: %v", err)
	}

	entries, err := audit.ForUnitFiltered(ctx, f.pool, audit.Filter{UnitID: f.unitID})
	if err != nil {
		t.Fatalf("reading the activity log: %v", err)
	}
	var found *audit.LogEntry
	for i := range entries {
		if entries[i].Action == "delete" && entries[i].EntityType == "content_page" {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("the delete is not in the activity log — %d entries, none of them the delete", len(entries))
	}
	if found.ActorName == "" || found.ActorName == "system" {
		t.Errorf("the delete is not attributed to anyone: %+v", found)
	}

	// And it says WHICH post, since the post itself is no longer there to
	// look up.
	var before string
	if err := f.pool.QueryRow(ctx,
		`SELECT COALESCE(before_state::text, '') FROM audit_log WHERE id = $1`, found.ID).Scan(&before); err != nil {
		t.Fatalf("reading the entry payload: %v", err)
	}
	if !strings.Contains(before, "Wrong date on this one") {
		t.Errorf("the entry does not record what was deleted: %q", before)
	}
}

// TestDeletingAMissingPostIsAnError rather than a silent success — the
// caller redirects to a list that would look unchanged.
func TestDeletingAMissingPostIsAnError(t *testing.T) {
	ctx := context.Background()
	f := newDeleteFixture(t, "content-del-missing")
	if err := DeletePost(ctx, f.pool, "00000000-0000-0000-0000-000000000000", f.unitID, f.actor); err == nil {
		t.Error("deleting a post that does not exist reported success")
	}
}

// TestANewsDeleteCannotReachAGallery. The delete handler guards on
// GetPost with the kind's own page type; if that stopped discriminating,
// a photo album's id posted to /admin/news/{id}/delete would remove the
// album. Nothing else in the request would look wrong.
func TestANewsDeleteCannotReachAGallery(t *testing.T) {
	ctx := context.Background()
	f := newDeleteFixture(t, "content-del-type")
	album, err := CreatePost(ctx, f.pool, f.unitID, "gallery", "Camp photos", "https://example.com/a.jpg", "public", "", f.actor)
	if err != nil {
		t.Fatalf("creating gallery: %v", err)
	}

	if _, found, err := GetPost(ctx, f.pool, album.ID, f.unitID, "post"); err != nil || found {
		t.Errorf("a gallery resolved as a news post: found=%v err=%v", found, err)
	}
	// And it is still there, since the handler would have stopped above.
	if _, found, _ := GetPostAnyType(ctx, f.pool, album.ID, f.unitID); !found {
		t.Error("the album is gone")
	}
}

// TestAFailedDeleteLeavesNoRecordOfOne. The entry is written after the
// delete succeeds, so a delete that removed nothing must not leave an
// activity-log line claiming it did.
func TestAFailedDeleteLeavesNoRecordOfOne(t *testing.T) {
	ctx := context.Background()
	f := newDeleteFixture(t, "content-del-phantom")

	if err := DeletePost(ctx, f.pool, "00000000-0000-0000-0000-000000000000", f.unitID, f.actor); err == nil {
		t.Fatal("deleting a post that does not exist reported success")
	}
	entries, err := audit.ForUnitFiltered(ctx, f.pool, audit.Filter{UnitID: f.unitID})
	if err != nil {
		t.Fatalf("reading the activity log: %v", err)
	}
	for _, e := range entries {
		if e.Action == "delete" && e.EntityType == "content_page" {
			t.Errorf("a delete that did nothing was logged as one: %+v", e)
		}
	}
}
