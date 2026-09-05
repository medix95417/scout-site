package web

// Showing role names to people rather than to the database.
//
// role_assignments.role is a slug because it has to be a stable key —
// permission checks compare it, custom_roles matches on it, the CSV
// import writes it. That is right, and it is also why every page listing
// somebody's roles was showing "den_leader" and "assistant_scoutmaster"
// to parents.
//
// units.RoleLabeler does the translation; this file is the one place the
// web layer builds one and applies it, so no handler has to remember that
// built-in roles are labelled in code and custom ones in the database.

import (
	"context"
	"log"

	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/roster"
	"github.com/47-yonkers/scout-site/internal/units"
)

// roleLabeler builds a labeler for a unit, never failing the caller.
//
// A labeler that couldn't load its custom roles still labels every
// built-in role correctly and prettifies the rest, which is a far better
// outcome than a 500 on the roster because one lookup failed. The error
// is logged so it doesn't pass unnoticed.
func (h *Handlers) roleLabeler(ctx context.Context, unitID string) units.RoleLabeler {
	l, err := units.NewRoleLabeler(ctx, h.Pool, unitID)
	if err != nil {
		log.Printf("web: loading custom role labels for unit %s: %v", unitID, err)
	}
	return l
}

// labelRoster fills in RoleLabels on every entry, with one labeler shared
// across the whole list rather than a query per member.
func (h *Handlers) labelRoster(ctx context.Context, unitID string, entries []family.RosterEntry) []family.RosterEntry {
	if len(entries) == 0 {
		return entries
	}
	l := h.roleLabeler(ctx, unitID)
	for i := range entries {
		entries[i].RoleLabels = l.Labels(entries[i].Roles)
	}
	return entries
}

// roleAssignmentView is one of a member's role assignments on the roster
// edit page, with its slug already resolved for display.
type roleAssignmentView struct {
	roster.RoleAssignment
	Label string
}

func labelAssignments(l units.RoleLabeler, assignments []roster.RoleAssignment) []roleAssignmentView {
	out := make([]roleAssignmentView, 0, len(assignments))
	for _, ra := range assignments {
		out = append(out, roleAssignmentView{RoleAssignment: ra, Label: l.Label(ra.Role)})
	}
	return out
}

// otherUnitRolesView is what a member holds in the OTHER unit, shown on
// the roster edit page so a leader can see the whole picture.
//
// Labelled with that unit's labeler, not this one: a custom role belongs
// to the unit that created it, so resolving the Troop's "Committee Chair"
// against the Pack's custom roles would fall through to the prettified
// slug and could quietly show a different name than the Troop does.
type otherUnitRolesView struct {
	UnitName   string
	RoleLabels []string
}

func (h *Handlers) labelOtherUnitRoles(ctx context.Context, other []roster.OtherUnitRoles) []otherUnitRolesView {
	out := make([]otherUnitRolesView, 0, len(other))
	for _, o := range other {
		l := h.roleLabeler(ctx, o.UnitID)
		out = append(out, otherUnitRolesView{UnitName: o.UnitName, RoleLabels: l.Labels(o.Roles)})
	}
	return out
}
