package units

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/db"
)

// Built-in role overrides against a real database.
//
// The pure tests in system_roles_test.go cover the rules that live in Go.
// What they can't reach is the part that decides whether the feature
// works at all: that an override actually changes the answer
// CapabilitiesForRoles gives — the function every permission check on the
// site goes through — and that resetting to the default removes the row
// rather than freezing a copy of it.
//
// Skipped without TEST_DATABASE_URL, same harness as internal/roster.
func dbPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping units integration tests")
	}
	// db.Connect + db.Migrate, not a bare pgxpool.New: `go test ./...`
	// runs packages concurrently, so this package may be the first to
	// reach the database and cannot assume another one has migrated it.
	// Connecting without migrating raced the package that does — the
	// symptom was "relation \"units\" does not exist" here and deadlocks
	// between this package's DELETEs and another's in-flight ALTER TABLE.
	// db.Migrate takes an advisory lock, so concurrent callers serialize
	// and every one of them returns to a fully migrated schema.
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

// dbUnit makes a throwaway unit and a member to attribute changes to.
func dbUnit(t *testing.T, pool *pgxpool.Pool, name string) (unitID, actorID string) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `
		INSERT INTO units (slug, name, unit_type, hostname)
		VALUES ($1, $1, 'pack', $1 || '.test.invalid') RETURNING id::text
	`, name).Scan(&unitID); err != nil {
		t.Fatalf("creating test unit: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM units WHERE id = $1`, unitID) })

	var familyID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO families (name) VALUES ($1) RETURNING id::text`, name).Scan(&familyID); err != nil {
		t.Fatalf("creating test family: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM families WHERE id = $1`, familyID) })
	if err := pool.QueryRow(ctx, `
		INSERT INTO members (family_id, first_name, last_name, member_type)
		VALUES ($1, 'Test', 'Leader', 'adult') RETURNING id::text
	`, familyID).Scan(&actorID); err != nil {
		t.Fatalf("creating test member: %v", err)
	}
	return unitID, actorID
}

// The whole point of the feature: what a unit says a role grants has to
// be what the permission check believes.
func TestOverrideChangesTheAnswerPermissionChecksGet(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	unitID, actor := dbUnit(t, pool, "roleoverride")

	caps, err := CapabilitiesForRoles(ctx, pool, unitID, []string{"den_leader"})
	if err != nil {
		t.Fatal(err)
	}
	if CanApproveExpenses(caps) {
		t.Fatal("a Den Leader can authorize spending by default — this test is testing nothing")
	}

	if err := SetSystemRoleCapabilities(ctx, pool, unitID, "den_leader",
		[]string{CapEditContent, CapApproveExpenses}, actor); err != nil {
		t.Fatal(err)
	}
	caps, err = CapabilitiesForRoles(ctx, pool, unitID, []string{"den_leader"})
	if err != nil {
		t.Fatal(err)
	}
	if !CanApproveExpenses(caps) {
		t.Fatal("the override did not reach CanApproveExpenses")
	}
	if !CanEditUnitContent(caps) {
		t.Error("the override dropped a capability that was ticked")
	}

	// Revoking works in the same direction.
	if err := SetSystemRoleCapabilities(ctx, pool, unitID, "den_leader", nil, actor); err != nil {
		t.Fatal(err)
	}
	caps, _ = CapabilitiesForRoles(ctx, pool, unitID, []string{"den_leader"})
	if CanEditUnitContent(caps) {
		t.Error("a role overridden to grant nothing still grants something")
	}
}

// An override is one unit's decision. The other unit must be unaffected —
// this is the same tenancy rule as role_assignments.
func TestOverrideIsScopedToOneUnit(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	packA, actorA := dbUnit(t, pool, "roleoverridea")
	packB, _ := dbUnit(t, pool, "roleoverrideb")

	if err := SetSystemRoleCapabilities(ctx, pool, packA, "den_leader",
		[]string{CapEditContent, CapManageLedger}, actorA); err != nil {
		t.Fatal(err)
	}

	capsB, err := CapabilitiesForRoles(ctx, pool, packB, []string{"den_leader"})
	if err != nil {
		t.Fatal(err)
	}
	if CanManageLedger(capsB) {
		t.Fatal("one unit's role change leaked into the other unit")
	}

	rolesB, err := SystemRolesForUnit(ctx, pool, packB)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rolesB {
		if r.Slug == "den_leader" && r.Overridden {
			t.Error("the other unit's admin page shows the role as changed")
		}
	}
}

