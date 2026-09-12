package files

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The bulk helpers take a list of ids straight off a form, so "which of
// these are mine" is answered in SQL rather than by the handler. These
// cover that boundary: an id from the other unit has to be ignored, not
// acted on, in every direction.

// makeMember creates the family and member an event needs as its
// author — events.created_by is not null.
func makeMember(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	ctx := context.Background()
	var familyID string
	if err := pool.QueryRow(ctx, `INSERT INTO families (name) VALUES ($1) RETURNING id::text`, name+" Family").Scan(&familyID); err != nil {
		t.Fatalf("creating family: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM families WHERE id = $1`, familyID) })

	var memberID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO members (family_id, first_name, last_name, member_type)
		VALUES ($1, $2, 'Tester', 'adult') RETURNING id::text
	`, familyID, name).Scan(&memberID); err != nil {
		t.Fatalf("creating member: %v", err)
	}
	return memberID
}

func makeEvent(t *testing.T, pool *pgxpool.Pool, unitID, author, title string, startsAt time.Time) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO events (unit_id, title, starts_at, visibility, status, created_by)
		VALUES ($1, $2, $3, 'members', 'published', $4) RETURNING id::text
	`, unitID, title, startsAt, author).Scan(&id); err != nil {
		t.Fatalf("creating event: %v", err)
	}
	return id
}

func makeFile(t *testing.T, pool *pgxpool.Pool, unitID, filename, contentType, category string) File {
	t.Helper()
	f, err := Create(context.Background(), pool, File{
		UnitID: unitID, Filename: filename, ContentType: contentType,
		SizeBytes: 10, StorageKey: unitID + "/" + filename, Category: category,
	})
	if err != nil {
		t.Fatalf("creating file: %v", err)
	}
	return f
}

func linkedEvents(t *testing.T, pool *pgxpool.Pool, fileID string) []string {
	t.Helper()
	ids, err := EventIDsForFile(context.Background(), pool, fileID)
	if err != nil {
		t.Fatalf("reading links: %v", err)
	}
	return ids
}

func TestSetPublicManyOnlyTouchesThisUnit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	mine := testUnit(t, pool, "bulk-public-mine")
	theirs := testUnit(t, pool, "bulk-public-theirs")

	a := makeFile(t, pool, mine, "a.jpg", "image/jpeg", CategoryEventPhoto)
	b := makeFile(t, pool, mine, "b.jpg", "image/jpeg", CategoryEventPhoto)
	other := makeFile(t, pool, theirs, "c.jpg", "image/jpeg", CategoryEventPhoto)

	n, err := SetPublicMany(ctx, pool, mine, []string{a.ID, b.ID, other.ID}, true)
	if err != nil {
		t.Fatalf("SetPublicMany: %v", err)
	}
	if n != 2 {
		t.Errorf("changed %d rows, want 2 — the other unit's file should have been skipped", n)
	}

	for _, id := range []string{a.ID, b.ID} {
		f, _, err := Get(ctx, pool, id, mine)
		if err != nil {
			t.Fatalf("reading back: %v", err)
		}
		if !f.Public {
			t.Errorf("file %s was not made public", id)
		}
	}
	f, _, err := Get(ctx, pool, other.ID, theirs)
	if err != nil {
		t.Fatalf("reading back the other unit's file: %v", err)
	}
	if f.Public {
		t.Error("a file belonging to the other unit was published by posting its id")
	}

	// And back again.
	if _, err := SetPublicMany(ctx, pool, mine, []string{a.ID}, false); err != nil {
		t.Fatalf("SetPublicMany(false): %v", err)
	}
	if f, _, _ := Get(ctx, pool, a.ID, mine); f.Public {
		t.Error("making a file members-only again didn't take")
	}

	// An empty selection is a no-op, not an error or a statement with an
	// empty ANY() in it.
	if n, err := SetPublicMany(ctx, pool, mine, nil, true); err != nil || n != 0 {
		t.Errorf("empty selection: n=%d err=%v, want 0/nil", n, err)
	}
}

func TestLinkEventManyAddsAndMoves(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	unitID := testUnit(t, pool, "bulk-link")
	author := makeMember(t, pool, "Linkauthor")

	wrong := makeEvent(t, pool, unitID, author, "The wrong campout", time.Now().Add(-40*24*time.Hour))
	right := makeEvent(t, pool, unitID, author, "The right campout", time.Now().Add(-3*24*time.Hour))
	extra := makeEvent(t, pool, unitID, author, "A joint hike", time.Now().Add(-2*24*time.Hour))

	a := makeFile(t, pool, unitID, "one.jpg", "image/jpeg", CategoryEventPhoto)
	b := makeFile(t, pool, unitID, "two.jpg", "image/jpeg", CategoryEventPhoto)

	// Filed under the wrong event to begin with, the way a batch upload
	// does it.
	if n, err := LinkEventMany(ctx, pool, unitID, wrong, []string{a.ID, b.ID}, false); err != nil || n != 2 {
		t.Fatalf("initial link: n=%d err=%v", n, err)
	}

	// Adding keeps what's there.
	if _, err := LinkEventMany(ctx, pool, unitID, extra, []string{a.ID}, false); err != nil {
		t.Fatalf("add link: %v", err)
	}
	if got := linkedEvents(t, pool, a.ID); len(got) != 2 {
		t.Errorf("after adding, file a has %d links, want 2 (the wrong one and the extra)", len(got))
	}

	// Linking to an event a file is already on changes nothing and says
	// so, rather than failing on a duplicate key.
	n, err := LinkEventMany(ctx, pool, unitID, extra, []string{a.ID}, false)
	if err != nil {
		t.Fatalf("re-linking: %v", err)
	}
	if n != 0 {
		t.Errorf("re-linking reported %d new links, want 0", n)
	}

	// Moving replaces the lot — the actual fix for "filed under the wrong
	// event".
	if n, err := LinkEventMany(ctx, pool, unitID, right, []string{a.ID, b.ID}, true); err != nil || n != 2 {
		t.Fatalf("move: n=%d err=%v", n, err)
	}
	for _, id := range []string{a.ID, b.ID} {
		got := linkedEvents(t, pool, id)
		if len(got) != 1 || got[0] != right {
			t.Errorf("file %s is linked to %v, want just the right campout", id, got)
		}
	}
}

