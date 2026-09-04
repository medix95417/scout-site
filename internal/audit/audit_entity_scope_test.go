package audit

// This test exists because entityScopeSQL has silently drifted out of
// sync with the audit.Log call sites twice now — first for
// family/member/role_assignment/sub_group, then for the eight types
// listed below. Both times the symptom was identical and invisible in
// normal use: audit_log rows were written correctly, but no read path
// could ever surface them, so the activity log quietly under-reported
// (48 real rows, in the second case) with nothing failing anywhere.
//
// Adding an audit.Log call with a new EntityType is only half the
// change; the entity's table has to be reachable from entityScopeSQL
// too. This test fails the build if someone does the first half and
// forgets the second.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// entityTypeTable maps every EntityType passed to audit.Log anywhere in
// the codebase to the table whose id (or other column) that entry's
// EntityID refers to — i.e. the table entityScopeSQL must select from
// for entries of this type to be visible on /audit.
//
// A few types deliberately point at a table they don't "own", because
// their EntityID is another entity's id:
//   - member_contact_info logs a members.id
//   - family_address logs a families.id (reached via members.family_id)
//
// Both are already covered by the role_assignments-based unions, so they
// map to the table those unions select from.
var entityTypeTable = map[string]string{
	"advancement_record":    "advancement_records",
	"content_page":          "content_pages",
	"custom_role":           "custom_roles",
	"event":                 "events",
	"family":                "role_assignments", // families.id, via members.family_id
	"family_address":        "role_assignments", // families.id, via members.family_id
	"fundraiser":            "fundraisers",
	"fundraiser_allocation": "fundraiser_allocations",
	"fundraiser_order":      "fundraiser_orders",
	"leader":                "leaders",
	"ledger_account":        "ledger_accounts",
	"ledger_transaction":    "ledger_transactions",
	"member":                "role_assignments", // members.id, via role_assignments.member_id
	"member_contact_info":   "role_assignments", // members.id, via role_assignments.member_id
	"newsletter":            "newsletters",
	"resource":              "resources",
	"role_assignment":       "role_assignments",
	"saved_treasury_report": "saved_treasury_reports",
	"sub_group":             "sub_groups",
	"system_setting":        "system_settings",
	"unit_setting":          "unit_settings",
	"bank_reconciliation":   "bank_reconciliations",
	"prospect":              "prospects",
}

var entityTypeLiteral = regexp.MustCompile(`EntityType:\s*"([a-z_]+)"`)

// loggedEntityTypes scans the whole repository for EntityType literals
// passed to audit.Log, so the test reflects what the code actually does
// rather than what a hand-maintained list claims it does.
func loggedEntityTypes(t *testing.T) []string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}

	seen := map[string]bool{}
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range entityTypeLiteral.FindAllStringSubmatch(string(src), -1) {
			seen[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking repo: %v", err)
	}

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestEveryLoggedEntityTypeIsScoped is the real point of this file: an
// audit entry that can never be read is the same as one that was never
// written, so every EntityType the codebase logs must be reachable from
// entityScopeSQL.
func TestEveryLoggedEntityTypeIsScoped(t *testing.T) {
	for _, et := range loggedEntityTypes(t) {
		table, mapped := entityTypeTable[et]
		if !mapped {
			t.Errorf("EntityType %q is logged somewhere in the codebase but is missing from entityTypeTable in this test — "+
				"add it, and make sure entityScopeSQL selects from its table, or entries of this type will be written to "+
				"audit_log and then never shown on /audit", et)
			continue
		}
		if !strings.Contains(entityScopeSQL, "FROM "+table+" ") && !strings.Contains(entityScopeSQL, "FROM "+table+"\n") {
			t.Errorf("EntityType %q maps to table %q, but entityScopeSQL never selects from it — "+
				"audit entries of this type are written but can never appear on /audit", et, table)
		}
	}
}

// TestEntityTypeTableHasNoStaleEntries catches the opposite drift: a
// mapping left behind after its audit.Log call site was removed, which
// would otherwise quietly weaken the check above by making a missing
// EntityType look accounted for.
func TestEntityTypeTableHasNoStaleEntries(t *testing.T) {
	logged := map[string]bool{}
	for _, et := range loggedEntityTypes(t) {
		logged[et] = true
	}
	for et := range entityTypeTable {
		if !logged[et] {
			t.Errorf("entityTypeTable lists %q, but nothing in the codebase logs that EntityType any more — remove the stale mapping", et)
		}
	}
}
