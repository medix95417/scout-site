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

// email_group is the one capability whose meaning is "this, but only for the
// group you hold the role in". What each built-in role is given decides who
// can email at all, so it is pinned here rather than left to be discovered.
func TestWhoIsGivenEmailGroupByDefault(t *testing.T) {
	want := map[string]bool{
		"cubmaster": true, "den_leader": true, "scoutmaster": true,
		"assistant_scoutmaster": true, "super_admin": true,
		// A Patrol Leader emails their own patrol — and gets nothing else that
		// touches the site's content.
		"patrol_leader": true,
		// The SPL is assigned to the unit, not a patrol, so there is no own
		// patrol to confine it to. A unit can grant it from the roles screen.
		"senior_patrol_leader": false,
		"treasurer":            false, "parent": false, "scout": false,
	}
	for role, grants := range want {
		got := CapabilitiesOf(DefaultCapabilitiesForRole(role)...).has(CapEmailGroup)
		if got != grants {
			t.Errorf("%s: email_group = %v, want %v", role, got, grants)
		}
	}
}

func TestAPatrolLeaderCannotEditTheSite(t *testing.T) {
	pl := CapabilitiesOf(DefaultCapabilitiesForRole("patrol_leader")...)
	if CanEditUnitContent(pl) {
		t.Error("giving a Patrol Leader email_group must not give them edit_content, which is the whole site")
	}
	if !CanEmailGroup(pl) {
		t.Error("a Patrol Leader should be able to email (their own patrol)")
	}
}

// Appointing a Patrol Leader now means granting email_group, so whoever
// appoints one has to hold it too (the ceiling on the roster pages). The
// leaders who appoint them by default do.
func TestTheLeadersWhoAppointPatrolLeadersStillCan(t *testing.T) {
	needed := CapabilitiesOf(DefaultCapabilitiesForRole("patrol_leader")...)
	for _, role := range []string{"scoutmaster", "assistant_scoutmaster", "super_admin"} {
		if !CapabilitiesOf(DefaultCapabilitiesForRole(role)...).Covers(needed) {
			t.Errorf("a %s can no longer appoint a Patrol Leader", role)
		}
	}
}

func TestEmailGroupIsOnTheRolesScreen(t *testing.T) {
	found := false
	for _, c := range AllCapabilities {
		if c == CapEmailGroup {
			found = true
		}
	}
	if !found {
		t.Error("email_group is not in AllCapabilities, so a custom role could not be given it")
	}
	if CapabilityLabel(CapEmailGroup) == CapEmailGroup {
		t.Error("email_group has no sentence on the roles screen, only its internal name")
	}
}
