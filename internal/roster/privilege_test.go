package roster

import (
	"context"
	"testing"

	"github.com/47-yonkers/scout-site/internal/units"
)

// The privilege ceiling, against a real database: what a unit-wide
// leader may hand out is bounded by what they hold, and a login is
// measured by everything it carries across both units.
//
// These are the escalations that were possible before the ceiling
// existed, each written as the exact call the handler makes.

func assign(t *testing.T, f fixture, memberID, role string) {
	t.Helper()
	if err := AssignRole(context.Background(), f.pool, memberID, f.unitID, nil, role, f.memberID); err != nil {
		t.Fatalf("assigning %s: %v", role, err)
	}
}

func allowed(t *testing.T, f fixture, actor units.Capabilities, role string) bool {
	t.Helper()
	ok, err := IsAllowedRole(context.Background(), f.pool, f.unitType, f.unitID, Scope{UnitWide: true}, actor, role)
	if err != nil {
		t.Fatalf("IsAllowedRole(%q): %v", role, err)
	}
	return ok
}

// TestIsAllowedRole_UnitWideLeaderCannotHandOutMoreThanTheyHold is the
// finding itself: an Assistant Scoutmaster is unit-wide, and unit-wide
// used to mean the whole fixed set including Treasurer.
func TestIsAllowedRole_UnitWideLeaderCannotHandOutMoreThanTheyHold(t *testing.T) {
	f := newFixture(t, "troop")
	asm := units.CapabilitiesOf(units.CapEditContent, units.CapApproveSubmissions)

	for _, role := range []string{"treasurer", "scoutmaster"} {
		if allowed(t, f, asm, role) {
			t.Errorf("an Assistant Scoutmaster must not be able to assign %q", role)
		}
	}
	for _, role := range []string{"assistant_scoutmaster", "senior_patrol_leader", "patrol_leader", "parent", "scout"} {
		if !allowed(t, f, asm, role) {
			t.Errorf("an Assistant Scoutmaster should be able to assign %q", role)
		}
	}

	scoutmaster := units.CapabilitiesOf(units.CapEditContent, units.CapApproveSubmissions, units.CapApproveExpenses)
	if !allowed(t, f, scoutmaster, "scoutmaster") {
		t.Error("a Scoutmaster should be able to appoint another Scoutmaster")
	}
	if allowed(t, f, scoutmaster, "treasurer") {
		t.Error("a Scoutmaster must not be able to make someone Treasurer")
	}

	admin := units.CapabilitiesOf(units.CapSuperAdmin)
	for _, role := range []string{"treasurer", "scoutmaster", "assistant_scoutmaster", "parent"} {
		if !allowed(t, f, admin, role) {
			t.Errorf("an Admin should be able to assign %q", role)
		}
	}
}

// TestIsAllowedRole_CustomRoleGrantingSuperAdminNeedsSuperAdmin: a custom
// role is offered to every unit-wide leader by scope, and one that
// grants super_admin was the shortest path to the top.
func TestIsAllowedRole_CustomRoleGrantingSuperAdminNeedsSuperAdmin(t *testing.T) {
	f := newFixture(t, "pack")
	ctx := context.Background()

	top, err := CreateCustomRole(ctx, f.pool, f.unitID, "Webmaster", []string{units.CapSuperAdmin}, f.memberID)
	if err != nil {
		t.Fatalf("creating custom role: %v", err)
	}
	books, err := CreateCustomRole(ctx, f.pool, f.unitID, "Committee Chair", []string{units.CapManageLedger}, f.memberID)
	if err != nil {
		t.Fatalf("creating custom role: %v", err)
	}
	helper, err := CreateCustomRole(ctx, f.pool, f.unitID, "Newsletter Helper", []string{units.CapEditContent}, f.memberID)
	if err != nil {
		t.Fatalf("creating custom role: %v", err)
	}

	cubmaster := units.CapabilitiesOf(units.CapEditContent, units.CapApproveExpenses)
	if allowed(t, f, cubmaster, top.Slug) {
		t.Error("a Cubmaster must not be able to hand out a custom role that grants super_admin")
	}
	if allowed(t, f, cubmaster, books.Slug) {
		t.Error("a Cubmaster must not be able to hand out a custom role that grants manage_ledger")
	}
	if !allowed(t, f, cubmaster, helper.Slug) {
		t.Error("a Cubmaster should be able to hand out a custom role granting only what they hold")
	}
	if !allowed(t, f, units.CapabilitiesOf(units.CapSuperAdmin), top.Slug) {
		t.Error("an Admin should be able to hand out the super_admin custom role")
	}

	// The dropdown and the check agree: what is refused is not offered.
	opts, err := AllowedRoles(ctx, f.pool, f.unitType, f.unitID, Scope{UnitWide: true}, cubmaster)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range opts {
		if o.Value == top.Slug || o.Value == books.Slug || o.Value == "treasurer" {
			t.Errorf("the form offered %q to a Cubmaster", o.Value)
		}
	}
}

