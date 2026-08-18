package units

import "testing"

func TestCanEditUnitContent(t *testing.T) {
	cases := []struct {
		roles []string
		want  bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"parent"}, false},
		{[]string{"senior_patrol_leader"}, false},
		{[]string{"scoutmaster"}, true},
		{[]string{"assistant_scoutmaster"}, true},
		{[]string{"cubmaster"}, true},
		{[]string{"den_leader"}, true},
		{[]string{"super_admin"}, true},
		{[]string{"treasurer"}, false}, // treasury access != content-edit access
		{[]string{"parent", "scoutmaster"}, true},
	}
	for _, c := range cases {
		if got := CanEditUnitContent(c.roles); got != c.want {
			t.Errorf("CanEditUnitContent(%v) = %v, want %v", c.roles, got, c.want)
		}
	}
}

func TestCanSubmitForApproval(t *testing.T) {
	cases := []struct {
		roles []string
		want  bool
	}{
		{nil, false},
		{[]string{"parent"}, false},
		{[]string{"senior_patrol_leader"}, true},
		{[]string{"patrol_leader"}, true},
		{[]string{"scoutmaster"}, false}, // adult leaders don't need the approval path
	}
	for _, c := range cases {
		if got := CanSubmitForApproval(c.roles); got != c.want {
			t.Errorf("CanSubmitForApproval(%v) = %v, want %v", c.roles, got, c.want)
		}
	}
}

func TestCanApprove(t *testing.T) {
	cases := []struct {
		roles []string
		want  bool
	}{
		{nil, false},
		{[]string{"senior_patrol_leader"}, false},
		{[]string{"scoutmaster"}, true},
		{[]string{"assistant_scoutmaster"}, true},
		{[]string{"super_admin"}, true},
		{[]string{"cubmaster"}, false}, // per requirements: only SM/ASM/super_admin approve
	}
	for _, c := range cases {
		if got := CanApprove(c.roles); got != c.want {
			t.Errorf("CanApprove(%v) = %v, want %v", c.roles, got, c.want)
		}
	}
}

func TestCanManageLedger(t *testing.T) {
	cases := []struct {
		roles []string
		want  bool
	}{
		{nil, false},
		{[]string{"scoutmaster"}, false}, // deliberately narrower than AdultLeaderRoles
		{[]string{"treasurer"}, true},
		{[]string{"super_admin"}, true},
	}
	for _, c := range cases {
		if got := CanManageLedger(c.roles); got != c.want {
			t.Errorf("CanManageLedger(%v) = %v, want %v", c.roles, got, c.want)
		}
	}
}

func TestIsSuperAdmin(t *testing.T) {
	cases := []struct {
		roles []string
		want  bool
	}{
		{nil, false},
		{[]string{"treasurer"}, false},
		{[]string{"scoutmaster"}, false},
		{[]string{"super_admin"}, true},
		{[]string{"parent", "super_admin"}, true},
	}
	for _, c := range cases {
		if got := IsSuperAdmin(c.roles); got != c.want {
			t.Errorf("IsSuperAdmin(%v) = %v, want %v", c.roles, got, c.want)
		}
	}
}
