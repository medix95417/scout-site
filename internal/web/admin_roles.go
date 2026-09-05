package web

import (
	"errors"
	"log"
	"net/http"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/roster"
	"github.com/47-yonkers/scout-site/internal/units"
)

// This file holds the custom-role admin pages — letting a super_admin
// create a new role on the fly (see internal/roster.CreateCustomRole) and
// pick which capabilities it grants, instead of every role being one of
// the 9 fixed, code-defined ones. Gated to IsSuperAdmin specifically, not
// just CanEditUnitContent: a custom role can be granted CapManageLedger
// or even CapSuperAdmin itself, so minting one is a highest-privilege
// operation, same reasoning as why AllowedRoles never offers super_admin
// on the regular roster-assignment form.

func (h *Handlers) requireSuperAdminForRoles(w http.ResponseWriter, r *http.Request) (units.Unit, bool) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next=/admin/custom-roles", http.StatusSeeOther)
		return unit, false
	}
	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.IsSuperAdmin(caps) {
		http.Error(w, "you don't have permission to manage roles", http.StatusForbidden)
		return unit, false
	}
	return unit, true
}

// capabilityOption is one checkbox on a role form.
type capabilityOption struct {
	Value string
	Label string
}

// capabilityOptions is built from units.AllCapabilities rather than
// written out by hand.
//
// It used to be a hand-written list, and it silently fell one behind:
// approve_expenses was added to the capability set and to custom_roles'
// CHECK constraint (migration 0039) but never to this list, so the
// documented escape hatch — "a unit that wants an Assistant Scoutmaster
// to authorize spending can grant it through a custom role" — had no
// checkbox to tick. Deriving the list means a new capability appears on
// the form the moment it exists.
var capabilityOptions = func() []capabilityOption {
	out := make([]capabilityOption, 0, len(units.AllCapabilities))
	for _, c := range units.AllCapabilities {
		out = append(out, capabilityOption{Value: c, Label: units.CapabilityLabel(c)})
	}
	return out
}()

// roleCapabilityView is one capability as a checkbox that knows whether
// it is currently granted — what both the custom-role edit form and the
// built-in role forms render.
type roleCapabilityView struct {
	Value   string
	Label   string
	Granted bool
}

func capabilityViews(granted []string) []roleCapabilityView {
	has := make(map[string]bool, len(granted))
	for _, c := range granted {
		has[c] = true
	}
	out := make([]roleCapabilityView, 0, len(units.AllCapabilities))
	for _, c := range units.AllCapabilities {
		out = append(out, roleCapabilityView{Value: c, Label: units.CapabilityLabel(c), Granted: has[c]})
	}
	return out
}

// customRoleView is one custom role with its capabilities already
// resolved into checkbox state, so the template holds no logic.
type customRoleView struct {
	roster.CustomRole
	CapabilityViews []roleCapabilityView
}

// systemRoleView is one built-in role, shown so a leader can see what it
// actually grants here — the review half of this page. Built-in roles
// can't be deleted or renamed, only re-pointed at a different set of
// capabilities, and put back to the default afterwards.
type systemRoleView struct {
	units.SystemRole
	CapabilityViews []roleCapabilityView
	// Relevant marks a role this kind of unit normally uses. Irrelevant
	// ones are still listed (a Pack may have inherited a Troop role from
	// an import) but shown apart, so the common case reads short.
	Relevant bool
}

func (h *Handlers) AdminCustomRolesList(w http.ResponseWriter, r *http.Request) {
	unit, ok := h.requireSuperAdminForRoles(w, r)
	if !ok {
		return
	}

	roles, err := roster.ListCustomRoles(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing custom roles: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	views := make([]customRoleView, 0, len(roles))
	for _, cr := range roles {
		views = append(views, customRoleView{CustomRole: cr, CapabilityViews: capabilityViews(cr.Capabilities)})
	}

	systemRoles, err := units.SystemRolesForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing built-in roles: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var relevant, other []systemRoleView
	for _, sr := range systemRoles {
		v := systemRoleView{
			SystemRole:      sr,
			CapabilityViews: capabilityViews(sr.Capabilities),
			Relevant:        sr.AppliesTo(unit.UnitType),
		}
		if v.Relevant {
			relevant = append(relevant, v)
		} else {
			other = append(other, v)
		}
	}

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
		baseData:         h.base(r, "Roles & Permissions"),
		Roles:            views,
		Capabilities:     capabilityOptions,
		SystemRoles:      relevant,
		OtherSystemRoles: other,
		// Which row, if any, opens with its edit form expanded — set by
		// the "Edit" links, so editing is a plain GET a browser can
		// bookmark and the back button understands.
		EditID:         r.URL.Query().Get("edit"),
		EditSystemRole: r.URL.Query().Get("edit_role"),
		SuperAdminRole: "super_admin",
	}
	h.render(w, h.customRoles, data)
}

