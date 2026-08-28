package roster

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/db"
	"github.com/47-yonkers/scout-site/internal/ledger"
	"github.com/47-yonkers/scout-site/internal/units"
)

// Integration tests for the database-backed half of this package, in the
// same shape as internal/ledger's: they skip unless TEST_DATABASE_URL is
// set, and CI provides one. The pure permission logic is covered without
// a database in scope_test.go.
//
// What's worth testing here is what this package decides, not what it
// stores: who a leader may manage, what roles they may hand out, and
// whether one unit's data can be reached from another. Those are the
// answers the whole admin surface trusts.

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping roster integration tests")
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

var (
	runID   = time.Now().UnixNano()
	counter atomic.Int64
)

type fixture struct {
	pool     *pgxpool.Pool
	unitID   string
	unitType string
	familyID string
	memberID string
}

func newFixture(t *testing.T, unitType string) fixture {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d-%d", runID, counter.Add(1))

	var unitID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO units (slug, name, unit_type, hostname) VALUES ($1, $2, $3, $4) RETURNING id`,
		"u-"+suffix, "Unit "+suffix, unitType, "h-"+suffix+".example.test",
	).Scan(&unitID); err != nil {
		t.Fatalf("creating unit: %v", err)
	}

	var familyID, memberID string
	if err := pool.QueryRow(ctx, `INSERT INTO families (name) VALUES ($1) RETURNING id`, "Fam "+suffix).Scan(&familyID); err != nil {
		t.Fatalf("creating family: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO members (family_id, first_name, last_name, member_type) VALUES ($1, 'Test', 'Adult', 'adult') RETURNING id`,
		familyID,
	).Scan(&memberID); err != nil {
		t.Fatalf("creating member: %v", err)
	}
	return fixture{pool: pool, unitID: unitID, unitType: unitType, familyID: familyID, memberID: memberID}
}

func (f fixture) newMember(t *testing.T, first string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO members (family_id, first_name, last_name, member_type) VALUES ($1, $2, 'Member', 'youth') RETURNING id`,
		f.familyID, first,
	).Scan(&id); err != nil {
		t.Fatalf("creating member %s: %v", first, err)
	}
	return id
}

// TestIsAllowedRole_ScopedLeaderCannotAssignLeadership is the server-side
// half of the privilege-escalation guard. The form only offers a Den
// Leader parent/scout, but a form is not a security boundary — this is
// what stops a hand-crafted POST promoting someone to Treasurer.
func TestIsAllowedRole_ScopedLeaderCannotAssignLeadership(t *testing.T) {
	f := newFixture(t, "pack")
	ctx := context.Background()

	var denID string
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO sub_groups (unit_id, name, sub_group_type) VALUES ($1, 'Bear Den', 'den') RETURNING id`, f.unitID,
	).Scan(&denID); err != nil {
		t.Fatalf("creating den: %v", err)
	}
	scoped := Scope{SubGroupIDs: map[string]bool{denID: true}}

	for _, role := range []string{"parent", "scout"} {
		ok, err := IsAllowedRole(ctx, f.pool, f.unitType, f.unitID, scoped, role)
		if err != nil {
			t.Fatalf("IsAllowedRole(%q): %v", role, err)
		}
		if !ok {
			t.Errorf("a den leader should be able to assign %q", role)
		}
	}
	for _, role := range []string{"cubmaster", "den_leader", "treasurer", "super_admin", "scoutmaster"} {
		ok, err := IsAllowedRole(ctx, f.pool, f.unitType, f.unitID, scoped, role)
		if err != nil {
			t.Fatalf("IsAllowedRole(%q): %v", role, err)
		}
		if ok {
			t.Errorf("a den leader must NOT be able to assign %q", role)
		}
	}
	// A role that doesn't exist at all is refused rather than passed through.
	if ok, _ := IsAllowedRole(ctx, f.pool, f.unitType, f.unitID, scoped, "made_up_role"); ok {
		t.Error("an unknown role slug must not be assignable")
	}
}