// Storing a delta, not a copy: a unit that clicks its way back to the
// defaults must end up following the code again, so a later change to
// those defaults still reaches them.
func TestResetRemovesTheOverrideRow(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	unitID, actor := dbUnit(t, pool, "rolereset")

	if err := SetSystemRoleCapabilities(ctx, pool, unitID, "treasurer",
		[]string{CapManageLedger, CapEditContent}, actor); err != nil {
		t.Fatal(err)
	}
	if n := overrideCount(t, pool, unitID, "treasurer"); n != 1 {
		t.Fatalf("override row count = %d, want 1", n)
	}

	// Back to the default, but written in a different order and with a
	// duplicate — it is still the default set.
	if err := SetSystemRoleCapabilities(ctx, pool, unitID, "treasurer",
		[]string{CapManageLedger, CapManageLedger}, actor); err != nil {
		t.Fatal(err)
	}
	if n := overrideCount(t, pool, unitID, "treasurer"); n != 0 {
		t.Errorf("resetting to the default left %d override row(s) — the unit is now pinned to a copy of today's default", n)
	}

	roles, err := SystemRolesForUnit(ctx, pool, unitID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range roles {
		if r.Slug == "treasurer" && r.Overridden {
			t.Error("the role still shows as changed after being reset")
		}
	}
}

// "Who can authorize spending" is built from a different query than the
// permission check. They have to agree, or an approver is missing from a
// dropdown while the site says they have the right.
func TestApproverListAgreesWithThePermissionCheck(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	unitID, actor := dbUnit(t, pool, "roleapprovers")

	// Grant it to a role that doesn't have it, and take it from one that does.
	if err := SetSystemRoleCapabilities(ctx, pool, unitID, "den_leader",
		[]string{CapEditContent, CapApproveExpenses}, actor); err != nil {
		t.Fatal(err)
	}
	if err := SetSystemRoleCapabilities(ctx, pool, unitID, "cubmaster",
		[]string{CapEditContent}, actor); err != nil {
		t.Fatal(err)
	}

	slugs, err := SystemRolesWithCapabilityForUnit(ctx, pool, unitID, CapApproveExpenses)
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, s := range slugs {
		has[s] = true
	}
	if !has["den_leader"] {
		t.Errorf("den_leader can authorize spending here but is missing from %v", slugs)
	}
	if has["cubmaster"] {
		t.Errorf("cubmaster can no longer authorize spending here but is still listed in %v", slugs)
	}

	// Cross-check each listed role against the check that actually runs.
	for _, slug := range slugs {
		caps, err := CapabilitiesForRoles(ctx, pool, unitID, []string{slug})
		if err != nil {
			t.Fatal(err)
		}
		if !CanApproveExpenses(caps) {
			t.Errorf("%s is listed as an approver but CanApproveExpenses says no", slug)
		}
	}
}

// The refusal has to hold at the database too, not just in the Go guard —
// nothing may write an override that disarms the only role able to undo it.
func TestSuperAdminCannotBeDisarmedAgainstTheDatabase(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	unitID, actor := dbUnit(t, pool, "rolelockout")

	if err := SetSystemRoleCapabilities(ctx, pool, unitID, "super_admin",
		[]string{CapEditContent}, actor); !errors.Is(err, ErrCannotDisarmSuperAdmin) {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if n := overrideCount(t, pool, unitID, "super_admin"); n != 0 {
		t.Error("the refused edit wrote an override row anyway")
	}
	caps, err := CapabilitiesForRoles(ctx, pool, unitID, []string{"super_admin"})
	if err != nil {
		t.Fatal(err)
	}
	if !IsSuperAdmin(caps) {
		t.Fatal("the unit is locked out of its own settings")
	}

	// Adding capabilities to super_admin is fine, as long as its own stays.
	if err := SetSystemRoleCapabilities(ctx, pool, unitID, "super_admin",
		append(DefaultCapabilitiesForRole("super_admin"), CapSubmitForApproval), actor); err != nil {
		t.Fatalf("a legitimate super_admin edit was refused: %v", err)
	}
}

func overrideCount(t *testing.T, pool *pgxpool.Pool, unitID, slug string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM role_capability_overrides WHERE unit_id = $1 AND role_slug = $2`,
		unitID, slug).Scan(&n); err != nil {
		t.Fatalf("counting overrides: %v", err)
	}
	return n
}
