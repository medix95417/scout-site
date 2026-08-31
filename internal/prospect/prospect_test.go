package prospect

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
)

// Integration tests, same TEST_DATABASE_URL harness as internal/ledger
// and internal/roster. The rules worth protecting here are enforced
// partly in Go and partly by CHECK constraints in migration 0042, and a
// fake pool would exercise neither.

var (
	runID   = time.Now().UnixNano()
	counter atomic.Int64
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping prospect integration tests")
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

func newUnit(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	suffix := fmt.Sprintf("%d-%d", runID, counter.Add(1))
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO units (slug, name, unit_type, hostname) VALUES ($1, $2, 'troop', $3) RETURNING id`,
		"p-"+suffix, "Troop "+suffix, "p-"+suffix+".example.test",
	).Scan(&id); err != nil {
		t.Fatalf("creating test unit: %v", err)
	}
	return id
}

func validSubmission(unitID string) New {
	age := 9
	return New{
		UnitID: unitID, ParentName: "Jamie Rivera", ParentEmail: "jamie@example.com",
		ParentPhone: "555-0100", ChildName: "Sam Rivera", ChildAge: &age,
		ChildGrade: "4th", ChildSchool: "Riverside Elementary", Message: "Saw you at the school fair.",
	}
}

func TestCreate_StoresAnEnquiry(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	unitID := newUnit(t, pool)

	p, err := Create(ctx, pool, validSubmission(unitID))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.Status != StatusNew {
		t.Errorf("a new enquiry should start as %q, got %q", StatusNew, p.Status)
	}
	if !p.Open() {
		t.Error("a new enquiry should count as open")
	}
	if p.ChildAge == nil || *p.ChildAge != 9 {
		t.Errorf("age didn't round-trip, got %v", p.ChildAge)
	}
}

// TestCreate_RejectsWhatAPersonCanFix covers the validation a stranger
// hits. Each case must come back as ErrInvalid — the handler turns those
// into a sentence on the form, and anything else into a 500.
func TestCreate_RejectsWhatAPersonCanFix(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	unitID := newUnit(t, pool)

	tooLong := strings.Repeat("x", MaxName+1)
	badAge := 99
	for name, mutate := range map[string]func(*New){
		"no parent name":   func(n *New) { n.ParentName = "   " },
		"no email":         func(n *New) { n.ParentEmail = "" },
		"email with no @":  func(n *New) { n.ParentEmail = "not-an-address" },
		"no child name":    func(n *New) { n.ChildName = "" },
		"name too long":    func(n *New) { n.ParentName = tooLong },
		"school too long":  func(n *New) { n.ChildSchool = strings.Repeat("y", MaxSchool+1) },
		"message too long": func(n *New) { n.Message = strings.Repeat("z", MaxMessage+1) },
		"implausible age":  func(n *New) { n.ChildAge = &badAge },
	} {
		in := validSubmission(unitID)
		mutate(&in)
		if _, err := Create(ctx, pool, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", name, err)
		}
	}
}

// TestCreate_AgeIsOptional — most of the form is, and a family who
// doesn't want to give an age shouldn't be stopped from enquiring.
func TestCreate_AgeIsOptional(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	unitID := newUnit(t, pool)

	in := validSubmission(unitID)
	in.ChildAge, in.ChildGrade, in.ChildSchool, in.ParentPhone, in.Message = nil, "", "", "", ""
	p, err := Create(ctx, pool, in)
	if err != nil {
		t.Fatalf("an enquiry with only the required fields should be accepted: %v", err)
	}
	if p.ChildAge != nil {
		t.Errorf("age should stay unset, got %v", *p.ChildAge)
	}
}

// TestUpdateStatus_TracksAndAudits is the tracking half: the status
// moves, the note sticks, and the change is in the Activity Log — a
// prospect nobody can see the history of isn't trackable.
func TestUpdateStatus_TracksAndAudits(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	unitID := newUnit(t, pool)

	p, err := Create(ctx, pool, validSubmission(unitID))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// An actor has to be a real member for the audit FK to hold.
	var familyID, actorID string
	if err := pool.QueryRow(ctx, `INSERT INTO families (name) VALUES ('Leader Family') RETURNING id`).Scan(&familyID); err != nil {
		t.Fatalf("creating a family: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO members (family_id, first_name, last_name, member_type) VALUES ($1, 'Lee', 'Leader', 'adult') RETURNING id`,
		familyID).Scan(&actorID); err != nil {
		t.Fatalf("creating a member: %v", err)
	}

	updated, err := UpdateStatus(ctx, pool, unitID, p.ID, StatusContacted, "Called 3 Sep, coming to a meeting", actorID)
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if updated.Status != StatusContacted || updated.Notes == "" {
		t.Fatalf("status/notes didn't stick: %+v", updated)
	}
	if !updated.Open() {
		t.Error("a contacted enquiry is still open — it hasn't been resolved either way")
	}

	var logged int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'prospect' AND entity_id = $1`, p.ID,
	).Scan(&logged); err != nil {
		t.Fatalf("counting audit entries: %v", err)
	}
	if logged != 1 {
		t.Errorf("the change should be in the Activity Log once, found %d", logged)
	}

	// Joined and declined both close it — the open list is "still needs
	// someone", not "hasn't joined".
	for _, closed := range []string{StatusJoined, StatusDeclined} {
		if Open(closed) {
			t.Errorf("%q should not count as open", closed)
		}
	}
}

func TestUpdateStatus_RejectsAnUnknownStatus(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	unitID := newUnit(t, pool)
	p, err := Create(ctx, pool, validSubmission(unitID))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := UpdateStatus(ctx, pool, unitID, p.ID, "definitely-joining", "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for an unknown status, got %v", err)
	}
}

// TestScopedToItsUnit is this codebase's standing cross-tenant check.
// An enquiry names a child, their age and their school; the other unit
// on this install has no business reading it.
func TestScopedToItsUnit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	mine, theirs := newUnit(t, pool), newUnit(t, pool)

	p, err := Create(ctx, pool, validSubmission(mine))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := Get(ctx, pool, theirs, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another unit should not be able to read this enquiry, got %v", err)
	}
	if _, err := UpdateStatus(ctx, pool, theirs, p.ID, StatusJoined, "", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("another unit should not be able to update it, got %v", err)
	}
	if err := Delete(ctx, pool, theirs, p.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("another unit should not be able to delete it, got %v", err)
	}

	list, err := ListForUnit(ctx, pool, theirs, false)
	if err != nil {
		t.Fatalf("ListForUnit: %v", err)
	}
	for _, other := range list {
		if other.ID == p.ID {
			t.Fatal("another unit's list should not include this enquiry")
		}
	}
}

// TestListForUnit_OpenOnlyFiltersResolved backs the default admin view.
func TestListForUnit_OpenOnlyFiltersResolved(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	unitID := newUnit(t, pool)

	open, err := Create(ctx, pool, validSubmission(unitID))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	done, err := Create(ctx, pool, validSubmission(unitID))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE prospects SET status = 'joined' WHERE id = $1`, done.ID); err != nil {
		t.Fatalf("closing one: %v", err)
	}

	openList, err := ListForUnit(ctx, pool, unitID, true)
	if err != nil {
		t.Fatalf("ListForUnit(openOnly): %v", err)
	}
	if len(openList) != 1 || openList[0].ID != open.ID {
		t.Fatalf("open-only should return just the unresolved one, got %d", len(openList))
	}

	all, err := ListForUnit(ctx, pool, unitID, false)
	if err != nil {
		t.Fatalf("ListForUnit(all): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("showing all should return both, got %d", len(all))
	}

	n, err := CountOpenForUnit(ctx, pool, unitID)
	if err != nil {
		t.Fatalf("CountOpenForUnit: %v", err)
	}
	if n != 1 {
		t.Errorf("open count = %d, want 1", n)
	}
}

// TestStatusesMatchTheDatabaseEnum guards the drift that would otherwise
// only show up as a failed UPDATE in production: Statuses here and the
// prospect_status enum in migration 0042 have to name the same set.
func TestStatusesMatchTheDatabaseEnum(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx, `SELECT unnest(enum_range(NULL::prospect_status))::text`)
	if err != nil {
		t.Fatalf("reading the enum: %v", err)
	}
	defer rows.Close()

	inDB := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scanning: %v", err)
		}
		inDB[v] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating: %v", err)
	}

	inGo := map[string]bool{}
	for _, s := range Statuses {
		inGo[s.Value] = true
		if !inDB[s.Value] {
			t.Errorf("status %q is offered in the UI but isn't in the prospect_status enum", s.Value)
		}
	}
	for v := range inDB {
		if !inGo[v] {
			t.Errorf("status %q exists in the database but is never offered, so nothing can ever reach it", v)
		}
	}
}