// TestIsAllowedRole_CustomRolesAreUnitWideOnly — a custom role can carry
// real capabilities, so a scoped leader must not be able to hand one out
// even though a unit-wide leader can.
func TestIsAllowedRole_CustomRolesAreUnitWideOnly(t *testing.T) {
	f := newFixture(t, "troop")
	ctx := context.Background()

	cr, err := CreateCustomRole(ctx, f.pool, f.unitID, "Committee Chair", []string{units.CapEditContent}, f.memberID)
	if err != nil {
		t.Fatalf("CreateCustomRole: %v", err)
	}

	unitWide := Scope{UnitWide: true}
	if ok, err := IsAllowedRole(ctx, f.pool, f.unitType, f.unitID, unitWide, cr.Slug); err != nil || !ok {
		t.Errorf("a unit-wide leader should be able to assign the custom role (ok=%v err=%v)", ok, err)
	}

	scoped := Scope{SubGroupIDs: map[string]bool{"some-patrol": true}}
	if ok, _ := IsAllowedRole(ctx, f.pool, f.unitType, f.unitID, scoped, cr.Slug); ok {
		t.Error("a scoped leader must NOT be able to assign a custom role")
	}
}

// TestIsAllowedRole_CustomRolesDoNotLeakAcrossUnits — Troop and Pack
// share one install and one custom_roles table. A role created for one
// must not become assignable in the other.
func TestIsAllowedRole_CustomRolesDoNotLeakAcrossUnits(t *testing.T) {
	troop := newFixture(t, "troop")
	pack := newFixture(t, "pack")
	ctx := context.Background()

	cr, err := CreateCustomRole(ctx, troop.pool, troop.unitID, "Troop Historian", []string{units.CapEditContent}, troop.memberID)
	if err != nil {
		t.Fatalf("CreateCustomRole: %v", err)
	}

	unitWide := Scope{UnitWide: true}
	if ok, _ := IsAllowedRole(ctx, troop.pool, troop.unitType, troop.unitID, unitWide, cr.Slug); !ok {
		t.Error("the role should be assignable in the unit that owns it")
	}
	if ok, _ := IsAllowedRole(ctx, pack.pool, pack.unitType, pack.unitID, unitWide, cr.Slug); ok {
		t.Error("a custom role must not be assignable in a different unit")
	}
}

// TestCreateCustomRole_RejectsReservedSlugs — reusing a fixed role's slug
// would make role_assignments.role ambiguous between the system role's
// meaning and the custom one's.
func TestCreateCustomRole_RejectsReservedSlugs(t *testing.T) {
	f := newFixture(t, "troop")
	ctx := context.Background()

	for _, label := range []string{"scoutmaster", "Scoutmaster", "SUPER ADMIN", "treasurer"} {
		if _, err := CreateCustomRole(ctx, f.pool, f.unitID, label, nil, f.memberID); !errors.Is(err, ErrReservedRoleSlug) {
			t.Errorf("CreateCustomRole(%q) = %v, want ErrReservedRoleSlug", label, err)
		}
	}
	// A label with no usable characters is refused too, rather than
	// creating a role with an empty slug.
	if _, err := CreateCustomRole(ctx, f.pool, f.unitID, "!!!", nil, f.memberID); err == nil {
		t.Error("a label that slugifies to nothing should be refused")
	}
}

// TestCreateCustomRole_FiltersUnknownCapabilities — the capability list
// is a closed set. An unrecognised name is dropped rather than stored, so
// a stale or hand-crafted form can't invent one.
func TestCreateCustomRole_FiltersUnknownCapabilities(t *testing.T) {
	f := newFixture(t, "troop")
	ctx := context.Background()

	cr, err := CreateCustomRole(ctx, f.pool, f.unitID, "Mixed Bag",
		[]string{units.CapEditContent, "not_a_capability", "", "manage_everything"}, f.memberID)
	if err != nil {
		t.Fatalf("CreateCustomRole: %v", err)
	}
	if len(cr.Capabilities) != 1 || cr.Capabilities[0] != units.CapEditContent {
		t.Errorf("capabilities = %v, want only %q", cr.Capabilities, units.CapEditContent)
	}
}

