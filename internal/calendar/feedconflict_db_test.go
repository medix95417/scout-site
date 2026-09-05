package calendar

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/icalendar"
)

// Conflict handling against a real database.
//
// Everything here is SQL — an overlap test with a slack window, an upsert
// keyed on (feed_id, external_uid), a two-statement replace in one
// transaction. None of it can be exercised without Postgres, and all of
// it fails in ways that look like success: a conflict that isn't detected
// simply imports the duplicate, and a "never import this" that isn't
// remembered simply asks again tomorrow.
//
// Skipped when there's no database, so `go test ./...` stays a pure unit
// run. Point TEST_DATABASE_URL at a scratch database to include it — see
// CLAUDE.md.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run the calendar-import database tests")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connecting to TEST_DATABASE_URL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// fixture makes a unit, a member to act as author, and a feed, all
// removed afterwards.
type fixture struct {
	pool   *pgxpool.Pool
	unitID string
	actor  string
	feed   Feed
}

func newFixture(t *testing.T, name string) fixture {
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

	var familyID, actor string
	if err := pool.QueryRow(ctx,
		`INSERT INTO families (name) VALUES ($1) RETURNING id::text`, name+" family").Scan(&familyID); err != nil {
		t.Fatalf("creating test family: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM families WHERE id = $1`, familyID) })
	if err := pool.QueryRow(ctx, `
		INSERT INTO members (family_id, first_name, last_name, member_type)
		VALUES ($1, 'Test', 'Leader', 'adult') RETURNING id::text
	`, familyID).Scan(&actor); err != nil {
		t.Fatalf("creating test member: %v", err)
	}

	feed, err := AddFeed(ctx, pool, unitID, "Council calendar",
		"https://example.org/"+name+".ics", "members", actor)
	if err != nil {
		t.Fatalf("creating test feed: %v", err)
	}
	return fixture{pool: pool, unitID: unitID, actor: actor, feed: feed}
}

// ourEvent inserts an event the unit typed by hand.
func (f fixture) ourEvent(t *testing.T, title string, start time.Time, hours int) string {
	t.Helper()
	var id string
	end := start.Add(time.Duration(hours) * time.Hour)
	var endArg any = end
	if hours == 0 {
		endArg = nil
	}
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO events (unit_id, title, starts_at, ends_at, visibility, status, created_by)
		VALUES ($1, $2, $3, $4, 'members', 'published', $5) RETURNING id::text
	`, f.unitID, title, start, endArg, f.actor).Scan(&id); err != nil {
		t.Fatalf("inserting %q: %v", title, err)
	}
	return id
}

func (f fixture) eventCount(t *testing.T, where string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM events WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatalf("counting events: %v", err)
	}
	return n
}

var base = time.Date(2027, 5, 8, 9, 0, 0, 0, time.UTC)

func TestConflictIsDetectedForAnOverlappingEvent(t *testing.T) {
	f := newFixture(t, "conflictdetect")
	ctx := context.Background()

	ours := f.ourEvent(t, "Camporee (ours)", base, 6)

	res := reconcile(ctx, f.pool, f.feed, []icalendar.Event{
		// Half an hour after ours starts: the same outing, typed twice.
		{UID: "camporee", Summary: "Spring Camporee", Start: base.Add(30 * time.Minute), End: base.Add(6 * time.Hour)},
		// Three months away: unrelated, must import normally.
		{UID: "dinner", Summary: "Council dinner", Start: base.AddDate(0, 3, 0), End: base.AddDate(0, 3, 0).Add(2 * time.Hour)},
	})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Conflicts != 1 {
		t.Errorf("conflicts = %d, want 1", res.Conflicts)
	}
	if res.Created != 1 {
		t.Errorf("created = %d, want 1 (the unrelated event)", res.Created)
	}

	// The clashing event must NOT be on the calendar.
	if n := f.eventCount(t, `feed_id = $1 AND external_uid = 'camporee'`, f.feed.ID); n != 0 {
		t.Error("the clashing event was imported anyway")
	}
	if n := f.eventCount(t, `id = $1`, ours); n != 1 {
		t.Error("our own event was touched")
	}

	conflicts, err := ConflictsForUnit(ctx, f.pool, f.unitID)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("pending conflicts = %d, want 1", len(conflicts))
	}
	c := conflicts[0]
	if c.Title != "Spring Camporee" || c.ExistingTitle != "Camporee (ours)" {
		t.Errorf("conflict describes the wrong pair: %q vs %q", c.Title, c.ExistingTitle)
	}
	if c.ExistingIsImported {
		t.Error("our hand-typed event is reported as imported")
	}
	if c.FeedName != "Council calendar" {
		t.Errorf("feed name = %q", c.FeedName)
	}
}

// The slack window is what makes the feature useful — two entries for the
// same outing rarely carry identical times — and also what makes it
// annoying if it's too wide. Both edges are checked.
func TestConflictSlackWindowEdges(t *testing.T) {
	f := newFixture(t, "conflictslack")
	ctx := context.Background()

	// A one-hour event at 09:00.
	f.ourEvent(t, "Pack meeting", base, 1)

	// Starts 20 minutes after ours ends — inside the 30-minute slack.
	res := reconcile(ctx, f.pool, f.feed, []icalendar.Event{
		{UID: "near", Summary: "Nearly touching", Start: base.Add(80 * time.Minute), End: base.Add(140 * time.Minute)},
	})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Conflicts != 1 {
		t.Errorf("an event 20 minutes after ours was not flagged (conflicts=%d)", res.Conflicts)
	}

	// Starts four hours later — comfortably outside.
	res = reconcile(ctx, f.pool, f.feed, []icalendar.Event{
		{UID: "far", Summary: "Later that day", Start: base.Add(5 * time.Hour), End: base.Add(6 * time.Hour)},
	})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Conflicts != 0 {
		t.Errorf("an event four hours later was wrongly flagged as a clash")
	}
	if res.Created != 1 {
		t.Errorf("the non-clashing event did not import (created=%d)", res.Created)
	}
}

// A feed refreshed hourly must not pile up twenty copies of the same
// unresolved conflict by morning.
func TestRepeatedRefreshUpdatesRatherThanStacksConflicts(t *testing.T) {
	f := newFixture(t, "conflictstack")
	ctx := context.Background()
	f.ourEvent(t, "Ours", base, 2)

	incoming := icalendar.Event{UID: "same", Summary: "Theirs", Start: base, End: base.Add(2 * time.Hour)}
	for i := 0; i < 3; i++ {
		if res := reconcile(ctx, f.pool, f.feed, []icalendar.Event{incoming}); res.Err != nil {
			t.Fatal(res.Err)
		}
	}

	n, err := CountConflictsForUnit(ctx, f.pool, f.unitID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("three refreshes produced %d conflicts, want 1", n)
	}

	// A retitled event updates the pending row rather than adding one.
	incoming.Summary = "Theirs, renamed"
	if res := reconcile(ctx, f.pool, f.feed, []icalendar.Event{incoming}); res.Err != nil {
		t.Fatal(res.Err)
	}
	conflicts, err := ConflictsForUnit(ctx, f.pool, f.unitID)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || conflicts[0].Title != "Theirs, renamed" {
		t.Fatalf("pending conflict not updated: %+v", conflicts)
	}
}

func TestResolveKeepBoth(t *testing.T) {
	f := newFixture(t, "conflictboth")
	ctx := context.Background()
	ours := f.ourEvent(t, "Ours", base, 2)

	reconcile(ctx, f.pool, f.feed, []icalendar.Event{{UID: "theirs", Summary: "Theirs", Start: base, End: base.Add(2 * time.Hour)}})
	c := onlyConflict(t, f)

	if err := ResolveConflict(ctx, f.pool, c.ID, f.unitID, ResolveImport, f.actor); err != nil {
		t.Fatal(err)
	}
	if n := f.eventCount(t, `id = $1`, ours); n != 1 {
		t.Error("our event disappeared")
	}
	if n := f.eventCount(t, `feed_id = $1 AND external_uid = 'theirs'`, f.feed.ID); n != 1 {
		t.Error("theirs was not imported")
	}
	if n, _ := CountConflictsForUnit(ctx, f.pool, f.unitID); n != 0 {
		t.Error("the conflict is still pending")
	}

	// And the next refresh treats it as an ordinary imported event.
	res := reconcile(ctx, f.pool, f.feed, []icalendar.Event{{UID: "theirs", Summary: "Theirs", Start: base, End: base.Add(2 * time.Hour)}})
	if res.Conflicts != 0 || res.Updated != 1 {
		t.Errorf("after keeping both, refresh gave conflicts=%d updated=%d, want 0 and 1", res.Conflicts, res.Updated)
	}
}

func TestResolveKeepOursIsRememberedForever(t *testing.T) {
	f := newFixture(t, "conflictskip")
	ctx := context.Background()
	ours := f.ourEvent(t, "Ours", base, 2)

	incoming := icalendar.Event{UID: "theirs", Summary: "Theirs", Start: base, End: base.Add(2 * time.Hour)}
	reconcile(ctx, f.pool, f.feed, []icalendar.Event{incoming})
	c := onlyConflict(t, f)

	if err := ResolveConflict(ctx, f.pool, c.ID, f.unitID, ResolveSkip, f.actor); err != nil {
		t.Fatal(err)
	}
	if n := f.eventCount(t, `id = $1`, ours); n != 1 {
		t.Fatal("skipping deleted our own event")
	}
	if n := f.eventCount(t, `feed_id = $1`, f.feed.ID); n != 0 {
		t.Fatal("skipping imported it anyway")
	}

	// The whole point: the next refresh must not ask again.
	res := reconcile(ctx, f.pool, f.feed, []icalendar.Event{incoming})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Ignored != 1 {
		t.Errorf("ignored = %d, want 1 — the decision was forgotten", res.Ignored)
	}
	if res.Conflicts != 0 {
		t.Errorf("conflicts = %d — it asked again", res.Conflicts)
	}
	if n, _ := CountConflictsForUnit(ctx, f.pool, f.unitID); n != 0 {
		t.Error("a new conflict was recorded despite the decision")
	}
}

func TestResolveTakeTheirsSwapsInOneStep(t *testing.T) {
	f := newFixture(t, "conflictreplace")
	ctx := context.Background()
	ours := f.ourEvent(t, "Placeholder", base, 0) // no end time — exercises assumedDuration

	reconcile(ctx, f.pool, f.feed, []icalendar.Event{
		{UID: "real", Summary: "The Real Thing", Start: base, End: base.Add(3 * time.Hour)},
	})
	c := onlyConflict(t, f)

	if err := ResolveConflict(ctx, f.pool, c.ID, f.unitID, ResolveReplace, f.actor); err != nil {
		t.Fatal(err)
	}
	if n := f.eventCount(t, `id = $1`, ours); n != 0 {
		t.Error("the placeholder was not deleted")
	}
	if n := f.eventCount(t, `feed_id = $1 AND external_uid = 'real' AND title = 'The Real Thing'`, f.feed.ID); n != 1 {
		t.Fatal("our event was deleted but theirs was not imported — the replace was not atomic")
	}
	if n, _ := CountConflictsForUnit(ctx, f.pool, f.unitID); n != 0 {
		t.Error("the conflict is still pending")
	}
}

// Deleting the event a conflict is about removes the reason to hold the
// import back, so the next refresh should just bring it in.
func TestDeletingOurEventClearsTheConflict(t *testing.T) {
	f := newFixture(t, "conflictcascade")
	ctx := context.Background()
	ours := f.ourEvent(t, "Ours", base, 2)

	incoming := icalendar.Event{UID: "theirs", Summary: "Theirs", Start: base, End: base.Add(2 * time.Hour)}
	reconcile(ctx, f.pool, f.feed, []icalendar.Event{incoming})
	if n, _ := CountConflictsForUnit(ctx, f.pool, f.unitID); n != 1 {
		t.Fatal("no conflict to begin with")
	}

	if _, err := f.pool.Exec(ctx, `DELETE FROM events WHERE id = $1`, ours); err != nil {
		t.Fatal(err)
	}
	if n, _ := CountConflictsForUnit(ctx, f.pool, f.unitID); n != 0 {
		t.Fatal("the conflict outlived the event it was about")
	}

	res := reconcile(ctx, f.pool, f.feed, []icalendar.Event{incoming})
	if res.Created != 1 {
		t.Errorf("created = %d, want 1 — with nothing to clash with it should import", res.Created)
	}
}

// A conflict the source stops offering is not a decision anybody needs.
func TestWithdrawnEventClearsItsConflict(t *testing.T) {
	f := newFixture(t, "conflictwithdrawn")
	ctx := context.Background()
	f.ourEvent(t, "Ours", base, 2)

	reconcile(ctx, f.pool, f.feed, []icalendar.Event{{UID: "theirs", Summary: "Theirs", Start: base, End: base.Add(2 * time.Hour)}})
	if n, _ := CountConflictsForUnit(ctx, f.pool, f.unitID); n != 1 {
		t.Fatal("no conflict to begin with")
	}

	// The feed no longer offers it.
	if res := reconcile(ctx, f.pool, f.feed, nil); res.Err != nil {
		t.Fatal(res.Err)
	}
	if n, _ := CountConflictsForUnit(ctx, f.pool, f.unitID); n != 0 {
		t.Error("a withdrawn event is still waiting on a decision")
	}
}

// One unit must not be able to rule on another's conflict.
func TestConflictsAreUnitScoped(t *testing.T) {
	f := newFixture(t, "conflictscope")
	other := newFixture(t, "conflictscopeother")
	ctx := context.Background()

	f.ourEvent(t, "Ours", base, 2)
	reconcile(ctx, f.pool, f.feed, []icalendar.Event{{UID: "theirs", Summary: "Theirs", Start: base, End: base.Add(2 * time.Hour)}})
	c := onlyConflict(t, f)

	if _, err := GetConflict(ctx, f.pool, c.ID, other.unitID); err == nil {
		t.Error("one unit could read another's conflict")
	}
	if err := ResolveConflict(ctx, f.pool, c.ID, other.unitID, ResolveReplace, other.actor); err == nil {
		t.Fatal("one unit could delete another unit's event by resolving its conflict")
	}
	if n, _ := CountConflictsForUnit(ctx, f.pool, f.unitID); n != 1 {
		t.Error("the conflict was resolved by the wrong unit")
	}
}

func onlyConflict(t *testing.T, f fixture) Conflict {
	t.Helper()
	conflicts, err := ConflictsForUnit(context.Background(), f.pool, f.unitID)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 conflict, got %d", len(conflicts))
	}
	return conflicts[0]
}
