package units

import "testing"

// Covers is the whole of the privilege ceiling on the roster pages, so
// the cases here are the cases that matter: each row is a leader trying
// to hand out, or act on an account holding, some set of capabilities.
func TestCapabilities_Covers(t *testing.T) {
	asm := CapabilitiesOf(CapEditContent, CapApproveSubmissions)
	cubmaster := CapabilitiesOf(CapEditContent, CapApproveExpenses)
	scoutmaster := CapabilitiesOf(CapEditContent, CapApproveSubmissions, CapApproveExpenses)
	treasurer := CapabilitiesOf(CapManageLedger)
	admin := CapabilitiesOf(CapSuperAdmin)
	denLeader := CapabilitiesOf(CapEditContent)
	patrolLeader := CapabilitiesOf(CapSubmitForApproval)

	cases := []struct {
		name   string
		actor  Capabilities
		needed Capabilities
		want   bool
	}{
		// The two escalations this exists to stop.
		{"ASM cannot hand out Treasurer", asm, treasurer, false},
		{"ASM cannot act on an Admin", asm, admin, false},
		{"ASM cannot act on a custom role granting only super_admin", asm, CapabilitiesOf(CapSuperAdmin), false},

		// The top leader role stays with the top leader.
		{"ASM cannot appoint a Scoutmaster", asm, scoutmaster, false},
		{"Scoutmaster can appoint an ASM", scoutmaster, asm, true},
		{"Cubmaster can appoint a Den Leader", cubmaster, denLeader, true},
		{"Den Leader cannot appoint a Cubmaster", denLeader, cubmaster, false},

		// Editing without approval covers submitting for approval.
		{"ASM can appoint a Patrol Leader", asm, patrolLeader, true},
		{"Den Leader covers submit_for_approval", denLeader, patrolLeader, true},
		{"Patrol Leader does not cover editing", patrolLeader, denLeader, false},

		// super_admin sits above everything, including capability sets
		// that a super_admin custom role would not literally list.
		{"Admin covers Treasurer", admin, treasurer, true},
		{"Admin covers Scoutmaster", admin, scoutmaster, true},
		{"Admin covers an empty set", admin, Capabilities{}, true},

		// Nothing needed is always covered; equal sets are covered.
		{"anyone covers a plain parent", patrolLeader, Capabilities{}, true},
		{"a set covers itself", asm, asm, true},
		{"a false entry in needed is not a requirement", asm, Capabilities{CapManageLedger: false}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.actor.Covers(c.needed); got != c.want {
				t.Errorf("%v.Covers(%v) = %v, want %v", c.actor, c.needed, got, c.want)
			}
		})
	}
}