// TestCreateCustomRole_AcceptsEveryDeclaredCapability guards the gap this
// test suite was written after finding: units.AllCapabilities and the
// custom_roles CHECK constraint have to agree, or a capability the admin
// form offers fails on insert. approve_expenses was in the Go list but
// not the constraint, which broke the documented "grant it to an ASM
// through a custom role" escape hatch.
func TestCreateCustomRole_AcceptsEveryDeclaredCapability(t *testing.T) {
	f := newFixture(t, "troop")
	ctx := context.Background()

	for i, capability := range units.AllCapabilities {
		label := fmt.Sprintf("Cap Role %d %d", counter.Add(1), i)
		cr, err := CreateCustomRole(ctx, f.pool, f.unitID, label, []string{capability}, f.memberID)
		if err != nil {
			t.Errorf("CreateCustomRole granting %q failed: %v — units.AllCapabilities and the custom_roles CHECK constraint have drifted apart", capability, err)
			continue
		}
		if len(cr.Capabilities) != 1 || cr.Capabilities[0] != capability {
			t.Errorf("granting %q stored %v", capability, cr.Capabilities)
		}
	}
}

// TestManageableMemberIDs_ScopedToTheLeadersOwnDen — a Den Leader editing
// the roster must only reach members in their own den.
func TestManageableMemberIDs_ScopedToTheLeadersOwnDen(t *testing.T) {
	f := newFixture(t, "pack")
	ctx := context.Background()

	var denA, denB string
	for _, d := range []struct {
		name string
		into *string
	}{{"Den A", &denA}, {"Den B", &denB}} {
		if err := f.pool.QueryRow(ctx,
			`INSERT INTO sub_groups (unit_id, name, sub_group_type) VALUES ($1, $2, 'den') RETURNING id`, f.unitID, d.name,
		).Scan(d.into); err != nil {
			t.Fatalf("creating %s: %v", d.name, err)
		}
	}

	inA := f.newMember(t, "Ada")
	inB := f.newMember(t, "Ben")
	if err := AssignRole(ctx, f.pool, inA, f.unitID, &denA, "scout", f.memberID); err != nil {
		t.Fatalf("assigning role in den A: %v", err)
	}
	if err := AssignRole(ctx, f.pool, inB, f.unitID, &denB, "scout", f.memberID); err != nil {
		t.Fatalf("assigning role in den B: %v", err)
	}

	scoped := Scope{SubGroupIDs: map[string]bool{denA: true}}
	manageable, err := ManageableMemberIDs(ctx, f.pool, f.unitID, scoped)
	if err != nil {
		t.Fatalf("ManageableMemberIDs: %v", err)
	}
	if !manageable[inA] {
		t.Error("a den leader should be able to manage a member of their own den")
	}
	if manageable[inB] {
		t.Error("a den leader must NOT be able to manage a member of another den")
	}
}

// TestSubGroupUnitID_DoesNotCrossUnits backs the check that stops a
// leader submitting the other unit's den id on their own unit's form.
func TestSubGroupUnitID_DoesNotCrossUnits(t *testing.T) {
	troop := newFixture(t, "troop")
	ctx := context.Background()

	var patrolID string
	if err := troop.pool.QueryRow(ctx,
		`INSERT INTO sub_groups (unit_id, name, sub_group_type) VALUES ($1, 'Eagle Patrol', 'patrol') RETURNING id`, troop.unitID,
	).Scan(&patrolID); err != nil {
		t.Fatalf("creating patrol: %v", err)
	}

	owner, ok, err := SubGroupUnitID(ctx, troop.pool, patrolID)
	if err != nil || !ok {
		t.Fatalf("SubGroupUnitID: ok=%v err=%v", ok, err)
	}
	if owner != troop.unitID {
		t.Errorf("owner = %q, want the troop that created it", owner)
	}
	if _, ok, _ := SubGroupUnitID(ctx, troop.pool, "00000000-0000-0000-0000-000000000000"); ok {
		t.Error("an unknown sub-group id should report not-found, not a unit")
	}
}

