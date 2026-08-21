package units

import "testing"

// capsFor resolves role slugs into capabilities using only the fixed
// system-role map — every test case here uses fixed slugs, so this avoids
// needing a database connection (CapabilitiesForRoles' only DB-touching
// path is looking up custom_roles for a slug that isn't a known system
// role).
func capsFor(roles []string) Capabilities {
	caps := make(Capabilities)
	for _, role := range roles {
		for _, c := range systemRoleCapabilities[role] {
			caps[c] = true
		}
	}
	return caps
}

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
		if got := CanEditUnitContent(capsFor(c.roles)); got != c.want {
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
		if got := CanSubmitForApproval(capsFor(c.roles)); got != c.want {
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
		if got := CanApprove(capsFor(c.roles)); got != c.want {
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
		{[]string{"scoutmaster"}, false}, // deliberately narrower than content-edit access
		{[]string{"treasurer"}, true},
		{[]string{"super_admin"}, true},
	}
	for _, c := range cases {
		if got := CanManageLedger(capsFor(c.roles)); got != c.want {
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
		if got := IsSuperAdmin(capsFor(c.roles)); got != c.want {
			t.Errorf("IsSuperAdmin(%v) = %v, want %v", c.roles, got, c.want)
		}
	}
}

func TestAccentTextColor(t *testing.T) {
	cases := []struct {
		name string
		hex  string
		want string
	}{
		{"Scouting Red — dark enough for white text", "#CE1126", "#ffffff"},
		{"Cub Scout Yellow — too light for white text", "#FDC116", "#111827"},
		{"BSA Blue — dark enough for white text", "#003F87", "#ffffff"},
		{"pure white — needs dark text", "#FFFFFF", "#111827"},
		{"pure black — needs white text", "#000000", "#ffffff"},
		{"malformed color falls back to white text", "not-a-color", "#ffffff"},
	}
	for _, c := range cases {
		u := Unit{AccentColor: c.hex}
		if got := u.AccentTextColor(); got != c.want {
			t.Errorf("%s: AccentTextColor(%q) = %q, want %q", c.name, c.hex, got, c.want)
		}
	}
}
