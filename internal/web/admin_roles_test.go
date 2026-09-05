package web

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/roster"
	"github.com/47-yonkers/scout-site/internal/units"
)

// The roles page renders two different shapes of role through one shared
// "roleCard" sub-template fed by a dict, which is exactly the arrangement
// that parses cleanly and then fails at execution time on a field name
// that doesn't exist. TestEveryTemplateParses can't see that; this can.
func TestCustomRolesTemplateExecutes(t *testing.T) {
	tmpl, err := parsePageTemplate("admin-custom-roles.html")
	if err != nil {
		t.Fatalf("parsing admin-custom-roles.html: %v", err)
	}

	// Built by hand rather than from the database, so this test needs no
	// Postgres and still exercises every branch the handler can produce.
	relevant := []systemRoleView{{
		SystemRole: units.SystemRole{
			Slug: "super_admin", Label: "Site Administrator",
			Default:      []string{units.CapSuperAdmin},
			Capabilities: []string{units.CapSuperAdmin},
		},
		CapabilityViews: capabilityViews([]string{units.CapSuperAdmin}),
		Relevant:        true,
	}, {
		// An overridden role, so the "Changed for this unit" branch and
		// the reset button both render.
		SystemRole: units.SystemRole{
			Slug: "den_leader", Label: "Den Leader", UnitTypes: []string{"pack"},
			Default:      []string{units.CapEditContent},
			Capabilities: []string{units.CapEditContent, units.CapApproveExpenses},
			Overridden:   true,
		},
		CapabilityViews: capabilityViews([]string{units.CapEditContent, units.CapApproveExpenses}),
		Relevant:        true,
	}, {
		// A role granting nothing, so the empty branch renders too.
		SystemRole:      units.SystemRole{Slug: "parent", Label: "Parent"},
		CapabilityViews: capabilityViews(nil),
		Relevant:        true,
	}}

	other := []systemRoleView{{
		SystemRole:      units.SystemRole{Slug: "patrol_leader", Label: "Patrol Leader", UnitTypes: []string{"troop"}},
		CapabilityViews: capabilityViews([]string{units.CapSubmitForApproval}),
	}}

	customs := []customRoleView{{
		CustomRole: roster.CustomRole{
			ID: "role-1", Slug: "committee_chair", Label: "Committee Chair",
			Capabilities: []string{units.CapManageLedger},
		},
		CapabilityViews: capabilityViews([]string{units.CapManageLedger}),
	}, {
		CustomRole:      roster.CustomRole{ID: "role-2", Slug: "historian", Label: "Historian"},
		CapabilityViews: capabilityViews(nil),
	}}

	data := struct {
		baseData
		Roles            []customRoleView
		Capabilities     []capabilityOption
		SystemRoles      []systemRoleView
		OtherSystemRoles []systemRoleView
		EditID           string
		EditSystemRole   string
		SuperAdminRole   string
	}{
		baseData:         baseData{PageTitle: "Roles & Permissions", Unit: units.Unit{Name: "Pack 47", UnitType: "pack"}, CSRFToken: "tok"},
		Roles:            customs,
		Capabilities:     capabilityOptions,
		SystemRoles:      relevant,
		OtherSystemRoles: other,
		EditID:           "role-1",
		EditSystemRole:   "den_leader",
		SuperAdminRole:   "super_admin",
	}

	var sb strings.Builder
	if err := tmpl.ExecuteTemplate(&sb, "base", data); err != nil {
		t.Fatalf("executing admin-custom-roles.html: %v", err)
	}
	out := sb.String()

	// Every form on this page mutates permissions, so every one of them
	// needs the CSRF token. A sub-template fed by a dict silently renders
	// an absent key as empty, so a dropped "CSRFToken" key produces a
	// form that parses, executes, looks right, and is rejected the moment
	// anybody submits it — checked by counting rather than eyeballing.
	forms := strings.Count(out, "<form ")
	tokens := strings.Count(out, `name="csrf_token" value="tok"`)
	if forms == 0 || forms != tokens {
		t.Errorf("page has %d forms but %d carry a CSRF token", forms, tokens)
	}

	for _, want := range []string{
		"Site Administrator",
		"Committee Chair",
		"Changed for this unit",
		"Put back to the site default",
		`action="/admin/system-roles/den_leader"`,
		`action="/admin/custom-roles/role-1"`,
		"Patrol Leader", // the other-program section still renders its rows
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page does not contain %q", want)
		}
	}
}

// The capability checkbox list used to be written out by hand and fell a
// capability behind (approve_expenses, added in migration 0039, never got
// a checkbox — so the one documented way to let an Assistant Scoutmaster
// authorize spending could not be ticked). It is now derived, and this
// fails if anything ever pins it back to a hand-written subset.
func TestCapabilityOptionsCoverEveryCapability(t *testing.T) {
	if len(capabilityOptions) != len(units.AllCapabilities) {
		t.Fatalf("capabilityOptions has %d entries, units.AllCapabilities has %d",
			len(capabilityOptions), len(units.AllCapabilities))
	}
	offered := map[string]bool{}
	for _, o := range capabilityOptions {
		offered[o.Value] = true
		if o.Label == "" || o.Label == o.Value {
			t.Errorf("capability %q has no human-readable label", o.Value)
		}
	}
	for _, c := range units.AllCapabilities {
		if !offered[c] {
			t.Errorf("capability %q has no checkbox on the role form", c)
		}
	}
}
