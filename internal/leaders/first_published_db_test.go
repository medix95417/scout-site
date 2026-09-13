package leaders

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/db"
)

// The homepage shows "the first three leaders" and links to the page
// showing the rest. That is only true if both read the same order, and
// the order lives in SQL — so it is checked against a real database
// rather than asserted about the Go around it.

func leadersTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping leaders integration tests")
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

func leadersUnit(t *testing.T, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	ctx := context.Background()
	var unitID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO units (slug, name, unit_type, hostname)
		VALUES ($1, $1, 'troop', $1 || '.test.invalid') RETURNING id::text
	`, slug).Scan(&unitID); err != nil {
		t.Fatalf("creating unit: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM units WHERE id = $1`, unitID) })
	return unitID
}

func TestFirstPublishedForUnitTakesTheTopOfTheLeadersPage(t *testing.T) {
	pool := leadersTestPool(t)
	ctx := context.Background()
	unitID := leadersUnit(t, pool, "first-published")

	// Inserted out of order, and with a draft in the middle, so neither
	// insertion order nor "the first N rows" can pass by accident.
	seed := []struct {
		name, status string
		sortOrder    int
	}{
		{"Dana Fourth", "published", 4},
		{"Sam First", "published", 1},
		{"Robin Draft", "draft", 2},
		{"Alex Third", "published", 3},
		{"Casey Second", "published", 2},
	}
	for _, s := range seed {
		if _, err := pool.Exec(ctx, `
			INSERT INTO leaders (unit_id, name, role_title, bio, sort_order, status)
			VALUES ($1, $2, 'Leader', 'A bio.', $3, $4)
		`, unitID, s.name, s.sortOrder, s.status); err != nil {
			t.Fatalf("seeding %s: %v", s.name, err)
		}
	}

	page, err := ListPublishedForUnit(ctx, pool, unitID)
	if err != nil {
		t.Fatalf("ListPublishedForUnit: %v", err)
	}
	first, err := FirstPublishedForUnit(ctx, pool, unitID, 3)
	if err != nil {
		t.Fatalf("FirstPublishedForUnit: %v", err)
	}

	if len(first) != 3 {
		t.Fatalf("got %d leaders, want 3", len(first))
	}
	// The promise: these are the page's own first three, in its order.
	for i, l := range first {
		if l.Name != page[i].Name {
			t.Errorf("position %d: homepage shows %q, the leaders page shows %q", i, l.Name, page[i].Name)
		}
	}
	if first[0].Name != "Sam First" || first[1].Name != "Casey Second" || first[2].Name != "Alex Third" {
		t.Errorf("wrong three or wrong order: %q, %q, %q", first[0].Name, first[1].Name, first[2].Name)
	}
	// A draft is not on the leaders page, so it is not on the homepage.
	for _, l := range first {
		if l.Name == "Robin Draft" {
			t.Error("an unpublished profile reached the homepage")
		}
	}
}

func TestFirstPublishedForUnitEdgeCases(t *testing.T) {
	pool := leadersTestPool(t)
	ctx := context.Background()

	// A unit with nothing published: an empty result is what tells the
	// homepage not to offer the link to /leaders.
	empty := leadersUnit(t, pool, "no-leaders")
	if _, err := pool.Exec(ctx, `
		INSERT INTO leaders (unit_id, name, sort_order, status) VALUES ($1, 'Draft Only', 1, 'draft')
	`, empty); err != nil {
		t.Fatalf("seeding draft: %v", err)
	}
	got, err := FirstPublishedForUnit(ctx, pool, empty, 3)
	if err != nil {
		t.Fatalf("FirstPublishedForUnit: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a unit with only drafts returned %d leaders, want none", len(got))
	}

	// Fewer published than asked for: show what there is, don't pad.
	one := leadersUnit(t, pool, "one-leader")
	if _, err := pool.Exec(ctx, `
		INSERT INTO leaders (unit_id, name, sort_order, status) VALUES ($1, 'Only Leader', 1, 'published')
	`, one); err != nil {
		t.Fatalf("seeding leader: %v", err)
	}
	got, err = FirstPublishedForUnit(ctx, pool, one, 3)
	if err != nil {
		t.Fatalf("FirstPublishedForUnit: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d leaders, want the single published one", len(got))
	}

	// Another unit's leaders are not this unit's, limit or no limit.
	other := leadersUnit(t, pool, "other-unit")
	if _, err := pool.Exec(ctx, `
		INSERT INTO leaders (unit_id, name, sort_order, status) VALUES ($1, 'Their Leader', 1, 'published')
	`, other); err != nil {
		t.Fatalf("seeding other unit: %v", err)
	}
	got, err = FirstPublishedForUnit(ctx, pool, one, 3)
	if err != nil {
		t.Fatalf("FirstPublishedForUnit: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Only Leader" {
		t.Errorf("the other unit's leader leaked in: %+v", got)
	}

	// n of 0 or less asks for nothing and must not mean "no LIMIT".
	for _, n := range []int{0, -1} {
		got, err := FirstPublishedForUnit(ctx, pool, one, n)
		if err != nil {
			t.Fatalf("FirstPublishedForUnit(n=%d): %v", n, err)
		}
		if len(got) != 0 {
			t.Errorf("n=%d returned %d leaders, want none", n, len(got))
		}
	}
}
