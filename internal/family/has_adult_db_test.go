package family

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/db"
)

// HasAdult is the gate on /my-family for a family-wide login: that
// login belongs to the household, and the household's contact details
// are an adult's to manage.

func adultTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping family integration tests")
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

func familyWith(t *testing.T, pool *pgxpool.Pool, name string, memberTypes ...string) string {
	t.Helper()
	ctx := context.Background()
	var familyID string
	if err := pool.QueryRow(ctx, `INSERT INTO families (name) VALUES ($1) RETURNING id::text`, name).Scan(&familyID); err != nil {
		t.Fatalf("creating family: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM families WHERE id = $1`, familyID) })

	for i, mt := range memberTypes {
		if _, err := pool.Exec(ctx, `
			INSERT INTO members (family_id, first_name, last_name, member_type)
			VALUES ($1, $2, 'Tester', $3)
		`, familyID, string(rune('A'+i)), mt); err != nil {
			t.Fatalf("creating %s member: %v", mt, err)
		}
	}
	return familyID
}

func TestHasAdult(t *testing.T) {
	pool := adultTestPool(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		members []string
		want    bool
	}{
		{"a parent and a Scout", []string{"adult", "youth"}, true},
		{"two parents", []string{"adult", "adult"}, true},
		{"a Scout on their own", []string{"youth"}, false},
		{"nobody yet", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			familyID := familyWith(t, pool, "hasadult-"+c.name, c.members...)
			got, err := HasAdult(ctx, pool, familyID)
			if err != nil {
				t.Fatalf("HasAdult: %v", err)
			}
			if got != c.want {
				t.Errorf("HasAdult = %v, want %v", got, c.want)
			}
		})
	}
}

// One family's adults say nothing about another's.
func TestHasAdultIsScopedToTheFamily(t *testing.T) {
	pool := adultTestPool(t)
	ctx := context.Background()

	familyWith(t, pool, "hasadult-neighbours", "adult")
	scoutOnly := familyWith(t, pool, "hasadult-scoutonly", "youth")

	got, err := HasAdult(ctx, pool, scoutOnly)
	if err != nil {
		t.Fatalf("HasAdult: %v", err)
	}
	if got {
		t.Error("a family with no adult reports one, so the neighbours' rows are being counted")
	}
}
