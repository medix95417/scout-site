package web

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/db"
	"github.com/47-yonkers/scout-site/internal/files"
)

// Rekeying moves real photos. The property that matters is not "the key
// changed" — it is that at no instant does the database name an object
// that is not there, however the run is interrupted. So the bucket here
// is a map that can be told to fail at each step, and the assertions are
// about what survives the failure.

// fakeBucket is object storage in a map.
type fakeBucket struct {
	objects map[string]bool

	failCopy   string // fail a copy whose SOURCE is this key
	failDelete string
	// silentCopy makes a copy of this source REPORT success and write
	// nothing — the way a broken backend loses a file quietly.
	silentCopy string

	attempted []string // copy sources attempted, in order
	copies    []string // dst keys actually written, in order
	deletes   []string
}

func newFakeBucket(keys ...string) *fakeBucket {
	b := &fakeBucket{objects: map[string]bool{}}
	for _, k := range keys {
		b.objects[k] = true
	}
	return b
}

func (b *fakeBucket) Copy(_ context.Context, src, dst string) error {
	b.attempted = append(b.attempted, src)
	if b.failCopy != "" && src == b.failCopy {
		return errors.New("copy failed")
	}
	if b.silentCopy != "" && src == b.silentCopy {
		return nil // "succeeded", wrote nothing
	}
	if !b.objects[src] {
		return errors.New("no such object")
	}
	b.objects[dst] = true
	b.copies = append(b.copies, dst)
	return nil
}

func (b *fakeBucket) Exists(_ context.Context, key string) bool { return b.objects[key] }

func (b *fakeBucket) Delete(_ context.Context, key string) error {
	if b.failDelete != "" && key == b.failDelete {
		return errors.New("delete failed")
	}
	delete(b.objects, key)
	b.deletes = append(b.deletes, key)
	return nil
}

// --- database fixture -----------------------------------------------

func rekeyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping rekey tests")
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

type rekeyFixture struct {
	pool   *pgxpool.Pool
	unitID string
	actor  string
}

func newRekeyFixture(t *testing.T, name string) rekeyFixture {
	t.Helper()
	ctx := context.Background()
	pool := rekeyPool(t)

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
		VALUES ($1, 'Test', 'Leader', 'adult') RETURNING id::text
	`, familyID).Scan(&actor); err != nil {
		t.Fatalf("creating member: %v", err)
	}
	return rekeyFixture{pool: pool, unitID: unitID, actor: actor}
}

// oldStyleFile inserts a row with a flat, pre-folder key.
func (f rekeyFixture) oldStyleFile(t *testing.T, filename, category string) files.File {
	t.Helper()
	key := f.unitID + "/" + "11111111-2222-3333-4444-" + filename
	created, err := files.Create(context.Background(), f.pool, files.File{
		UnitID: f.unitID, Filename: filename, ContentType: "image/jpeg",
		SizeBytes: 100, StorageKey: key, Category: category, UploadedBy: &f.actor,
	})
	if err != nil {
		t.Fatalf("creating file %q: %v", filename, err)
	}
	return created
}

func (f rekeyFixture) event(t *testing.T, title string, when time.Time) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO events (unit_id, title, starts_at, ends_at, visibility, status, created_by)
		VALUES ($1, $2, $3, $4, 'members', 'published', $5) RETURNING id::text
	`, f.unitID, title, when, when.Add(time.Hour), f.actor).Scan(&id); err != nil {
		t.Fatalf("creating event: %v", err)
	}
	return id
}

