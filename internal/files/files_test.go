package files

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/db"
)

// These cover the two things this package gained for hosted email images
// (see internal/web/inline_images.go): a Create that can make a file
// public in one statement, and a lookup by the content-derived storage
// key that makes re-saving a draft reuse the copy already stored.
//
// Both are SQL, so both are only really checkable against Postgres.
// Skipped without TEST_DATABASE_URL, same harness as internal/family.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping files integration tests")
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

func testUnit(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO units (slug, name, unit_type, hostname)
		VALUES ($1, $1, 'pack', $1 || '.test.invalid') RETURNING id::text
	`, name).Scan(&id); err != nil {
		t.Fatalf("creating test unit: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM units WHERE id = $1`, id) })
	return id
}

func TestCreateHonoursPublic(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	unitID := testUnit(t, pool, "files-public")

	pub, err := Create(ctx, pool, File{
		UnitID: unitID, Filename: "logo.png", ContentType: "image/png",
		SizeBytes: 3, StorageKey: unitID + "/email-images/aaa.png",
		Category: CategoryGeneral, Public: true,
	})
	if err != nil {
		t.Fatalf("creating public file: %v", err)
	}
	priv, err := Create(ctx, pool, File{
		UnitID: unitID, Filename: "roster.pdf", ContentType: "application/pdf",
		SizeBytes: 3, StorageKey: unitID + "/x.pdf", Category: CategoryGeneral,
	})
	if err != nil {
		t.Fatalf("creating private file: %v", err)
	}

	// Read back from the database, not from the struct we passed in — the
	// question is what was actually written.
	got, found, err := Get(ctx, pool, pub.ID, unitID)
	if err != nil || !found {
		t.Fatalf("reading back the public file: found=%v err=%v", found, err)
	}
	if !got.Public {
		t.Error("a file created with Public set came back private, so no mail client could fetch it")
	}
	got, found, err = Get(ctx, pool, priv.ID, unitID)
	if err != nil || !found {
		t.Fatalf("reading back the private file: found=%v err=%v", found, err)
	}
	if got.Public {
		t.Error("a file created without Public came back public — the default has changed")
	}
}

func TestByStorageKeyFindsTheStoredCopy(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	unitID := testUnit(t, pool, "files-bykey")
	key := unitID + "/email-images/deadbeef.jpg"

	if _, found, err := ByStorageKey(ctx, pool, unitID, key); err != nil || found {
		t.Fatalf("a key nothing is stored under: found=%v err=%v", found, err)
	}

	created, err := Create(ctx, pool, File{
		UnitID: unitID, Filename: "logo.jpg", DisplayName: "Email image",
		ContentType: "image/jpeg", SizeBytes: 13280, StorageKey: key,
		Category: CategoryGeneral, Public: true,
	})
	if err != nil {
		t.Fatalf("creating file: %v", err)
	}

	got, found, err := ByStorageKey(ctx, pool, unitID, key)
	if err != nil || !found {
		t.Fatalf("looking up the stored copy: found=%v err=%v", found, err)
	}
	if got.ID != created.ID {
		t.Errorf("found %s, want %s", got.ID, created.ID)
	}
	if !got.Public || got.ContentType != "image/jpeg" || got.SizeBytes != 13280 {
		t.Errorf("row came back wrong: %+v", got)
	}
}

// TestByStorageKeyIsScopedToTheUnit. The key is derived from the image's
// own bytes, so two units embedding the same logo derive the same digest
// — the unit scope (and the unit prefix on the key) is the only thing
// keeping one unit's file from being handed to the other.
func TestByStorageKeyIsScopedToTheUnit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	troop := testUnit(t, pool, "files-troop")
	pack := testUnit(t, pool, "files-pack")

	// Deliberately the SAME key under both units — worse than what the
	// caller can actually produce, since it prefixes the unit id.
	key := "shared/email-images/samedigest.png"
	if _, err := Create(ctx, pool, File{
		UnitID: troop, Filename: "logo.png", ContentType: "image/png",
		SizeBytes: 1, StorageKey: key, Category: CategoryGeneral, Public: true,
	}); err != nil {
		t.Fatalf("creating the Troop's file: %v", err)
	}

	if _, found, err := ByStorageKey(ctx, pool, pack, key); err != nil || found {
		t.Errorf("the Pack was handed the Troop's file: found=%v err=%v", found, err)
	}
	if _, found, err := ByStorageKey(ctx, pool, troop, key); err != nil || !found {
		t.Errorf("the Troop could not find its own file: found=%v err=%v", found, err)
	}
}