// AdminCustomRolesUpdate renames a custom role and changes what it
// grants. The slug is untouched — see roster.UpdateCustomRole for why.
func (h *Handlers) AdminCustomRolesUpdate(w http.ResponseWriter, r *http.Request) {
	unit, ok := h.requireSuperAdminForRoles(w, r)
	if !ok {
		return
	}
	user, _ := auth.UserFromContext(r.Context())
	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member", http.StatusBadRequest)
		return
	}

	_, err = roster.UpdateCustomRole(r.Context(), h.Pool, r.PathValue("id"), unit.ID,
		r.FormValue("label"), r.Form["capabilities"], actor.ID)
	switch {
	case errors.Is(err, roster.ErrCustomRoleNotFound):
		http.Error(w, "that role no longer exists in this unit", http.StatusNotFound)
		return
	case err != nil:
		log.Printf("web: updating custom role: %v", err)
		http.Error(w, "could not save that role — check the name isn't blank", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/custom-roles", http.StatusSeeOther)
}

// AdminSystemRoleUpdate changes what a built-in role grants in this unit,
// or puts it back to the code's default.
//
// The two are one handler because "reset" is just "save the defaults" —
// see units.SetSystemRoleCapabilities, which clears the override row when
// the requested set matches, so the two paths can never disagree about
// what "default" means.
func (h *Handlers) AdminSystemRoleUpdate(w http.ResponseWriter, r *http.Request) {
	unit, ok := h.requireSuperAdminForRoles(w, r)
	if !ok {
		return
	}
	user, _ := auth.UserFromContext(r.Context())
	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member", http.StatusBadRequest)
		return
	}

	slug := r.PathValue("slug")
	granted := r.Form["capabilities"]
	if r.FormValue("reset") != "" {
		granted = units.DefaultCapabilitiesForRole(slug)
	}

	err = units.SetSystemRoleCapabilities(r.Context(), h.Pool, unit.ID, slug, granted, actor.ID)
	switch {
	case errors.Is(err, units.ErrCannotDisarmSuperAdmin):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		log.Printf("web: updating built-in role %q: %v", slug, err)
		http.Error(w, "could not save that role", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/custom-roles", http.StatusSeeOther)
}

func (h *Handlers) AdminCustomRolesCreate(w http.ResponseWriter, r *http.Request) {
	unit, ok := h.requireSuperAdminForRoles(w, r)
	if !ok {
		return
	}
	user, _ := auth.UserFromContext(r.Context())
	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member — has your family been added to the roster yet?", http.StatusBadRequest)
		return
	}

	label := r.FormValue("label")
	if label == "" {
		http.Error(w, "a name is required", http.StatusBadRequest)
		return
	}

	_, err = roster.CreateCustomRole(r.Context(), h.Pool, unit.ID, label, r.Form["capabilities"], actor.ID)
	if err != nil {
		if errors.Is(err, roster.ErrReservedRoleSlug) {
			http.Error(w, "that name is reserved for one of the site's built-in roles — choose a different name", http.StatusBadRequest)
			return
		}
		log.Printf("web: creating custom role: %v", err)
		http.Error(w, "a role with that name may already exist for this unit", http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/admin/custom-roles", http.StatusSeeOther)
}

func (h *Handlers) AdminCustomRolesDelete(w http.ResponseWriter, r *http.Request) {
	unit, ok := h.requireSuperAdminForRoles(w, r)
	if !ok {
		return
	}
	user, _ := auth.UserFromContext(r.Context())
	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member", http.StatusBadRequest)
		return
	}

	if err := roster.DeleteCustomRole(r.Context(), h.Pool, r.PathValue("id"), unit.ID, actor.ID); err != nil {
		log.Printf("web: deleting custom role: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/custom-roles", http.StatusSeeOther)
}
