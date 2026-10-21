package roster

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/47-yonkers/scout-site/internal/units"
)

// SubGroupIDsWithCapability is the "where" of a capability that means "this,
// but only for your own group" — email_group. These cover the cases that
// would turn a Patrol Leader into someone who can write to every patrol, or
// quietly stop a den leader being able to write to their own.

func (f fixture) group(t *testing.T, name, groupType string) string {
	t.Helper()
	g, err := CreateSubGroup(context.Background(), f.pool, f.unitID, name, groupType, f.memberID)
	if err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	return g.ID
}

func (f fixture) assign(t *testing.T, member string, group *string, role string) {
	t.Helper()
	if err := AssignRole(context.Background(), f.pool, member, f.unitID, group, role, f.memberID); err != nil {
		t.Fatalf("assigning %s: %v", role, err)
	}
}

func (f fixture) emailGroups(t *testing.T, familyID string, memberID *string) []string {
	t.Helper()
	got, err := SubGroupIDsWithCapability(context.Background(), f.pool, familyID, memberID, f.unitID, units.CapEmailGroup)
	if err != nil {
		t.Fatalf("SubGroupIDsWithCapability: %v", err)
	}
	sort.Strings(got)
	return got
}

func sorted(ids ...string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAPatrolLeaderCanEmailOnlyTheirOwnPatrol(t *testing.T) {
	f := newFixture(t, "troop")
	eagle := f.group(t, "Eagle Patrol", "patrol")
	f.group(t, "Wolf Patrol", "patrol")

	pl := f.newMember(t, "Pat")
	f.assign(t, pl, &eagle, "patrol_leader")

	if got := f.emailGroups(t, f.familyID, &pl); !same(got, sorted(eagle)) {
		t.Errorf("a Patrol Leader of Eagle can email %v, want only Eagle %v", got, eagle)
	}
}

func TestADenLeaderCanEmailTheirDenAndNoOther(t *testing.T) {
	f := newFixture(t, "pack")
	bear := f.group(t, "Bear Den", "den")
	f.group(t, "Wolf Den", "den")

	leader := f.newMember(t, "Dee")
	f.assign(t, leader, &bear, "den_leader")

	if got := f.emailGroups(t, f.familyID, &leader); !same(got, sorted(bear)) {
		t.Errorf("a Den Leader of Bear can email %v, want only Bear %v", got, bear)
	}
}

// An assignment with no group is the unit-wide kind, and counting it as
// "every group" here would turn a Patrol Leader whose assignment was left
// without a patrol into someone who can write to all of them. A unit-wide
// leader's reach is Scope.UnitWide's job, as it is for everything else.
func TestARoleWithNoGroupContributesNoGroup(t *testing.T) {
	f := newFixture(t, "troop")
	f.group(t, "Eagle Patrol", "patrol")

	loose := f.newMember(t, "Lou")
	f.assign(t, loose, nil, "patrol_leader") // assigned to the unit, not to a patrol

	if got := f.emailGroups(t, f.familyID, &loose); len(got) != 0 {
		t.Errorf("a Patrol Leader assigned to no patrol can email %v, want nothing", got)
	}
}

func TestARoleWithoutTheCapabilityContributesNothing(t *testing.T) {
	f := newFixture(t, "troop")
	eagle := f.group(t, "Eagle Patrol", "patrol")

	scout := f.newMember(t, "Sky")
	parent := f.newMember(t, "Par")
	spl := f.newMember(t, "Spencer")
	f.assign(t, scout, &eagle, "scout")
	f.assign(t, parent, &eagle, "parent")
	f.assign(t, spl, &eagle, "senior_patrol_leader") // not given email_group by default

	for name, m := range map[string]string{"a Scout": scout, "a parent": parent, "the SPL": spl} {
		m := m
		if got := f.emailGroups(t, f.familyID, &m); len(got) != 0 {
			t.Errorf("%s in Eagle can email %v, want nothing", name, got)
		}
	}
}

func TestAnotherUnitsGroupIsNeverReturned(t *testing.T) {
	a := newFixture(t, "troop")
	b := newFixture(t, "troop")
	bGroup := b.group(t, "Hawk Patrol", "patrol")

	// A role in unit A that points at a group of unit B: not something the
	// app creates, but the query must not be the thing that trusts that.
	pl := a.newMember(t, "Pat")
	if _, err := a.pool.Exec(context.Background(),
		`INSERT INTO role_assignments (member_id, unit_id, sub_group_id, role) VALUES ($1, $2, $3, 'patrol_leader')`,
		pl, a.unitID, bGroup); err != nil {
		t.Fatalf("seeding a cross-unit assignment: %v", err)
	}

	if got := a.emailGroups(t, a.familyID, &pl); len(got) != 0 {
		t.Errorf("a group of another unit came back: %v", got)
	}
}

func TestAFoldedGroupIsNoLongerSomewhereToSendFrom(t *testing.T) {
	f := newFixture(t, "troop")
	eagle := f.group(t, "Eagle Patrol", "patrol")
	pl := f.newMember(t, "Pat")
	f.assign(t, pl, &eagle, "patrol_leader")

	if err := SetSubGroupActive(context.Background(), f.pool, eagle, false, f.memberID); err != nil {
		t.Fatalf("folding the patrol: %v", err)
	}
	if got := f.emailGroups(t, f.familyID, &pl); len(got) != 0 {
		t.Errorf("an inactive patrol is still emailable: %v", got)
	}
}

func TestADeactivatedMemberHoldsNothing(t *testing.T) {
	f := newFixture(t, "troop")
	eagle := f.group(t, "Eagle Patrol", "patrol")
	pl := f.newMember(t, "Pat")
	f.assign(t, pl, &eagle, "patrol_leader")

	if _, err := f.pool.Exec(context.Background(), `UPDATE members SET active = false WHERE id = $1`, pl); err != nil {
		t.Fatal(err)
	}
	if got := f.emailGroups(t, f.familyID, &pl); len(got) != 0 {
		t.Errorf("an inactive member still holds %v", got)
	}
}

// The shared-login rule: a family-wide login carries the household's roles,
// and an individual login is judged on its own member only — so a parent's
// authority is never lent to a child's own login, and the reverse.
func TestSharedAndIndividualLoginsAreJudgedDifferently(t *testing.T) {
	f := newFixture(t, "troop")
	eagle := f.group(t, "Eagle Patrol", "patrol")
	wolf := f.group(t, "Wolf Patrol", "patrol")

	// f.memberID is the fixture's adult; give them Wolf, and the youth Eagle.
	f.assign(t, f.memberID, &wolf, "patrol_leader")
	youth := f.newMember(t, "Yan")
	f.assign(t, youth, &eagle, "patrol_leader")

	if got := f.emailGroups(t, f.familyID, nil); !same(got, sorted(eagle, wolf)) {
		t.Errorf("the family-wide login can email %v, want both %v", got, sorted(eagle, wolf))
	}
	adult := f.memberID
	if got := f.emailGroups(t, f.familyID, &adult); !same(got, sorted(wolf)) {
		t.Errorf("the adult's own login can email %v, want only Wolf", got)
	}
	if got := f.emailGroups(t, f.familyID, &youth); !same(got, sorted(eagle)) {
		t.Errorf("the youth's own login can email %v, want only Eagle", got)
	}
}

// What a unit has decided about a built-in role is what counts, not the
// code's defaults — in both directions.
func TestAUnitsOwnChoicesDecideWhoCanEmail(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "troop")
	eagle := f.group(t, "Eagle Patrol", "patrol")
	pl := f.newMember(t, "Pat")
	f.assign(t, pl, &eagle, "patrol_leader")
	spl := f.newMember(t, "Spencer")
	f.assign(t, spl, &eagle, "senior_patrol_leader")

	// This unit does not want its Patrol Leaders sending email…
	if err := units.SetSystemRoleCapabilities(ctx, f.pool, f.unitID, "patrol_leader",
		[]string{units.CapSubmitForApproval}, f.memberID); err != nil {
		t.Fatalf("override: %v", err)
	}
	// …and does want its Senior Patrol Leader to.
	if err := units.SetSystemRoleCapabilities(ctx, f.pool, f.unitID, "senior_patrol_leader",
		[]string{units.CapSubmitForApproval, units.CapEmailGroup}, f.memberID); err != nil {
		t.Fatalf("override: %v", err)
	}

	if got := f.emailGroups(t, f.familyID, &pl); len(got) != 0 {
		t.Errorf("the unit took email away from Patrol Leaders but one can still email %v", got)
	}
	if got := f.emailGroups(t, f.familyID, &spl); !same(got, sorted(eagle)) {
		t.Errorf("the unit gave the SPL email but they can email %v, want Eagle", got)
	}
}

func TestACustomRoleCanBeGivenTheCapability(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "troop")
	eagle := f.group(t, "Eagle Patrol", "patrol")
	wolf := f.group(t, "Wolf Patrol", "patrol")

	cr, err := CreateCustomRole(ctx, f.pool, f.unitID, fmt.Sprintf("Patrol Scribe %d", counter.Add(1)),
		[]string{units.CapEmailGroup}, f.memberID)
	if err != nil {
		t.Fatalf("CreateCustomRole: %v", err)
	}
	scribe := f.newMember(t, "Sam")
	f.assign(t, scribe, &eagle, cr.Slug)
	_ = wolf

	if got := f.emailGroups(t, f.familyID, &scribe); !same(got, sorted(eagle)) {
		t.Errorf("a custom role with email_group assigned to Eagle can email %v, want only Eagle", got)
	}
}
