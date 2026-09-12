package audit

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/db"
)

// A sign-in is recorded against the member who signed in, with the
// address in after_state's "ip" key. Two things have to hold for that to
// be worth anything, and both are SQL: the entry has to be reachable
// from the unit's activity log at all, and the address has to come back
// out of the JSON into LogEntry.IPAddress.

func auditTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping audit integration tests")
	}
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

// signInFixture builds a unit with one member holding a role in it, and
// returns the unit and member ids.
func signInFixture(t *testing.T, pool *pgxpool.Pool, slug string) (unitID, memberID string) {
	t.Helper()
	ctx := context.Background()

	if err := pool.QueryRow(ctx, `
		INSERT INTO units (slug, name, unit_type, hostname)
		VALUES ($1, $1, 'troop', $1 || '.test.invalid') RETURNING id::text
	`, slug).Scan(&unitID); err != nil {
		t.Fatalf("creating unit: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM units WHERE id = $1`, unitID) })

	var familyID string
	if err := pool.QueryRow(ctx, `INSERT INTO families (name) VALUES ($1) RETURNING id::text`, slug+" Family").Scan(&familyID); err != nil {
		t.Fatalf("creating family: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM families WHERE id = $1`, familyID) })

	if err := pool.QueryRow(ctx, `
		INSERT INTO members (family_id, first_name, last_name, member_type)
		VALUES ($1, 'Sam', 'Kowalski', 'adult') RETURNING id::text
	`, familyID).Scan(&memberID); err != nil {
		t.Fatalf("creating member: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO role_assignments (member_id, unit_id, role) VALUES ($1, $2, 'parent')
	`, memberID, unitID); err != nil {
		t.Fatalf("granting role: %v", err)
	}
	return unitID, memberID
}

func TestSignInEntryCarriesItsAddressIntoTheLog(t *testing.T) {
	pool := auditTestPool(t)
	ctx := context.Background()
	unitID, memberID := signInFixture(t, pool, "audit-signin")

	Log(ctx, pool, Entry{
		EntityType: "login",
		EntityID:   memberID,
		ActorID:    &memberID,
		Action:     "sign_in",
		After:      map[string]string{"ip": "203.0.113.7", "site": "audit-signin"},
	})

	entries, err := ForUnitFiltered(ctx, pool, Filter{UnitID: unitID, Limit: 50})
	if err != nil {
		t.Fatalf("reading the log: %v", err)
	}

	var found *LogEntry
	for i := range entries {
		if entries[i].EntityType == "login" {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatal("the sign-in never appears in the unit's activity log — check entityScopeSQL reaches it")
	}
	if found.IPAddress != "203.0.113.7" {
		t.Errorf("IPAddress = %q, want the address recorded in after_state", found.IPAddress)
	}
	if found.ActorName != "Sam Kowalski" {
		t.Errorf("ActorName = %q, want the member who signed in", found.ActorName)
	}
	if found.Action != "sign_in" {
		t.Errorf("Action = %q", found.Action)
	}
}

// Every other kind of entry leaves the column empty rather than showing
// something misleading.
func TestEntriesWithoutAnAddressComeBackBlank(t *testing.T) {
	pool := auditTestPool(t)
	ctx := context.Background()
	unitID, memberID := signInFixture(t, pool, "audit-noaddr")

	Log(ctx, pool, Entry{
		EntityType: "member",
		EntityID:   memberID,
		ActorID:    &memberID,
		Action:     "update",
		After:      map[string]string{"first_name": "Sam"},
	})

	entries, err := ForUnitFiltered(ctx, pool, Filter{UnitID: unitID, Limit: 50})
	if err != nil {
		t.Fatalf("reading the log: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries came back at all")
	}
	for _, e := range entries {
		if e.EntityType == "member" && e.IPAddress != "" {
			t.Errorf("a roster edit reports an IP address of %q", e.IPAddress)
		}
	}
}

// A sign-in belongs to the units that member holds a role in, and to no
// others — the same scoping every other member-keyed entry gets.
func TestASignInStaysInItsOwnUnitsLog(t *testing.T) {
	pool := auditTestPool(t)
	ctx := context.Background()
	unitID, memberID := signInFixture(t, pool, "audit-mine")
	otherUnitID, _ := signInFixture(t, pool, "audit-theirs")

	Log(ctx, pool, Entry{
		EntityType: "login",
		EntityID:   memberID,
		ActorID:    &memberID,
		Action:     "sign_in",
		After:      map[string]string{"ip": "198.51.100.4"},
	})

	mine, err := ForUnitFiltered(ctx, pool, Filter{UnitID: unitID, EntityType: "login", Limit: 50})
	if err != nil {
		t.Fatalf("reading our log: %v", err)
	}
	if len(mine) == 0 {
		t.Fatal("the sign-in is missing from the log of the unit the member belongs to")
	}

	theirs, err := ForUnitFiltered(ctx, pool, Filter{UnitID: otherUnitID, EntityType: "login", Limit: 50})
	if err != nil {
		t.Fatalf("reading the other unit's log: %v", err)
	}
	for _, e := range theirs {
		if e.EntityID == memberID {
			t.Error("one unit's activity log shows another unit's sign-in")
		}
	}
}
