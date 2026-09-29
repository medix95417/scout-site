package prospect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
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
	// A real unit gets the default statuses from migration 0048 or from
	// seed.sql; one conjured straight into the table gets them here, so
	// a test isn't working against a unit with no workflow at all.
	if err := EnsureDefaults(context.Background(), pool, id); err != nil {
		t.Fatalf("seeding default statuses: %v", err)
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
	// "Open" is now whichever statuses the unit hasn't marked closed,
	// so it is asked of the list rather than of the row.
	if !inOpenList(t, ctx, pool, unitID, p.ID) {
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

	updated, err := UpdateStatus(ctx, pool, unitID, p.ID, StatusContacted, "", "Called 3 Sep, coming to a meeting", actorID)
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if updated.Status != StatusContacted || updated.Notes == "" {
		t.Fatalf("status/notes didn't stick: %+v", updated)
	}
	if !inOpenList(t, ctx, pool, unitID, p.ID) {
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
	// someone", not "hasn't joined". Which statuses close an enquiry is
	// a per-unit flag now, and these two carry it by default.
	for _, closed := range []string{StatusJoined, StatusDeclined} {
		if _, err := UpdateStatus(ctx, pool, unitID, p.ID, closed, "", "", actorID); err != nil {
			t.Fatalf("UpdateStatus(%q): %v", closed, err)
		}
		if inOpenList(t, ctx, pool, unitID, p.ID) {
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
	if _, err := UpdateStatus(ctx, pool, unitID, p.ID, "definitely-joining", "", "", ""); !errors.Is(err, ErrInvalid) {
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
	if _, err := UpdateStatus(ctx, pool, theirs, p.ID, StatusJoined, "", "", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("another unit should not be able to update it, got %v", err)
	}
	if err := Delete(ctx, pool, theirs, p.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("another unit should not be able to delete it, got %v", err)
	}

	list, err := ListForUnit(ctx, pool, theirs, false, OrderNewest)
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

	openList, err := ListForUnit(ctx, pool, unitID, true, OrderNewest)
	if err != nil {
		t.Fatalf("ListForUnit(openOnly): %v", err)
	}
	if len(openList) != 1 || openList[0].ID != open.ID {
		t.Fatalf("open-only should return just the unresolved one, got %d", len(openList))
	}

	all, err := ListForUnit(ctx, pool, unitID, false, OrderNewest)
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

// The default statuses are written down in three places — this
// package, migration 0048 and seed.sql — and they have to agree.
//
// This replaces a test that compared the Go list against the
// prospect_status enum. That enum is gone (the statuses are per-unit
// rows now), but the drift it guarded only moved: a unit created by
// the migration, a unit created by the seed, and a unit given its
// defaults at runtime must all start with the same workflow, or which
// one you are looking at depends on how your database came to exist.
func TestDefaultStatusesAgreeEverywhere(t *testing.T) {
	inGo := map[string]string{}
	for _, l := range DefaultStatuses() {
		inGo[l.Value] = l.Label
	}
	if len(inGo) == 0 {
		t.Fatal("there are no default statuses at all")
	}

	for _, file := range []string{
		"../db/migrations/0048_prospect_labels.sql",
		"../db/migrations/seed.sql",
	} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		found := defaultStatusRows.FindAllStringSubmatch(string(body), -1)
		if len(found) == 0 {
			t.Errorf("%s no longer seeds any default statuses", file)
			continue
		}
		inSQL := map[string]string{}
		for _, m := range found {
			inSQL[m[1]] = m[2]
		}
		for value, label := range inGo {
			switch got, ok := inSQL[value]; {
			case !ok:
				t.Errorf("%s doesn't seed the default status %q", file, value)
			case got != label:
				t.Errorf("%s calls %q %q, this package calls it %q", file, value, got, label)
			}
		}
		for value := range inSQL {
			if _, ok := inGo[value]; !ok {
				t.Errorf("%s seeds %q, which this package doesn't know about", file, value)
			}
		}
	}
}

// defaultStatusRows matches a ('value', 'Label', closed, order) tuple in
// the VALUES block both SQL files use to seed the defaults.
var defaultStatusRows = regexp.MustCompile(`\('([a-z_-]+)',\s*'([^']+)',\s*(?:true|false),\s*\d+\)`)

// inOpenList reports whether a prospect shows in the default view — the
// one filtered to enquiries still wanting a reply. Which statuses those
// are is the unit's own choice now (prospect_labels.closed), so this is
// a database question and no longer a property of the row.
func inOpenList(t *testing.T, ctx context.Context, pool *pgxpool.Pool, unitID, id string) bool {
	t.Helper()
	open, err := ListForUnit(ctx, pool, unitID, true, OrderNewest)
	if err != nil {
		t.Fatalf("listing open prospects: %v", err)
	}
	for _, p := range open {
		if p.ID == id {
			return true
		}
	}
	return false
}
