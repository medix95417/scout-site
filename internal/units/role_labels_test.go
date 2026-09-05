package units

import "testing"

// The zero-value labeler is the one a page falls back to when loading a
// unit's custom roles failed. It must still be useful, because the
// alternative is a roster full of internal slugs.
func TestZeroLabelerStillLabelsBuiltInRoles(t *testing.T) {
	var l RoleLabeler
	for slug, want := range map[string]string{
		"den_leader":            "Den Leader",
		"assistant_scoutmaster": "Assistant Scoutmaster",
		"super_admin":           "Site Administrator",
		"scout":                 "Scout",
	} {
		if got := l.Label(slug); got != want {
			t.Errorf("Label(%q) = %q, want %q", slug, got, want)
		}
	}
}

// Every built-in slug must have a label, or a roster shows a raw key for
// whichever one was missed.
func TestEveryBuiltInRoleLabels(t *testing.T) {
	var l RoleLabeler
	for _, r := range systemRoleOrder {
		got := l.Label(r.slug)
		if got == r.slug {
			t.Errorf("built-in role %q renders as its own slug", r.slug)
		}
		if got == "" {
			t.Errorf("built-in role %q renders as empty", r.slug)
		}
	}
}

// A unit's own name for a role wins over anything else.
func TestCustomLabelWins(t *testing.T) {
	l := RoleLabeler{custom: map[string]string{
		"committee_chair": "Committee Chair",
		// A unit may deliberately rename what a slug means to them. The
		// stored label is theirs and is not second-guessed.
		"advancement_coordinator": "Advancement Chair",
	}}
	if got := l.Label("committee_chair"); got != "Committee Chair" {
		t.Errorf("Label = %q", got)
	}
	if got := l.Label("advancement_coordinator"); got != "Advancement Chair" {
		t.Errorf("Label = %q", got)
	}
}

// Deleting a custom role deliberately leaves its assignments in place, so
// a slug with no definition anywhere is reachable in normal use — not a
// corrupt-data case. It must read like a role name, not like a database
// column.
func TestUndefinedSlugIsPrettified(t *testing.T) {
	var l RoleLabeler
	for slug, want := range map[string]string{
		"committee_chair": "Committee Chair",
		"quartermaster":   "Quartermaster",
		"chaplain-aide":   "Chaplain Aide",
		"":                "",
	} {
		if got := l.Label(slug); got != want {
			t.Errorf("Label(%q) = %q, want %q", slug, got, want)
		}
	}
}

// Order is preserved, because the roster shows roles in the order the
// query returned them and a reshuffle between page loads reads as a bug.
func TestLabelsPreservesOrder(t *testing.T) {
	l := RoleLabeler{custom: map[string]string{"committee_chair": "Committee Chair"}}
	got := l.Labels([]string{"den_leader", "committee_chair", "parent"})
	want := []string{"Den Leader", "Committee Chair", "Parent"}
	if len(got) != len(want) {
		t.Fatalf("Labels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Labels = %v, want %v", got, want)
		}
	}
	if l.Labels(nil) != nil {
		t.Error("Labels(nil) should be nil, so a member with no roles renders nothing")
	}
}