// TestIsAllowedRole_MeasuresARoleByWhatItGrantsHere: a unit can change
// what a built-in role grants. The ceiling has to read the override, or
// a Den Leader role quietly given manage_ledger is handed out by anyone
// who could hand out the default one.
func TestIsAllowedRole_MeasuresARoleByWhatItGrantsHere(t *testing.T) {
	f := newFixture(t, "pack")
	ctx := context.Background()
	cubmaster := units.CapabilitiesOf(units.CapEditContent, units.CapApproveExpenses)

	if !allowed(t, f, cubmaster, "den_leader") {
		t.Fatal("by default a Cubmaster can appoint a Den Leader")
	}
	if err := units.SetSystemRoleCapabilities(ctx, f.pool, f.unitID, "den_leader",
		[]string{units.CapEditContent, units.CapManageLedger}, f.memberID); err != nil {
		t.Fatalf("overriding den_leader: %v", err)
	}
	if allowed(t, f, cubmaster, "den_leader") {
		t.Error("once Den Leader grants manage_ledger here, a Cubmaster must not be able to hand it out")
	}
}

// TestCapabilitiesAcrossUnits: the login is one object, the member row is
// one object, and both span units. A Troop leader must see the Pack
// Admin as an Admin.
func TestCapabilitiesAcrossUnits(t *testing.T) {
	troop := newFixture(t, "troop")
	pack := newFixture(t, "pack")
	ctx := context.Background()

	// One family with a parent on the Troop side and the same person as
	// super_admin on the Pack side.
	parent := troop.newMember(t, "Robin")
	assign(t, troop, parent, "parent")
	if err := AssignRole(ctx, pack.pool, parent, pack.unitID, nil, "super_admin", pack.memberID); err != nil {
		t.Fatalf("assigning super_admin in the pack: %v", err)
	}

	member, err := MemberCapabilitiesAcrossUnits(ctx, troop.pool, parent)
	if err != nil {
		t.Fatal(err)
	}
	if !member.Has(units.CapSuperAdmin) {
		t.Errorf("member ceiling missed the Pack super_admin role: %v", member)
	}

	fam, err := FamilyCapabilitiesAcrossUnits(ctx, troop.pool, troop.familyID)
	if err != nil {
		t.Fatal(err)
	}
	if !fam.Has(units.CapSuperAdmin) {
		t.Errorf("family ceiling missed the Pack super_admin role: %v", fam)
	}

	// And the ceiling then refuses a Troop Scoutmaster.
	scoutmaster := units.CapabilitiesOf(units.CapEditContent, units.CapApproveSubmissions, units.CapApproveExpenses)
	if scoutmaster.Covers(fam) {
		t.Error("a Troop Scoutmaster must not cover a family whose parent is the Pack's Admin")
	}

	// A family with nothing but parents is within anyone's reach.
	plain := newFixture(t, "troop")
	assign(t, plain, plain.newMember(t, "Sam"), "parent")
	held, err := FamilyCapabilitiesAcrossUnits(ctx, plain.pool, plain.familyID)
	if err != nil {
		t.Fatal(err)
	}
	if !units.CapabilitiesOf(units.CapEditContent).Covers(held) {
		t.Errorf("a plain family should be within a Den Leader's ceiling, got %v", held)
	}
}
