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

// capabilityOption is one checkbox on the "create a custom role" form.
type capabilityOption struct {
	Value string
	Label string
}

var capabilityOptions = []capabilityOption{
	{units.CapEditContent, "Edit content — homepage, news, gallery, calendar, roster (no approval needed)"},
	{units.CapApproveSubmissions, "Approve submissions — decide a pending SPL/Patrol-Leader event or post"},
	{units.CapSubmitForApproval, "Submit for approval — can propose content/events, but they need approval first"},
	{units.CapManageLedger, "Manage the treasury — ledger, trip funds, fundraisers"},
	{units.CapSuperAdmin, "Site settings — the highest privilege tier; also lets this role create more roles"},
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

	data := struct {
		baseData
		Roles        []roster.CustomRole
		Capabilities []capabilityOption
	}{
		baseData:     h.base(r, "Custom Roles"),
		Roles:        roles,
		Capabilities: capabilityOptions,
	}
	h.render(w, h.customRoles, data)
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
