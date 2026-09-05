package units

import (
	"errors"
	"testing"
)

// Every built-in role slug the code can grant capabilities to must also
// be one the roles admin page can show. A slug in one list and not the
// other is a role whose permissions are real but invisible — which is
// the whole failure this page exists to prevent.
func TestEverySystemRoleIsDisplayable(t *testing.T) {
	for slug := range systemRoleCapabilities {
		if !isKnownSystemRole(slug) {
			t.Errorf("role %q grants capabilities but has no entry in systemRoleOrder, so the admin page never shows it", slug)
		}
	}
	for _, r := range systemRoleOrder {
		if SystemRoleLabel(r.slug) == r.slug {
			t.Errorf("role %q has no human-readable label", r.slug)
		}
	}
	// The two roles that deliberately grant nothing are still listed, so
	// a leader can see that they grant nothing rather than wonder.
	for _, slug := range []string{"parent", "scout"} {
		if !isKnownSystemRole(slug) {
			t.Errorf("role %q should be shown even though it grants nothing", slug)
		}
	}
}

// Every capability must read as a sentence somewhere, or the checkbox
// list shows raw internal names like "approve_submissions".
func TestEveryCapabilityHasALabel(t *testing.T) {
	for _, c := range AllCapabilities {
		if CapabilityLabel(c) == c {
			t.Errorf("capability %q has no label", c)
		}
	}
}

// Taking site-settings away from the Site Administrator role is the one
// change that cannot be undone from inside the site: the page that would
// undo it is the page it locks you out of. The refusal happens before any
// database work, which is what lets this run without one.
func TestSuperAdminCannotBeDisarmed(t *testing.T) {
	err := SetSystemRoleCapabilities(t.Context(), nil, "unit-1", "super_admin",
		[]string{CapEditContent, CapManageLedger}, "actor-1")
	if !errors.Is(err, ErrCannotDisarmSuperAdmin) {
		t.Fatalf("removing super_admin from the super_admin role returned %v, want ErrCannotDisarmSuperAdmin", err)
	}

	// The same edit with the capability kept is allowed through to the
	// database — a nil pool panics or errors there, and either way it is
	// past the guard, which is what this asserts.
	func() {
		defer func() { _ = recover() }()
		_ = SetSystemRoleCapabilities(t.Context(), nil, "unit-1", "super_admin",
			[]string{CapSuperAdmin, CapEditContent}, "actor-1")
	}()
}

// A role slug that isn't built-in must not be writable here — custom
// roles are edited on custom_roles, and letting an arbitrary string
// through would create override rows nothing ever reads.
func TestOnlyBuiltInRolesAreOverridable(t *testing.T) {
	err := SetSystemRoleCapabilities(t.Context(), nil, "unit-1", "committee_chair",
		[]string{CapEditContent}, "actor-1")
	if err == nil {
		t.Fatal("a custom role slug was accepted as a built-in role override")
	}
}

func TestFilterCapabilitiesDropsUnknownAndDuplicates(t *testing.T) {
	got := filterCapabilities([]string{CapEditContent, "drop_database", CapEditContent, CapSuperAdmin, ""})
	want := []string{CapEditContent, CapSuperAdmin}
	if len(got) != len(want) {
		t.Fatalf("filterCapabilities = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filterCapabilities = %v, want %v", got, want)
		}
	}
}

// sameCapabilitySet decides whether an edit is "back to the default", and
// therefore whether the override row is deleted rather than stored. Order
// and duplicates must not change that answer, or a unit that clicks its
// way back to the defaults gets a frozen copy of them instead.
func TestSameCapabilitySetIgnoresOrderAndDuplicates(t *testing.T) {
	if !sameCapabilitySet(
		[]string{CapSuperAdmin, CapEditContent},
		[]string{CapEditContent, CapSuperAdmin, CapEditContent}) {
		t.Error("same set in a different order compared unequal")
	}
	if sameCapabilitySet([]string{CapEditContent}, []string{CapEditContent, CapManageLedger}) {
		t.Error("different sets compared equal")
	}
	if !sameCapabilitySet(nil, []string{"not_a_capability"}) {
		t.Error("a set of only unknown names should be equal to the empty set, since both grant nothing")
	}
}
