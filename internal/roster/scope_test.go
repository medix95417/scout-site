package roster

import "testing"

// These are the pure, no-database parts of this package — the ones that
// decide what a leader is allowed to do. They run everywhere, including
// CI with no Postgres. The database-backed half is in roster_test.go.

// TestScope_CanManageSubGroup covers the primitive every roster
// permission check is built on.
func TestScope_CanManageSubGroup(t *testing.T) {
	unitWide := Scope{UnitWide: true}
	denLeader := Scope{SubGroupIDs: map[string]bool{"den-3": true}}
	nobody := Scope{}

	cases := []struct {
		name     string
		scope    Scope
		subGroup string
		want     bool
	}{
		{"unit-wide manages their own", unitWide, "den-3", true},
		{"unit-wide manages any den", unitWide, "some-other-den", true},
		{"unit-wide manages the unscoped case", unitWide, "", true},
		{"den leader manages their den", denLeader, "den-3", true},
		{"den leader can NOT manage another den", denLeader, "den-4", false},
		// The empty case matters: a blank sub-group means "unit-wide", and
		// a scoped leader must not get that by submitting an empty field.
		{"den leader can NOT manage the unscoped case", denLeader, "", false},
		{"no roles manages nothing", nobody, "den-3", false},
		{"no roles manages nothing, unscoped", nobody, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.scope.CanManageSubGroup(c.subGroup); got != c.want {
				t.Errorf("CanManageSubGroup(%q) = %v, want %v", c.subGroup, got, c.want)
			}
		})
	}
}

// TestFixedRoleOptions_ScopedLeaderCannotPromote is the privilege-
// escalation guard: a Den Leader manages their own den, and must not be
// able to hand out a leadership role — least of all one that carries real
// capabilities like managing the books.
func TestFixedRoleOptions_ScopedLeaderCannotPromote(t *testing.T) {
	scoped := Scope{SubGroupIDs: map[string]bool{"den-3": true}}

	for _, unitType := range []string{"troop", "pack"} {
		opts := fixedRoleOptions(unitType, scoped)
		got := map[string]bool{}
		for _, o := range opts {
			got[o.Value] = true
		}
		if len(got) != 2 || !got["parent"] || !got["scout"] {
			t.Errorf("%s: a scoped leader was offered %v, want exactly parent and scout", unitType, got)
		}
		for _, forbidden := range []string{
			"scoutmaster", "assistant_scoutmaster", "cubmaster", "den_leader",
			"treasurer", "super_admin", "senior_patrol_leader", "patrol_leader",
		} {
			if got[forbidden] {
				t.Errorf("%s: a scoped leader must not be able to assign %q", unitType, forbidden)
			}
		}
	}
}

// TestFixedRoleOptions_UnitTypeSpecific — a Troop shouldn't be offering
// Cubmaster, and a Pack shouldn't be offering Patrol Leader.
func TestFixedRoleOptions_UnitTypeSpecific(t *testing.T) {
	unitWide := Scope{UnitWide: true}

	troop := map[string]bool{}
	for _, o := range fixedRoleOptions("troop", unitWide) {
		troop[o.Value] = true
	}
	pack := map[string]bool{}
	for _, o := range fixedRoleOptions("pack", unitWide) {
		pack[o.Value] = true
	}

	for _, r := range []string{"scoutmaster", "assistant_scoutmaster", "senior_patrol_leader", "patrol_leader"} {
		if !troop[r] {
			t.Errorf("a troop should offer %q", r)
		}
		if pack[r] {
			t.Errorf("a pack should not offer %q", r)
		}
	}
	for _, r := range []string{"cubmaster", "den_leader"} {
		if !pack[r] {
			t.Errorf("a pack should offer %q", r)
		}
		if troop[r] {
			t.Errorf("a troop should not offer %q", r)
		}
	}
	// Neither unit type hands out super_admin through the roster form.
	if troop["super_admin"] || pack["super_admin"] {
		t.Error("super_admin must never be assignable from the roster page")
	}
}

// TestSlugify covers the label-to-slug conversion custom roles depend on.
// A slug that collided with a fixed role slug would make a role
// assignment ambiguous, which is why CreateCustomRole rejects reserved
// ones — this checks the conversion feeding that check behaves.
func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Committee Chair", "committee_chair"},
		{"  Committee   Chair  ", "committee_chair"},
		{"Advancement-Chair", "advancement_chair"},
		{"Chair (Fundraising)", "chair_fundraising"},
		{"MiXeD CaSe", "mixed_case"},
		{"Troop 47 Historian", "troop_47_historian"},
		{"!!!", ""},
		{"", ""},
		{"___", ""},
		// The case that matters: a label that slugifies onto a reserved
		// slug must produce exactly that slug, so the reserved check sees
		// it rather than letting a near-miss through.
		{"Scout Master", "scout_master"},
		{"scoutmaster", "scoutmaster"},
		{"Super Admin", "super_admin"},
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSlugify_CanReachEveryReservedSlug makes sure the reserved-slug
// check in CreateCustomRole is actually reachable: if slugify could never
// produce a reserved slug, that check would be dead code and a future
// change to either side could silently open a gap.
func TestSlugify_CanReachEveryReservedSlug(t *testing.T) {
	for _, slug := range []string{
		"cubmaster", "den_leader", "scoutmaster", "assistant_scoutmaster",
		"senior_patrol_leader", "patrol_leader", "treasurer", "super_admin",
	} {
		if got := slugify(slug); got != slug {
			t.Errorf("slugify(%q) = %q — a user typing the reserved slug must land on it exactly", slug, got)
		}
	}
}
