package units

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Deactivating a member must take their permissions with them.
//
// members.active is a soft delete (migration 0022): it hides someone from
// the roster without erasing the history that references them. Before this
// guard, the role lookups ignored it entirely, so a Scoutmaster who left
// the unit vanished from the roster and kept full admin access —
// /admin/roster, content editing, the lot. Deactivation looked like
// removal and wasn't.
//
// Both lookups feed capabilitiesFor, so a missed filter here silently
// grants every capability the stale roles imply.
func TestRoleLookupsExcludeInactiveMembers(t *testing.T) {
	src := readSource(t)

	for _, fn := range []string{"RolesForMemberInUnit", "RolesForFamilyInUnit"} {
		body := functionBody(t, src, fn)
		if !strings.Contains(body, "members.active") {
			t.Errorf("%s does not filter on members.active — a deactivated member keeps "+
				"every role, and therefore every capability, they had before being removed", fn)
		}
		// The member variant has to join members to reach the column at
		// all; without the join the filter cannot be there.
		if !regexp.MustCompile(`(?s)JOIN\s+members`).MatchString(body) {
			t.Errorf("%s does not join members, so it cannot be honouring the active flag", fn)
		}
	}
}

func readSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("units.go")
	if err != nil {
		t.Fatalf("reading units.go: %v", err)
	}
	return string(b)
}

// functionBody returns the source text of one top-level function, so the
// assertions above are scoped to the query that matters rather than to
// the whole file (where an unrelated mention of members.active would make
// this pass for the wrong reason).
func functionBody(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "func "+name+"(")
	if start < 0 {
		t.Fatalf("function %s not found in units.go — update this guard rather than deleting it", name)
	}
	rest := src[start:]
	if end := strings.Index(rest, "\nfunc "); end > 0 {
		return rest[:end]
	}
	return rest
}
