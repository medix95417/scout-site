package roster

import (
	"context"
	"errors"
	"testing"

	"github.com/47-yonkers/scout-site/internal/units"
)

// Editing a custom role, against a real database.
//
// Two things here can only fail at the database. The capability list is
// written to a text[] column with a CHECK constraint mirroring
// units.AllCapabilities, and the two fell out of step once before —
// approve_expenses was in Go and not in the constraint, so the one
// documented way to let an Assistant Scoutmaster authorize spending
// failed on insert (migration 0039). And the slug must survive a rename,
// because it is the value every role_assignments row holds.

func TestUpdateCustomRoleKeepsTheSlugAndChangesTheRest(t *testing.T) {
	f := newFixture(t, "pack")
	ctx := context.Background()

	cr, err := CreateCustomRole(ctx, f.pool, f.unitID, "Committee Chair",
		[]string{units.CapEditContent}, f.memberID)
	if err != nil {
		t.Fatal(err)
	}

	// Somebody holds the role. A rename must not lose them.
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO role_assignments (unit_id, member_id, role) VALUES ($1, $2, $3)`,
		f.unitID, f.memberID, cr.Slug); err != nil {
		t.Fatalf("assigning the custom role: %v", err)
	}

	updated, err := UpdateCustomRole(ctx, f.pool, cr.ID, f.unitID, "Committee Chairperson",
		[]string{units.CapEditContent, units.CapApproveExpenses}, f.memberID)
	if err != nil {
		t.Fatalf("updating a custom role with approve_expenses failed — Go and the CHECK constraint disagree: %v", err)
	}
	if updated.Label != "Committee Chairperson" {
		t.Errorf("label = %q", updated.Label)
	}
	if updated.Slug != cr.Slug {
		t.Fatalf("slug changed from %q to %q — everyone holding the role would silently lose it", cr.Slug, updated.Slug)
	}

	// The person holding it now has the new capability, through the same
	// lookup every permission check uses.
	roles, err := units.RolesForMemberInUnit(ctx, f.pool, f.memberID, f.unitID)
	if err != nil {
		t.Fatal(err)
	}
	caps, err := units.CapabilitiesForRoles(ctx, f.pool, f.unitID, roles)
	if err != nil {
		t.Fatal(err)
	}
	if !units.CanApproveExpenses(caps) {
		t.Error("the edited capability did not reach the permission check")
	}

	// A blank name is refused rather than saved.
	if _, err := UpdateCustomRole(ctx, f.pool, cr.ID, f.unitID, "   ", nil, f.memberID); err == nil {
		t.Error("a blank role name was accepted")
	}

	// Junk capabilities are dropped rather than reaching the constraint.
	cleaned, err := UpdateCustomRole(ctx, f.pool, cr.ID, f.unitID, "Committee Chairperson",
		[]string{units.CapEditContent, "drop_everything", units.CapEditContent}, f.memberID)
	if err != nil {
		t.Fatalf("a hand-crafted capability value reached the database: %v", err)
	}
	if len(cleaned.Capabilities) != 1 || cleaned.Capabilities[0] != units.CapEditContent {
		t.Errorf("capabilities not filtered/deduplicated: %v", cleaned.Capabilities)
	}
}

// One unit's roles are not another's to edit, even though a family can
// span both units.
func TestCustomRoleEditsAreUnitScoped(t *testing.T) {
	a := newFixture(t, "pack")
	b := newFixture(t, "troop")
	ctx := context.Background()

	cr, err := CreateCustomRole(ctx, a.pool, a.unitID, "Committee Chair",
		[]string{units.CapEditContent}, a.memberID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := GetCustomRole(ctx, a.pool, cr.ID, b.unitID); !errors.Is(err, ErrCustomRoleNotFound) {
		t.Errorf("one unit read another's custom role: %v", err)
	}
	if _, err := UpdateCustomRole(ctx, a.pool, cr.ID, b.unitID, "Hijacked",
		[]string{units.CapSuperAdmin}, b.memberID); !errors.Is(err, ErrCustomRoleNotFound) {
		t.Fatalf("one unit granted itself super_admin by editing another unit's role: %v", err)
	}

	after, err := GetCustomRole(ctx, a.pool, cr.ID, a.unitID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Label != "Committee Chair" || len(after.Capabilities) != 1 {
		t.Errorf("the role was modified despite the refusal: %+v", after)
	}
}
