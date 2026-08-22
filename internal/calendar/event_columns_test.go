package calendar

// eventColumns scans straight into an Event's plain string fields, so a
// nullable text column selected bare will fail the scan at runtime with
// "cannot scan NULL into *string" — and because queryEvents scans rows in
// a loop, that single bad row aborts the whole query: /calendar returns a
// 500 and the homepage's upcoming-events list renders empty, for every
// visitor, not just on the row that happens to be NULL.
//
// That is exactly what happened with description and location, which were
// nullable from 0001_init.sql onward but were selected bare. This test
// reads the migrations rather than hard-coding a column list, so a
// nullable column added to events in some future migration fails here
// instead of in production.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// scannedAsString is the set of Event fields eventColumns scans into a
// plain (non-pointer) string — i.e. the ones that cannot tolerate a NULL.
// Columns scanned into a pointer (ends_at, sub_group_id) are deliberately
// absent: NULL is a valid, expected value for those.
var scannedAsString = map[string]bool{
	"title":       true,
	"description": true,
	"location":    true,
	"visibility":  true,
	"status":      true,
}

// nullableEventColumns parses the migrations for the events table's
// columns and returns those that permit NULL.
func nullableEventColumns(t *testing.T) []string {
	t.Helper()

	dir, err := filepath.Abs(filepath.Join("..", "db", "migrations"))
	if err != nil {
		t.Fatalf("resolving migrations dir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading migrations dir: %v", err)
	}

	createEvents := regexp.MustCompile(`(?s)CREATE TABLE events\s*\((.*?)\n\);`)
	addColumn := regexp.MustCompile(`ALTER TABLE events ADD COLUMN (\w+)\s+(\w+)([^;]*);`)
	colDef := regexp.MustCompile(`^(\w+)\s+(text|uuid|integer|boolean|timestamptz|visibility|content_status)\b`)

	nullable := map[string]bool{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		text := string(src)

		if m := createEvents.FindStringSubmatch(text); m != nil {
			for _, line := range strings.Split(m[1], "\n") {
				line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
				cm := colDef.FindStringSubmatch(line)
				if cm == nil || strings.Contains(line, "PRIMARY KEY") {
					continue
				}
				nullable[cm[1]] = !strings.Contains(line, "NOT NULL")
			}
		}
		for _, m := range addColumn.FindAllStringSubmatch(text, -1) {
			nullable[m[1]] = !strings.Contains(m[3], "NOT NULL")
		}
	}

	var out []string
	for col, isNullable := range nullable {
		if isNullable {
			out = append(out, col)
		}
	}
	sort.Strings(out)
	return out
}

func TestEventColumnsCoalescesNullableStringColumns(t *testing.T) {
	nullable := nullableEventColumns(t)
	if len(nullable) == 0 {
		t.Fatal("parsed zero nullable columns for the events table — the migration parser above has probably broken, " +
			"which would make this test silently vacuous")
	}

	for _, col := range nullable {
		if !scannedAsString[col] {
			continue // scanned into a pointer; NULL is fine
		}
		if !strings.Contains(eventColumns, "COALESCE("+col+", '')") {
			t.Errorf("events.%s is nullable and is scanned into a plain string, but eventColumns selects it without "+
				"COALESCE(%s, '') — a single NULL row will abort the whole scan, 500ing /calendar and blanking the "+
				"homepage's upcoming-events list for every visitor", col, col)
		}
	}
}

// TestScannedAsStringMatchesEventStruct guards the list above from drifting
// as columns are added: every column eventColumns selects bare (not through
// a cast or COALESCE) and scans into a string should be listed.
func TestEventColumnsHasNoUnreviewedBareColumns(t *testing.T) {
	for _, col := range nullableEventColumns(t) {
		bare := regexp.MustCompile(`(^|[ ,` + "`" + `])` + col + `([ ,` + "`" + `]|$)`)
		if bare.MatchString(eventColumns) && scannedAsString[col] {
			t.Errorf("events.%s appears bare in eventColumns but is marked as scanned-into-string and is nullable; "+
				"it needs COALESCE", col)
		}
	}
}