func TestLinkEventManyRefusesAnotherUnitsEvent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	mine := testUnit(t, pool, "bulk-event-mine")
	theirs := testUnit(t, pool, "bulk-event-theirs")
	author := makeMember(t, pool, "Eventauthor")

	ours := makeEvent(t, pool, mine, author, "Our campout", time.Now())
	notOurs := makeEvent(t, pool, theirs, author, "Their campout", time.Now())
	f := makeFile(t, pool, mine, "one.jpg", "image/jpeg", CategoryEventPhoto)

	if _, err := LinkEventMany(ctx, pool, mine, ours, []string{f.ID}, false); err != nil {
		t.Fatalf("linking to our own event: %v", err)
	}

	// A move to another unit's event must not clear the links first and
	// then fail — that would lose the link and attach nothing.
	if _, err := LinkEventMany(ctx, pool, mine, notOurs, []string{f.ID}, true); err != ErrEventNotInUnit {
		t.Fatalf("linking to another unit's event returned %v, want ErrEventNotInUnit", err)
	}
	if got := linkedEvents(t, pool, f.ID); len(got) != 1 || got[0] != ours {
		t.Errorf("the refused move disturbed the existing links: %v", got)
	}
}

func TestUnlinkEventsManyIsUnitScoped(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	mine := testUnit(t, pool, "bulk-unlink-mine")
	theirs := testUnit(t, pool, "bulk-unlink-theirs")
	author := makeMember(t, pool, "Unlinkauthor")

	myEvent := makeEvent(t, pool, mine, author, "Ours", time.Now())
	theirEvent := makeEvent(t, pool, theirs, author, "Theirs", time.Now())
	a := makeFile(t, pool, mine, "a.jpg", "image/jpeg", CategoryEventPhoto)
	other := makeFile(t, pool, theirs, "b.jpg", "image/jpeg", CategoryEventPhoto)

	if _, err := LinkEventMany(ctx, pool, mine, myEvent, []string{a.ID}, false); err != nil {
		t.Fatalf("link: %v", err)
	}
	if _, err := LinkEventMany(ctx, pool, theirs, theirEvent, []string{other.ID}, false); err != nil {
		t.Fatalf("link (other unit): %v", err)
	}

	if _, err := UnlinkEventsMany(ctx, pool, mine, []string{a.ID, other.ID}); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if got := linkedEvents(t, pool, a.ID); len(got) != 0 {
		t.Errorf("our file is still linked to %v", got)
	}
	if got := linkedEvents(t, pool, other.ID); len(got) != 1 {
		t.Error("the other unit's file was unlinked by posting its id")
	}
}

func TestListDocumentFilesGroupedByEventExcludesPictures(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	unitID := testUnit(t, pool, "bulk-docs")
	author := makeMember(t, pool, "Docauthor")
	event := makeEvent(t, pool, unitID, author, "Summer Camp", time.Now().Add(-10*24*time.Hour))

	pdf := makeFile(t, pool, unitID, "packing-list.pdf", "application/pdf", CategoryGeneral)
	// Left unlinked on purpose: it should come back in the ungrouped half.
	makeFile(t, pool, unitID, "handbook.pdf", "application/pdf", CategoryGeneral)
	photo := makeFile(t, pool, unitID, "camp.jpg", "image/jpeg", CategoryEventPhoto)
	video := makeFile(t, pool, unitID, "camp.mp4", "video/mp4", CategoryEventPhoto)
	// A PDF a leader filed under "Event photo/video" by mistake is still
	// a document: the content type is what the file is.
	misfiled := makeFile(t, pool, unitID, "map.pdf", "application/pdf", CategoryEventPhoto)

	for _, f := range []File{pdf, photo, video, misfiled} {
		if _, err := LinkEventMany(ctx, pool, unitID, event, []string{f.ID}, false); err != nil {
			t.Fatalf("linking %s: %v", f.Filename, err)
		}
	}

	groups, ungrouped, err := ListDocumentFilesGroupedByEvent(ctx, pool, unitID)
	if err != nil {
		t.Fatalf("listing documents: %v", err)
	}

	seen := map[string]bool{}
	for _, g := range groups {
		if g.EventTitle != "Summer Camp" {
			t.Errorf("unexpected group %q", g.EventTitle)
		}
		for _, f := range g.Files {
			seen[f.Filename] = true
		}
	}
	for _, f := range ungrouped {
		seen[f.Filename] = true
	}

	for _, want := range []string{"packing-list.pdf", "handbook.pdf", "map.pdf"} {
		if !seen[want] {
			t.Errorf("%s is missing from the document list", want)
		}
	}
	for _, unwanted := range []string{"camp.jpg", "camp.mp4"} {
		if seen[unwanted] {
			t.Errorf("%s is a picture/video and should not be offered as a document", unwanted)
		}
	}
}