// TestCreateFamilyWithMember_RejectsDuplicateEmail — the login email is
// the account's identity, so a second family must not be able to claim
// one that's taken.
func TestCreateFamilyWithMember_RejectsDuplicateEmail(t *testing.T) {
	f := newFixture(t, "troop")
	ctx := context.Background()

	email := fmt.Sprintf("dup-%d-%d@example.test", runID, counter.Add(1))
	in := NewFamilyInput{FamilyName: "First Family", Email: email, FirstName: "Ann", LastName: "First"}
	if _, _, _, err := CreateFamilyWithMember(ctx, f.pool, in, f.memberID); err != nil {
		t.Fatalf("first create: %v", err)
	}

	dup := NewFamilyInput{FamilyName: "Second Family", Email: email, FirstName: "Bob", LastName: "Second"}
	if _, _, _, err := CreateFamilyWithMember(ctx, f.pool, dup, f.memberID); err == nil {
		t.Error("a second family reusing the same login email should be refused")
	}

	// Same address in different case is the same account.
	upper := NewFamilyInput{FamilyName: "Third", Email: "  " + email[:1] + email[1:], FirstName: "Cy", LastName: "Third"}
	upper.Email = "  " + upper.Email + "  "
	if _, _, _, err := CreateFamilyWithMember(ctx, f.pool, upper, f.memberID); err == nil {
		t.Error("the same email with surrounding whitespace should still be refused")
	}
}

// TestMembersWithCapability finds who can authorize spending — the lookup
// backing the "is there anybody who can approve this?" check before an
// expense is parked waiting for one.
func TestMembersWithCapability(t *testing.T) {
	f := newFixture(t, "troop")
	ctx := context.Background()

	sm := f.newMember(t, "Sam")
	treasurer := f.newMember(t, "Terry")
	parent := f.newMember(t, "Pat")
	for _, a := range []struct{ member, role string }{
		{sm, "scoutmaster"}, {treasurer, "treasurer"}, {parent, "parent"},
	} {
		if err := AssignRole(ctx, f.pool, a.member, f.unitID, nil, a.role, f.memberID); err != nil {
			t.Fatalf("assigning %s: %v", a.role, err)
		}
	}

	got, err := MembersWithCapability(ctx, f.pool, f.unitID, units.CapApproveExpenses)
	if err != nil {
		t.Fatalf("MembersWithCapability: %v", err)
	}
	ids := map[string]bool{}
	for _, m := range got {
		ids[m.ID] = true
	}
	if !ids[sm] {
		t.Error("the Scoutmaster should be able to authorize expenses")
	}
	if ids[treasurer] {
		t.Error("the Treasurer must NOT be able to authorize expenses — that's the separation")
	}
	if ids[parent] {
		t.Error("a parent should not be able to authorize expenses")
	}
}

// TestMembersWithCapability_IncludesCustomRoles — the lookup has to see a
// capability granted through a custom role, not just the fixed ones, or
// the ASM escape hatch wouldn't work.
func TestMembersWithCapability_IncludesCustomRoles(t *testing.T) {
	f := newFixture(t, "troop")
	ctx := context.Background()

	label := fmt.Sprintf("Deputy %d", counter.Add(1))
	cr, err := CreateCustomRole(ctx, f.pool, f.unitID, label, []string{units.CapApproveExpenses}, f.memberID)
	if err != nil {
		t.Fatalf("CreateCustomRole: %v", err)
	}
	deputy := f.newMember(t, "Dee")
	if err := AssignRole(ctx, f.pool, deputy, f.unitID, nil, cr.Slug, f.memberID); err != nil {
		t.Fatalf("assigning custom role: %v", err)
	}

	got, err := MembersWithCapability(ctx, f.pool, f.unitID, units.CapApproveExpenses)
	if err != nil {
		t.Fatalf("MembersWithCapability: %v", err)
	}
	found := false
	for _, m := range got {
		if m.ID == deputy {
			found = true
		}
	}
	if !found {
		t.Error("a member holding the capability through a custom role should be found")
	}
}

// --- Deleting a member -----------------------------------------------------

