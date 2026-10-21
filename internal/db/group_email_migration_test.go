package db

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// Migration 0053 gives built-in roles a new capability, and a unit that has
// already customised a role has stored the whole list — which replaces the
// code's defaults — so the new capability would be missing from exactly the
// roles whose leaders had touched their settings. The migration's UPDATE
// fixes that, and this runs that statement, taken from the migration file
// itself rather than retyped, against rows in the state such a unit is in.

func TestGroupEmailMigrationCarriesTheCapabilityOntoCustomisedRoles(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping migration test")
	}
	ctx := context.Background()
	pool, err := Connect(ctx, url)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	raw, err := migrationsFS.ReadFile("migrations/0053_group_email.sql")
	if err != nil {
		t.Fatalf("reading the migration: %v", err)
	}
	text := string(raw)
	start := strings.Index(text, "UPDATE role_capability_overrides")
	if start < 0 {
		t.Fatal("the migration no longer contains the override backfill")
	}
	statement := text[start : start+strings.Index(text[start:], ";")+1]

	var unitID string
	slug := fmt.Sprintf("mig53-%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx,
		`INSERT INTO units (slug, name, unit_type, hostname) VALUES ($1, $1, 'troop', $1 || '.test.invalid') RETURNING id::text`,
		slug).Scan(&unitID); err != nil {
		t.Fatalf("creating a unit: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM units WHERE id = $1`, unitID) })

	// What a unit that customised these roles before the migration has stored,
	// and what each should come out as.
	cases := []struct {
		role   string
		before []string
		after  []string
	}{
		// An adult leader who still edits content gets the capability…
		{"den_leader", []string{"edit_content", "manage_ledger"}, []string{"edit_content", "manage_ledger", "email_group"}},
		{"scoutmaster", []string{"edit_content", "approve_submissions", "approve_expenses"}, []string{"edit_content", "approve_submissions", "approve_expenses", "email_group"}},
		// …and so does a Patrol Leader who still submits for approval.
		{"patrol_leader", []string{"submit_for_approval"}, []string{"submit_for_approval", "email_group"}},
		// A role the unit stripped of what the default hangs on is left as the
		// unit left it.
		{"cubmaster", []string{"approve_expenses"}, []string{"approve_expenses"}},
		{"patrol_leader", nil, nil},
		// Already has it: not added twice.
		{"assistant_scoutmaster", []string{"edit_content", "email_group"}, []string{"edit_content", "email_group"}},
		// Not a role the default grants it to at all.
		{"treasurer", []string{"manage_ledger"}, []string{"manage_ledger"}},
		{"senior_patrol_leader", []string{"submit_for_approval"}, []string{"submit_for_approval"}},
	}

	// patrol_leader appears twice; one row per (unit, role), so each case
	// gets its own unit and nothing depends on the order.
	unitFor := map[int]string{}
	for i, c := range cases {
		u := unitID
		if i > 0 {
			if err := pool.QueryRow(ctx,
				`INSERT INTO units (slug, name, unit_type, hostname) VALUES ($1, $1, 'troop', $1 || '.test.invalid') RETURNING id::text`,
				fmt.Sprintf("%s-%d", slug, i)).Scan(&u); err != nil {
				t.Fatalf("creating a unit: %v", err)
			}
			id := u
			t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM units WHERE id = $1`, id) })
		}
		unitFor[i] = u
		before := c.before
		if before == nil {
			before = []string{}
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO role_capability_overrides (unit_id, role_slug, capabilities) VALUES ($1, $2, $3)`,
			u, c.role, before); err != nil {
			t.Fatalf("seeding %s: %v", c.role, err)
		}
	}

	// The migration's statement is not scoped to one unit — run unscoped, as
	// it did — so it also touches whatever else is in the table. Only these
	// rows are read back.
	if _, err := pool.Exec(ctx, statement); err != nil {
		t.Fatalf("running the migration's backfill: %v", err)
	}

	for i, c := range cases {
		var got []string
		if err := pool.QueryRow(ctx,
			`SELECT capabilities FROM role_capability_overrides WHERE unit_id = $1 AND role_slug = $2`,
			unitFor[i], c.role).Scan(&got); err != nil {
			t.Fatalf("reading %s back: %v", c.role, err)
		}
		want := append([]string{}, c.after...)
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s with %v came out as %v, want %v", c.role, c.before, got, want)
		}
	}
}
