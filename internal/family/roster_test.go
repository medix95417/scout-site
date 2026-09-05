package family

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The roster is one line per person. That is the whole of the rule, and
// it is only checkable against a real database, because what breaks it is
// a GROUP BY: aggregate on a column that varies within a member and
// Postgres hands back a row per distinct value, each carrying only the
// subset of that member's roles belonging to it.
//
// It broke exactly that way. sub_groups.name was in the GROUP BY, so a
// Den Leader who is also a parent — one role scoped to a den, one not —
// appeared twice: once as "Bear Den 3, den_leader" and once as
// "—, parent". Neither line was wrong on its own, which is why it took a
// person noticing their own name twice.
//
// Skipped without TEST_DATABASE_URL, same harness as internal/roster.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping roster integration tests")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type rosterFixture struct {
	pool     *pgxpool.Pool
	unitID   string
	familyID string
}

func newRosterFixture(t *testing.T, name string) rosterFixture {
	t.Helper()
	ctx := context.Background()
	pool := testPool(t)

	var unitID string
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

	return rosterFixture{pool: pool, unitID: unitID, familyID: familyID}
}

func (f rosterFixture) member(t *testing.T, first, memberType string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO members (family_id, first_name, last_name, member_type, active)
		VALUES ($1, $2, 'Tester', $3, true) RETURNING id::text
	`, f.familyID, first, memberType).Scan(&id); err != nil {
		t.Fatalf("creating member %s: %v", first, err)
	}
	return id
}

func (f rosterFixture) subGroup(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO sub_groups (unit_id, name, sub_group_type) VALUES ($1, $2, 'den') RETURNING id::text
	`, f.unitID, name).Scan(&id); err != nil {
		t.Fatalf("creating sub-group %s: %v", name, err)
	}
	return id
}

func (f rosterFixture) assign(t *testing.T, memberID, role, subGroupID string) {
	t.Helper()
	var sg any
	if subGroupID != "" {
		sg = subGroupID
	}
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO role_assignments (unit_id, member_id, role, sub_group_id) VALUES ($1, $2, $3, $4)
	`, f.unitID, memberID, role, sg); err != nil {
		t.Fatalf("assigning %s: %v", role, err)
	}
}

func entryFor(t *testing.T, entries []RosterEntry, memberID string) RosterEntry {
	t.Helper()
	var found []RosterEntry
	for _, e := range entries {
		if e.ID == memberID {
			found = append(found, e)
		}
	}
	if len(found) == 0 {
		t.Fatalf("member %s is missing from the roster entirely", memberID)
	}
	if len(found) > 1 {
		var lines []string
		for _, e := range found {
			lines = append(lines, e.FirstName+" — "+e.SubGroupName+" — "+strings.Join(e.Roles, ","))
		}
		t.Fatalf("member appears on %d roster lines, want 1:\n  %s", len(found), strings.Join(lines, "\n  "))
	}
	return found[0]
}

// The case that broke: one role scoped to a den, one not.
func TestRosterGivesOneLinePerMemberWithMixedScopedRoles(t *testing.T) {
	f := newRosterFixture(t, "rosterline")
	ctx := context.Background()

	dana := f.member(t, "Dana", "adult")
	bears := f.subGroup(t, "Bear Den 3")
	f.assign(t, dana, "den_leader", bears)
	f.assign(t, dana, "parent", "")

	entries, err := RosterForUnit(ctx, f.pool, f.unitID)
	if err != nil {
		t.Fatal(err)
	}

	e := entryFor(t, entries, dana)
	roles := append([]string(nil), e.Roles...)
	sort.Strings(roles)
	if len(roles) != 2 || roles[0] != "den_leader" || roles[1] != "parent" {
		t.Errorf("the single line shows roles %v, want both den_leader and parent", e.Roles)
	}
	if e.SubGroupName != "Bear Den 3" {
		t.Errorf("sub-group = %q, want %q", e.SubGroupName, "Bear Den 3")
	}
}

// A member in two dens gets both named on the one line, rather than two
// lines each naming one.
func TestRosterJoinsMultipleSubGroupsOnOneLine(t *testing.T) {
	f := newRosterFixture(t, "rostertwodens")
	ctx := context.Background()

	sam := f.member(t, "Sam", "adult")
	f.assign(t, sam, "den_leader", f.subGroup(t, "Bear Den 3"))
	f.assign(t, sam, "parent", f.subGroup(t, "Wolf Den 1"))

	e := entryFor(t, mustRoster(t, ctx, f), sam)
	if !strings.Contains(e.SubGroupName, "Bear Den 3") || !strings.Contains(e.SubGroupName, "Wolf Den 1") {
		t.Errorf("sub-groups = %q, want both dens named", e.SubGroupName)
	}
}

// The ordinary cases must not have changed.
func TestRosterUnchangedForSingleRoleMembers(t *testing.T) {
	f := newRosterFixture(t, "rostersimple")
	ctx := context.Background()

	scout := f.member(t, "Alex", "youth")
	f.assign(t, scout, "scout", f.subGroup(t, "Bear Den 3"))
	plain := f.member(t, "Jo", "adult")
	f.assign(t, plain, "parent", "")

	entries := mustRoster(t, ctx, f)

	s := entryFor(t, entries, scout)
	if s.SubGroupName != "Bear Den 3" || len(s.Roles) != 1 || s.Roles[0] != "scout" {
		t.Errorf("scout entry = %+v", s)
	}
	p := entryFor(t, entries, plain)
	if p.SubGroupName != "" {
		t.Errorf("an unscoped member has sub-group %q, want empty", p.SubGroupName)
	}
}

// A member whose ONLY role here is super_admin still stays off the
// roster, and one who holds it alongside a real role still appears once.
func TestRosterSuperAdminHandlingSurvivesTheRegrouping(t *testing.T) {
	f := newRosterFixture(t, "rostersuper")
	ctx := context.Background()

	opsOnly := f.member(t, "Ops", "adult")
	f.assign(t, opsOnly, "super_admin", "")

	leader := f.member(t, "Casey", "adult")
	f.assign(t, leader, "super_admin", "")
	f.assign(t, leader, "cubmaster", "")

	entries := mustRoster(t, ctx, f)
	for _, e := range entries {
		if e.ID == opsOnly {
			t.Error("a super_admin-only member is on the family-facing roster")
		}
	}
	c := entryFor(t, entries, leader)
	if len(c.Roles) != 2 {
		t.Errorf("a Cubmaster who is also super_admin shows roles %v, want both", c.Roles)
	}
}

// InactiveRosterForUnit had the identical GROUP BY and the identical bug,
// so a deactivated Den Leader would have been offered for reactivation
// twice.
func TestInactiveRosterAlsoGivesOneLinePerMember(t *testing.T) {
	f := newRosterFixture(t, "rosterinactive")
	ctx := context.Background()

	gone := f.member(t, "Robin", "adult")
	f.assign(t, gone, "den_leader", f.subGroup(t, "Bear Den 3"))
	f.assign(t, gone, "parent", "")
	if _, err := f.pool.Exec(ctx, `UPDATE members SET active = false WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}

	entries, err := InactiveRosterForUnit(ctx, f.pool, f.unitID)
	if err != nil {
		t.Fatal(err)
	}
	e := entryFor(t, entries, gone)
	if len(e.Roles) != 2 {
		t.Errorf("the single inactive line shows roles %v, want both", e.Roles)
	}
}

func mustRoster(t *testing.T, ctx context.Context, f rosterFixture) []RosterEntry {
	t.Helper()
	entries, err := RosterForUnit(ctx, f.pool, f.unitID)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
