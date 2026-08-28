package units

import "testing"

// TestCapApproveExpenses_SeparatedFromSpending is the whole point of the
// control: nobody should hold both "can spend the unit's money" and "can
// authorize spending it" through a single system role. If a future edit
// grants both to one role, the second signature stops meaning anything.
func TestCapApproveExpenses_SeparatedFromSpending(t *testing.T) {
	for role, granted := range systemRoleCapabilities {
		if role == "super_admin" {
			// Deliberately holds everything — it's the escape hatch for a
			// unit whose leader is unavailable, and is expected to be one
			// or two trusted people, not a routine login.
			continue
		}
		var spend, approve bool
		for _, c := range granted {
			switch c {
			case CapManageLedger:
				spend = true
			case CapApproveExpenses:
				approve = true
			}
		}
		if spend && approve {
			t.Errorf("role %q can both spend and authorize spending — that defeats the separation", role)
		}
	}
}

// TestCapApproveExpenses_HeldByTheUnitsTopLeader pins down who signs off:
// the Cubmaster in a Pack, the Scoutmaster in a Troop.
func TestCapApproveExpenses_HeldByTheUnitsTopLeader(t *testing.T) {
	for _, role := range []string{"cubmaster", "scoutmaster", "super_admin"} {
		caps := Capabilities{}
		for _, c := range systemRoleCapabilities[role] {
			caps[c] = true
		}
		if !CanApproveExpenses(caps) {
			t.Errorf("%q should be able to authorize expenses", role)
		}
	}
	// Everyone else, including the Treasurer who enters the expense.
	for _, role := range []string{"treasurer", "assistant_scoutmaster", "den_leader", "parent", "scout", "senior_patrol_leader", "patrol_leader"} {
		caps := Capabilities{}
		for _, c := range systemRoleCapabilities[role] {
			caps[c] = true
		}
		if CanApproveExpenses(caps) {
			t.Errorf("%q should NOT be able to authorize expenses", role)
		}
	}
}

// TestSystemRolesWithCapability backs roster.MembersWithCapability's
// "who can do this" lookup.
func TestSystemRolesWithCapability(t *testing.T) {
	got := SystemRolesWithCapability(CapApproveExpenses)
	want := []string{"cubmaster", "scoutmaster", "super_admin"}
	if len(got) != len(want) {
		t.Fatalf("SystemRolesWithCapability = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SystemRolesWithCapability = %v, want %v (sorted)", got, want)
		}
	}
	if len(SystemRolesWithCapability("no_such_capability")) != 0 {
		t.Error("an unknown capability should match no roles")
	}
}
