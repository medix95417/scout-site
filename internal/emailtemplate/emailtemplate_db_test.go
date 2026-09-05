package emailtemplate

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Saved templates against a real database. The behaviour worth pinning
// down is the upsert: "Save as template" under a name that already exists
// has to replace it, because that is what the person clicking it means —
// and it must not replace the same-named template of the other kind, or
// somebody's newsletter layout vanishes when they save a recruiting
// letter.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping email template integration tests")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// testUnit makes a throwaway unit plus the member every audit entry
// needs as its actor — created_by and audit_log.actor_id are both real
// foreign keys, so a placeholder string is not an option.
func testUnit(t *testing.T, pool *pgxpool.Pool, name string) (unitID, actorID string) {
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

func TestSaveReplacesByNameWithinOneKind(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	unitID, actor := testUnit(t, pool, "tplsave")

	first, err := Save(ctx, pool, unitID, KindProspect, "Autumn letter", "Join us", "<p>v1</p>", actor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Save(ctx, pool, unitID, KindProspect, "Autumn letter", "Join us now", "<p>v2</p>", actor)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Error("saving under an existing name created a second template rather than replacing it")
	}
	if second.Body != "<p>v2</p>" || second.Subject != "Join us now" {
		t.Errorf("the template was not replaced: %+v", second)
	}

	// The same name under the other kind is a different template.
	nl, err := Save(ctx, pool, unitID, KindNewsletter, "Autumn letter", "Newsletter", "<p>n</p>", actor)
	if err != nil {
		t.Fatal(err)
	}
	if nl.ID == first.ID {
		t.Fatal("saving a newsletter template overwrote the prospect template of the same name")
	}

	list, err := ListForUnit(ctx, pool, unitID, KindProspect)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("the prospect list has %d templates, want 1", len(list))
	}
	if list[0].Kind != KindProspect {
		t.Errorf("the prospect list contains a %s template", list[0].Kind)
	}
}

func TestTemplatesAreUnitScoped(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	a, actorA := testUnit(t, pool, "tplscopea")
	b, actorB := testUnit(t, pool, "tplscopeb")

	mine, err := Save(ctx, pool, a, KindProspect, "Shared name", "A", "<p>a</p>", actorA)
	if err != nil {
		t.Fatal(err)
	}
	// The same name in the other unit is a different template entirely.
	theirs, err := Save(ctx, pool, b, KindProspect, "Shared name", "B", "<p>b</p>", actorB)
	if err != nil {
		t.Fatal(err)
	}
	if theirs.ID == mine.ID {
		t.Fatal("two units share one template row")
	}

	if _, err := Get(ctx, pool, mine.ID, b); !errors.Is(err, ErrNotFound) {
		t.Errorf("one unit read another's template: %v", err)
	}
	if err := Delete(ctx, pool, mine.ID, b, actorB); !errors.Is(err, ErrNotFound) {
		t.Errorf("one unit deleted another's template: %v", err)
	}
	if _, err := Get(ctx, pool, mine.ID, a); err != nil {
		t.Errorf("the template was deleted despite the refusal: %v", err)
	}
}

func TestSaveRejectsBadInput(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	unitID, actor := testUnit(t, pool, "tplbad")

	for _, tc := range []struct {
		name              string
		kind, tname, subj string
	}{
		{"unknown kind", "spam", "x", "y"},
		{"blank name", KindProspect, "   ", "y"},
		{"name too long", KindProspect, string(make([]byte, MaxName+1)), "y"},
		{"subject too long", KindProspect, "ok", string(make([]byte, MaxSubject+1))},
	} {
		if _, err := Save(ctx, pool, unitID, tc.kind, tc.tname, tc.subj, "<p>x</p>", actor); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}