func (f rekeyFixture) keyOf(t *testing.T, fileID string) string {
	t.Helper()
	var key string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT storage_key FROM files WHERE id = $1`, fileID).Scan(&key); err != nil {
		t.Fatalf("reading storage_key: %v", err)
	}
	return key
}

// --- the tests -------------------------------------------------------

func TestRekeyMovesFilesIntoTheirFolders(t *testing.T) {
	ctx := context.Background()
	f := newRekeyFixture(t, "rekey-move")

	doc := f.oldStyleFile(t, "bylaws.pdf", files.CategoryGeneral)
	photo := f.oldStyleFile(t, "meeting.jpg", files.CategoryEventPhoto)
	camp := f.oldStyleFile(t, "camp.jpg", files.CategoryEventPhoto)
	eventID := f.event(t, "Summer Camp", time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC))
	if err := files.SetEventLinks(ctx, f.pool, camp.ID, f.unitID, []string{eventID}); err != nil {
		t.Fatal(err)
	}

	bucket := newFakeBucket(doc.StorageKey, photo.StorageKey, camp.StorageKey)
	result, err := rekeyFiles(ctx, f.pool, bucket, true)
	if err != nil {
		t.Fatalf("rekeyFiles: %v", err)
	}
	if len(result.Moves) != 3 {
		t.Fatalf("moved %d files, want 3 (%+v)", len(result.Moves), result)
	}

	for _, c := range []struct {
		id, wantPrefix string
	}{
		{doc.ID, f.unitID + "/documents/"},
		{photo.ID, f.unitID + "/photos/"},
		{camp.ID, f.unitID + "/summer-camp-2026-07-15/"},
	} {
		got := f.keyOf(t, c.id)
		if !strings.HasPrefix(got, c.wantPrefix) {
			t.Errorf("file landed at %q, want it under %q", got, c.wantPrefix)
		}
		if !bucket.objects[got] {
			t.Errorf("the database names %q but the bucket has no such object", got)
		}
	}
	// The originals are gone, not left as duplicates.
	for _, old := range []string{doc.StorageKey, photo.StorageKey, camp.StorageKey} {
		if bucket.objects[old] {
			t.Errorf("the original %q is still in the bucket", old)
		}
	}
}

// TestDryRunChangesNothing. This is the default, and the only reason it
// is safe to hand an operator a command that rewrites their bucket.
func TestDryRunChangesNothing(t *testing.T) {
	ctx := context.Background()
	f := newRekeyFixture(t, "rekey-dry")
	doc := f.oldStyleFile(t, "bylaws.pdf", files.CategoryGeneral)

	bucket := newFakeBucket(doc.StorageKey)
	result, err := rekeyFiles(ctx, f.pool, bucket, false)
	if err != nil {
		t.Fatalf("rekeyFiles: %v", err)
	}

	if len(result.Moves) != 1 {
		t.Errorf("the plan lists %d moves, want 1", len(result.Moves))
	}
	if got := f.keyOf(t, doc.ID); got != doc.StorageKey {
		t.Errorf("a dry run changed the database: %q -> %q", doc.StorageKey, got)
	}
	if len(bucket.copies) != 0 || len(bucket.deletes) != 0 {
		t.Errorf("a dry run touched the bucket: %d copies, %d deletes", len(bucket.copies), len(bucket.deletes))
	}
	if !bucket.objects[doc.StorageKey] {
		t.Error("a dry run removed the object")
	}
}

// TestRekeyIsIdempotent — a second run has nothing to do, and a third
// would not shuffle anything back. This is what makes it safe to re-run
// after an interruption.
func TestRekeyIsIdempotent(t *testing.T) {
	ctx := context.Background()
	f := newRekeyFixture(t, "rekey-idem")
	doc := f.oldStyleFile(t, "bylaws.pdf", files.CategoryGeneral)
	bucket := newFakeBucket(doc.StorageKey)

	if _, err := rekeyFiles(ctx, f.pool, bucket, true); err != nil {
		t.Fatal(err)
	}
	afterFirst := f.keyOf(t, doc.ID)

	second, err := rekeyFiles(ctx, f.pool, bucket, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Moves) != 0 {
		t.Errorf("the second run moved %d files, want 0", len(second.Moves))
	}
	if second.Skipped != 1 {
		t.Errorf("the second run skipped %d, want 1", second.Skipped)
	}
	if got := f.keyOf(t, doc.ID); got != afterFirst {
		t.Errorf("the second run changed the key again: %q -> %q", afterFirst, got)
	}
}

// TestAFailedCopyLeavesEverythingWhereItWas is the crash-safety case
// that matters most: the row must still name the original.
func TestAFailedCopyLeavesEverythingWhereItWas(t *testing.T) {
	ctx := context.Background()
	f := newRekeyFixture(t, "rekey-copyfail")
	doc := f.oldStyleFile(t, "bylaws.pdf", files.CategoryGeneral)

	bucket := newFakeBucket(doc.StorageKey)
	bucket.failCopy = doc.StorageKey

	result, err := rekeyFiles(ctx, f.pool, bucket, true)
	if err != nil {
		t.Fatalf("rekeyFiles: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("Failed = %d, want 1", result.Failed)
	}
	if got := f.keyOf(t, doc.ID); got != doc.StorageKey {
		t.Errorf("the row was repointed despite the copy failing: %q", got)
	}
	if !bucket.objects[doc.StorageKey] {
		t.Error("the original was deleted despite the copy failing")
	}
}

// TestAFailedDeleteStillLeavesTheFileReachable — the delete is last, so
// failing it costs an orphan and nothing else.
func TestAFailedDeleteStillLeavesTheFileReachable(t *testing.T) {
	ctx := context.Background()
	f := newRekeyFixture(t, "rekey-delfail")
	doc := f.oldStyleFile(t, "bylaws.pdf", files.CategoryGeneral)

	bucket := newFakeBucket(doc.StorageKey)
	bucket.failDelete = doc.StorageKey

	result, err := rekeyFiles(ctx, f.pool, bucket, true)
	if err != nil {
		t.Fatalf("rekeyFiles: %v", err)
	}
	if len(result.Moves) != 1 {
		t.Errorf("the move was reported as failed, but only the cleanup failed: %+v", result)
	}
	got := f.keyOf(t, doc.ID)
	if got == doc.StorageKey {
		t.Error("the row was not repointed")
	}
	if !bucket.objects[got] {
		t.Errorf("the database names %q but the bucket has no such object", got)
	}
	if !bucket.objects[doc.StorageKey] {
		t.Error("the original vanished even though its delete failed")
	}
}

// TestAMissingObjectIsReportedNotRepointed. A row pointing at nothing is
// a pre-existing problem; giving it a fresh key would erase the evidence
// and leave the same broken row looking healthy.
func TestAMissingObjectIsReportedNotRepointed(t *testing.T) {
	ctx := context.Background()
	f := newRekeyFixture(t, "rekey-missing")
	ghost := f.oldStyleFile(t, "never-uploaded.pdf", files.CategoryGeneral)

	bucket := newFakeBucket() // empty
	result, err := rekeyFiles(ctx, f.pool, bucket, true)
	if err != nil {
		t.Fatalf("rekeyFiles: %v", err)
	}
	if len(result.Missing) != 1 {
		t.Fatalf("Missing = %d, want 1", len(result.Missing))
	}
	// Reported as missing, and NOT also counted as a failed move — the
	// two mean different things to whoever reads the output.
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0: a row with no object is missing, not a failure", result.Failed)
	}
	if len(bucket.attempted) != 0 {
		t.Errorf("a copy was attempted for a row with no object: %v", bucket.attempted)
	}
	if len(result.Moves) != 0 {
		t.Errorf("a row with no object was moved anyway: %+v", result.Moves)
	}
	if got := f.keyOf(t, ghost.ID); got != ghost.StorageKey {
		t.Errorf("a row with no object was repointed to %q", got)
	}
}

// TestThumbnailsMoveWithTheirFile, and a thumbnail that cannot be moved
// does not fail the move — it regenerates on demand.
func TestThumbnailsMoveWithTheirFile(t *testing.T) {
	ctx := context.Background()
	f := newRekeyFixture(t, "rekey-thumb")
	photo := f.oldStyleFile(t, "camp.jpg", files.CategoryEventPhoto)
	oldThumb := photo.StorageKey + thumbStorageSuffix

	bucket := newFakeBucket(photo.StorageKey, oldThumb)
	if _, err := rekeyFiles(ctx, f.pool, bucket, true); err != nil {
		t.Fatal(err)
	}

	newKey := f.keyOf(t, photo.ID)
	if !bucket.objects[newKey+thumbStorageSuffix] {
		t.Error("the thumbnail did not move with its file")
	}
	if bucket.objects[oldThumb] {
		t.Error("the old thumbnail was left behind")
	}

	// A file with no cached thumbnail must not acquire a phantom one.
	f2 := newRekeyFixture(t, "rekey-nothumb")
	doc := f2.oldStyleFile(t, "bylaws.pdf", files.CategoryGeneral)
	b2 := newFakeBucket(doc.StorageKey)
	if _, err := rekeyFiles(ctx, f2.pool, b2, true); err != nil {
		t.Fatal(err)
	}
	if b2.objects[f2.keyOf(t, doc.ID)+thumbStorageSuffix] {
		t.Error("a thumbnail was invented for a file that had none")
	}
	// And it should not even ask: one pointless failing copy per file
	// across a whole library is a lot of noise for nothing.
	for _, src := range b2.attempted {
		if strings.HasSuffix(src, thumbStorageSuffix) {
			t.Errorf("a thumbnail copy was attempted for a file with no thumbnail: %q", src)
		}
	}
}

// TestAThumbnailThatCannotMoveDoesNotFailTheMove — the file is the point;
// a thumbnail regenerates on demand.
func TestAThumbnailThatCannotMoveDoesNotFailTheMove(t *testing.T) {
	ctx := context.Background()
	f := newRekeyFixture(t, "rekey-thumbfail")
	photo := f.oldStyleFile(t, "camp.jpg", files.CategoryEventPhoto)
	oldThumb := photo.StorageKey + thumbStorageSuffix

	bucket := newFakeBucket(photo.StorageKey, oldThumb)
	bucket.failCopy = oldThumb

	result, err := rekeyFiles(ctx, f.pool, bucket, true)
	if err != nil {
		t.Fatalf("rekeyFiles: %v", err)
	}
	if result.Failed != 0 || len(result.Moves) != 1 {
		t.Errorf("Failed=%d Moves=%d — a thumbnail failure aborted the file's move", result.Failed, len(result.Moves))
	}
	newKey := f.keyOf(t, photo.ID)
	if newKey == photo.StorageKey {
		t.Error("the file was not repointed")
	}
	if !bucket.objects[newKey] {
		t.Errorf("the database names %q but the bucket has no such object", newKey)
	}
	// The stale thumbnail stays where it is rather than being deleted
	// with nothing to replace it.
	if !bucket.objects[oldThumb] {
		t.Error("the old thumbnail was deleted even though its copy failed")
	}
}

// TestACopyThatQuietlyWritesNothingIsCaught. A backend that returns
// success without storing the bytes is the one failure mode that would
// otherwise lose a file: repoint, then delete the only real copy.
func TestACopyThatQuietlyWritesNothingIsCaught(t *testing.T) {
	ctx := context.Background()
	f := newRekeyFixture(t, "rekey-silent")
	doc := f.oldStyleFile(t, "bylaws.pdf", files.CategoryGeneral)

	bucket := newFakeBucket(doc.StorageKey)
	bucket.silentCopy = doc.StorageKey

	result, err := rekeyFiles(ctx, f.pool, bucket, true)
	if err != nil {
		t.Fatalf("rekeyFiles: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("Failed = %d, want 1 — an empty copy was treated as a success", result.Failed)
	}
	if got := f.keyOf(t, doc.ID); got != doc.StorageKey {
		t.Errorf("the row was repointed to %q, which was never written", got)
	}
	if !bucket.objects[doc.StorageKey] {
		t.Fatal("the original was deleted — the file is gone")
	}
}

// TestOneBadFileDoesNotAbandonTheRest. A library is thousands of files;
// one unreadable object must not stop the pass.
func TestOneBadFileDoesNotAbandonTheRest(t *testing.T) {
	ctx := context.Background()
	f := newRekeyFixture(t, "rekey-partial")
	bad := f.oldStyleFile(t, "broken.pdf", files.CategoryGeneral)
	good := f.oldStyleFile(t, "fine.pdf", files.CategoryGeneral)

	bucket := newFakeBucket(bad.StorageKey, good.StorageKey)
	bucket.failCopy = bad.StorageKey

	result, err := rekeyFiles(ctx, f.pool, bucket, true)
	if err != nil {
		t.Fatalf("rekeyFiles: %v", err)
	}
	if result.Failed != 1 || len(result.Moves) != 1 {
		t.Errorf("Failed=%d Moves=%d, want 1 and 1", result.Failed, len(result.Moves))
	}
	if got := f.keyOf(t, good.ID); got == good.StorageKey {
		t.Error("the healthy file was not moved")
	}
}

// TestRekeyFilesTheSameWayAnUploadWould is what keeps a second run from
// undoing the first: both go through folderFor.
func TestRekeyFilesTheSameWayAnUploadWould(t *testing.T) {
	ctx := context.Background()
	f := newRekeyFixture(t, "rekey-agrees")
	eventID := f.event(t, "Court of Honor", time.Date(2026, 5, 2, 19, 0, 0, 0, time.UTC))
	photo := f.oldStyleFile(t, "coh.jpg", files.CategoryEventPhoto)
	if err := files.SetEventLinks(ctx, f.pool, photo.ID, f.unitID, []string{eventID}); err != nil {
		t.Fatal(err)
	}

	bucket := newFakeBucket(photo.StorageKey)
	if _, err := rekeyFiles(ctx, f.pool, bucket, true); err != nil {
		t.Fatal(err)
	}

	h := &Handlers{Pool: f.pool}
	uploadWould := h.uploadFolder(ctx, f.unitID, files.CategoryEventPhoto, []string{eventID})
	got := f.keyOf(t, photo.ID)
	if !strings.HasPrefix(got, f.unitID+"/"+uploadWould+"/") {
		t.Errorf("rekey filed it at %q, but an upload today would use folder %q", got, uploadWould)
	}
}