// TestDeleteMember_RemovesAMemberWithNoHistory covers what delete is
// actually for: a person typed in by mistake, who has done nothing.
func TestDeleteMember_RemovesAMemberWithNoHistory(t *testing.T) {
	f := newFixture(t, "troop")
	ctx := context.Background()
	id := f.newMember(t, "Mistyped")

	if err := AssignRole(ctx, f.pool, id, f.unitID, nil, "scout", f.memberID); err != nil {
		t.Fatalf("assigning a role: %v", err)
	}

	if err := DeleteMember(ctx, f.pool, id, f.memberID); err != nil {
		t.Fatalf("deleting a member with no history should succeed: %v", err)
	}

	var stillThere bool
	if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM members WHERE id = $1)`, id).Scan(&stillThere); err != nil {
		t.Fatalf("re-reading the member: %v", err)
	}
	if stillThere {
		t.Fatal("the member should be gone")
	}

	// The role assignment goes with them (ON DELETE CASCADE), rather than
	// being left pointing at nobody.
	var roles int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM role_assignments WHERE member_id = $1`, id).Scan(&roles); err != nil {
		t.Fatalf("counting role assignments: %v", err)
	}
	if roles != 0 {
		t.Fatalf("their role assignments should be gone too, found %d", roles)
	}

	// And the removal itself is recorded — deleting the person must not
	// also delete the fact that somebody deleted them.
	var logged int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'member' AND entity_id = $1 AND action = 'delete'`, id,
	).Scan(&logged); err != nil {
		t.Fatalf("counting audit entries: %v", err)
	}
	if logged != 1 {
		t.Fatalf("the deletion should be in the Activity Log exactly once, found %d", logged)
	}
}

// TestDeleteMember_RefusesWhenTheyAppearInTheActivityLog is the rule that
// keeps the audit log meaningful: an entry whose actor can be deleted is
// an entry that can be orphaned.
func TestDeleteMember_RefusesWhenTheyAppearInTheActivityLog(t *testing.T) {
	f := newFixture(t, "troop")
	ctx := context.Background()
	id := f.newMember(t, "Busy")

	if _, err := f.pool.Exec(ctx,
		`INSERT INTO audit_log (entity_type, entity_id, actor_id, action) VALUES ('member', $1, $2, 'update')`,
		id, id,
	); err != nil {
		t.Fatalf("seeding an audit entry: %v", err)
	}

	err := DeleteMember(ctx, f.pool, id, f.memberID)
	var hist MemberHasHistoryError
	if !errors.As(err, &hist) {
		t.Fatalf("expected MemberHasHistoryError, got %v", err)
	}
	if !strings.Contains(hist.Reason, "Activity Log") {
		t.Errorf("the reason should name the Activity Log so the leader knows what's holding it, got %q", hist.Reason)
	}

	var stillThere bool
	if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM members WHERE id = $1)`, id).Scan(&stillThere); err != nil {
		t.Fatalf("re-reading the member: %v", err)
	}
	if !stillThere {
		t.Fatal("a refused delete must leave the member exactly where they were")
	}
}

// TestDeleteMember_RefusesWhenTheyHaveMoney is the same rule for the
// ledger, and the one with the worst failure mode: a Scout account with
// postings can't lose its owner without unbalancing books that are
// supposed to balance forever.
func TestDeleteMember_RefusesWhenTheyHaveMoney(t *testing.T) {
	f := newFixture(t, "troop")
	ctx := context.Background()
	id := f.newMember(t, "Funded")

	scout, err := ledger.EnsureScoutAccount(ctx, f.pool, f.unitID, id, "Funded Member", f.memberID)
	if err != nil {
		t.Fatalf("opening a Scout account: %v", err)
	}
	external, err := ledger.EnsureExternalAccount(ctx, f.pool, f.unitID, f.memberID)
	if err != nil {
		t.Fatalf("opening the external account: %v", err)
	}
	if _, err := ledger.PostTransaction(ctx, f.pool, f.unitID, "deposit", "popcorn earnings", f.memberID, []ledger.Posting{
		{AccountID: external.ID, AmountCents: -2500},
		{AccountID: scout.ID, AmountCents: 2500},
	}); err != nil {
		t.Fatalf("posting to the Scout account: %v", err)
	}

	err = DeleteMember(ctx, f.pool, id, f.memberID)
	var hist MemberHasHistoryError
	if !errors.As(err, &hist) {
		t.Fatalf("expected MemberHasHistoryError, got %v", err)
	}
	if !strings.Contains(hist.Reason, "Scout account") {
		t.Errorf("the reason should name the Scout account, got %q", hist.Reason)
	}

	// The postings are the point — they must still be there and still sum
	// to zero across the transaction.
	var sum int64
	if err := f.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(p.amount_cents), 0) FROM ledger_postings p
		JOIN ledger_transactions t ON t.id = p.transaction_id
		WHERE t.unit_id = $1
	`, f.unitID).Scan(&sum); err != nil {
		t.Fatalf("summing postings: %v", err)
	}
	if sum != 0 {
		t.Fatalf("the books should still balance after a refused delete, got %d", sum)
	}
}
