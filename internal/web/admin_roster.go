package web

import (
	"log"
	"net/http"
	"strings"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/roster"
	"github.com/47-yonkers/scout-site/internal/units"
)

// This file holds the self-service roster management admin pages —
// internal/roster's business logic wired up to HTTP. It replaces the
// direct-SQL-insert workflow called out as a Phase 1 gap in README.md.
//
// Every handler here re-derives the acting family's units.CanEditUnitContent
// gate (same one calendar/homepage editing already uses) and, on top of
// that, a roster.Scope (unit-wide vs. a specific den/patrol) since roster
// data is more sensitive than a calendar entry and the requirements doc is
// explicit that a Den Leader only manages "their den."

// subGroupNoun returns "den" or "patrol" for unit-type-aware labels.
func subGroupNoun(unitType string) string {
	if unitType == "troop" {
		return "patrol"
	}
	return "den"
}

// requireRosterEditor is the common auth+scope preamble every handler below
// needs: logged in, holds CanEditUnitContent, and their roster.Scope. Writes
// an HTTP error/redirect and returns ok=false if the request should stop
// here.
func (h *Handlers) requireRosterEditor(w http.ResponseWriter, r *http.Request, redirectPath string) (unit units.Unit, actor family.Member, scope roster.Scope, ok bool) {
	unit, _ = units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next="+redirectPath, http.StatusSeeOther)
		return unit, family.Member{}, roster.Scope{}, false
	}

	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanEditUnitContent(caps) {
		http.Error(w, "you don't have permission to manage the roster", http.StatusForbidden)
		return unit, family.Member{}, roster.Scope{}, false
	}

	scope, err = h.rosterScope(r.Context(), user, unit.ID)
	if err != nil {
		log.Printf("web: computing roster scope: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return unit, family.Member{}, roster.Scope{}, false
	}

	actor, err = h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member — has your family been added to the roster yet?", http.StatusBadRequest)
		return unit, family.Member{}, roster.Scope{}, false
	}

	return unit, actor, scope, true
}

// resolveSubGroup validates a submitted sub_group_id against both the
// acting scope (is this leader allowed to touch this den/patrol?) and the
// unit (does it actually belong to the site we're on?). Returns a nil
// pointer with no error for an intentionally blank selection.
func (h *Handlers) resolveSubGroup(r *http.Request, unit units.Unit, scope roster.Scope, submitted string) (*string, error) {
	submitted = strings.TrimSpace(submitted)
	if submitted == "" {
		if !scope.UnitWide {
			// A scoped leader's entire scope IS a specific den/patrol, so
			// leaving this blank would create a member/role they couldn't
			// manage afterward (CanManageMember has nothing to match against
			// a null sub_group_id) — require them to pick one of their own.
			return nil, errBadRequest("choose a " + subGroupNoun(unit.UnitType))
		}
		return nil, nil
	}
	if !scope.CanManageSubGroup(submitted) {
		return nil, errForbidden("you don't have permission to assign that " + subGroupNoun(unit.UnitType))
	}
	owningUnit, ok, err := roster.SubGroupUnitID(r.Context(), h.Pool, submitted)
	if err != nil {
		return nil, err
	}
	if !ok || owningUnit != unit.ID {
		return nil, errBadRequest("that " + subGroupNoun(unit.UnitType) + " doesn't belong to this site")
	}
	return &submitted, nil
}

// httpStatusError lets resolveSubGroup (and friends) carry an HTTP status
// alongside a message without every caller re-deciding which status a given
// validation failure deserves.
type httpStatusError struct {
	status int
	msg    string
}

func (e httpStatusError) Error() string { return e.msg }

func errForbidden(msg string) error  { return httpStatusError{http.StatusForbidden, msg} }
func errBadRequest(msg string) error { return httpStatusError{http.StatusBadRequest, msg} }

// writeError renders err with its carried HTTP status if it's an
// httpStatusError, or 500 otherwise.
func writeError(w http.ResponseWriter, err error) {
	if se, ok := err.(httpStatusError); ok {
		http.Error(w, se.msg, se.status)
		return
	}
	log.Printf("web: roster admin: %v", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// --- Roster list / add ------------------------------------------------

type rosterRow struct {
	family.RosterEntry
	Editable bool
}

func (h *Handlers) AdminRosterList(w http.ResponseWriter, r *http.Request) {
	unit, _, scope, ok := h.requireRosterEditor(w, r, "/admin/roster")
	if !ok {
		return
	}

	subGroups, err := roster.SubGroupsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading sub-groups: %v", err)
	}
	var addableSubGroups []roster.SubGroup
	for _, sg := range subGroups {
		if scope.CanManageSubGroup(sg.ID) {
			addableSubGroups = append(addableSubGroups, sg)
		}
	}

	families, err := roster.AllFamilies(r.Context(), h.Pool)
	if err != nil {
		log.Printf("web: loading families: %v", err)
	}

	entries, err := family.RosterForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading roster: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	manageable, err := roster.ManageableMemberIDs(r.Context(), h.Pool, unit.ID, scope)
	if err != nil {
		log.Printf("web: computing manageable members: %v", err)
	}
	rows := make([]rosterRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, rosterRow{RosterEntry: e, Editable: scope.UnitWide || manageable[e.ID]})
	}

	allowedRoles, err := roster.AllowedRoles(r.Context(), h.Pool, unit.UnitType, unit.ID, scope)
	if err != nil {
		log.Printf("web: loading allowed roles: %v", err)
	}

	data := struct {
		baseData
		Scope            roster.Scope
		SubGroupNoun     string
		SubGroups        []roster.SubGroup
		AddableSubGroups []roster.SubGroup
		Families         []roster.FamilyOption
		Roles            []roster.RoleOption
		Roster           []rosterRow
	}{
		baseData:         h.base(r, "Manage Roster"),
		Scope:            scope,
		SubGroupNoun:     subGroupNoun(unit.UnitType),
		SubGroups:        subGroups,
		AddableSubGroups: addableSubGroups,
		Families:         families,
		Roles:            allowedRoles,
		Roster:           rows,
	}
	h.render(w, h.rosterAdmin, data)
}

// AdminRosterCreateFamily handles "Add a New Family": creates the family,
// its login, its first adult member, and assigns the chosen role, all in
// one submission, then shows the generated temporary password once.
func (h *Handlers) AdminRosterCreateFamily(w http.ResponseWriter, r *http.Request) {
	unit, actor, scope, ok := h.requireRosterEditor(w, r, "/admin/roster")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	role := r.FormValue("role")
	if allowed, err := roster.IsAllowedRole(r.Context(), h.Pool, unit.UnitType, unit.ID, scope, role); err != nil || !allowed {
		http.Error(w, "you don't have permission to assign that role", http.StatusForbidden)
		return
	}
	subGroupPtr, err := h.resolveSubGroup(r, unit, scope, r.FormValue("sub_group_id"))
	if err != nil {
		writeError(w, err)
		return
	}

	_, memberID, tempPassword, err := roster.CreateFamilyWithMember(r.Context(), h.Pool, roster.NewFamilyInput{
		FamilyName: strings.TrimSpace(r.FormValue("family_name")),
		Email:      r.FormValue("email"),
		FirstName:  strings.TrimSpace(r.FormValue("first_name")),
		LastName:   strings.TrimSpace(r.FormValue("last_name")),
	}, actor.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := roster.AssignRole(r.Context(), h.Pool, memberID, unit.ID, subGroupPtr, role, actor.ID); err != nil {
		log.Printf("web: assigning role to new family's member: %v", err)
	}

	h.renderCredentials(w, r, "Family created", r.FormValue("email"), tempPassword)
}

// AdminRosterAddMember handles "Add a Member to an Existing Family" —
// e.g. a second Scout joining a family already in the system.
func (h *Handlers) AdminRosterAddMember(w http.ResponseWriter, r *http.Request) {
	unit, actor, scope, ok := h.requireRosterEditor(w, r, "/admin/roster")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	familyID := r.FormValue("family_id")
	if familyID == "" {
		http.Error(w, "choose a family", http.StatusBadRequest)
		return
	}
	memberType := r.FormValue("member_type")
	if memberType != "adult" && memberType != "youth" {
		http.Error(w, "invalid member type", http.StatusBadRequest)
		return
	}
	role := r.FormValue("role")
	if allowed, err := roster.IsAllowedRole(r.Context(), h.Pool, unit.UnitType, unit.ID, scope, role); err != nil || !allowed {
		http.Error(w, "you don't have permission to assign that role", http.StatusForbidden)
		return
	}
	subGroupPtr, err := h.resolveSubGroup(r, unit, scope, r.FormValue("sub_group_id"))
	if err != nil {
		writeError(w, err)
		return
	}

	memberID, err := roster.AddMember(r.Context(), h.Pool, familyID,
		strings.TrimSpace(r.FormValue("first_name")), strings.TrimSpace(r.FormValue("last_name")), memberType, actor.ID)
	if err != nil {
		log.Printf("web: adding member: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := roster.AssignRole(r.Context(), h.Pool, memberID, unit.ID, subGroupPtr, role, actor.ID); err != nil {
		log.Printf("web: assigning role to new member: %v", err)
	}

	http.Redirect(w, r, "/admin/roster", http.StatusSeeOther)
}

// AdminRosterCreateSubGroup adds a new den/patrol. Unit-wide leaders only —
// creating a new organizational group is above a single Den Leader's scope.
func (h *Handlers) AdminRosterCreateSubGroup(w http.ResponseWriter, r *http.Request) {
	unit, actor, scope, ok := h.requireRosterEditor(w, r, "/admin/roster")
	if !ok {
		return
	}
	if !scope.UnitWide {
		http.Error(w, "only unit-wide leaders can add a new "+subGroupNoun(unit.UnitType), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	if _, err := roster.CreateSubGroup(r.Context(), h.Pool, unit.ID, name, subGroupNoun(unit.UnitType), actor.ID); err != nil {
		log.Printf("web: creating sub-group: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/roster", http.StatusSeeOther)
}

// --- Member edit --------------------------------------------------------

func (h *Handlers) AdminRosterMemberEdit(w http.ResponseWriter, r *http.Request) {
	unit, _, scope, ok := h.requireRosterEditor(w, r, r.URL.Path)
	if !ok {
		return
	}
	memberID := r.PathValue("id")

	member, found, err := roster.GetMember(r.Context(), h.Pool, memberID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	manageable, err := scope.CanManageMember(r.Context(), h.Pool, memberID, unit.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !manageable {
		http.Error(w, "this member is outside your "+subGroupNoun(unit.UnitType), http.StatusForbidden)
		return
	}

	memberRoles, err := roster.RolesForMemberInUnit(r.Context(), h.Pool, memberID, unit.ID)
	if err != nil {
		log.Printf("web: loading member roles: %v", err)
	}
	subGroups, err := roster.SubGroupsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading sub-groups: %v", err)
	}
	var addableSubGroups []roster.SubGroup
	for _, sg := range subGroups {
		if scope.CanManageSubGroup(sg.ID) {
			addableSubGroups = append(addableSubGroups, sg)
		}
	}

	// Individual login state — see roster.MemberHasLogin/MemberLoginEmail —
	// drives whether the page offers "create an individual login" or
	// "reset its password" (see admin-roster-member.html). Every member
	// type is eligible now, not just adults: the whole point of an
	// individual login is letting a Scout (youth) log in as themselves.
	individualLoginEmail, hasIndividualLogin, err := roster.MemberLoginEmail(r.Context(), h.Pool, memberID)
	if err != nil {
		log.Printf("web: loading individual login state: %v", err)
	}

	allowedRoles, err := roster.AllowedRoles(r.Context(), h.Pool, unit.UnitType, unit.ID, scope)
	if err != nil {
		log.Printf("web: loading allowed roles: %v", err)
	}

	data := struct {
		baseData
		Scope                roster.Scope
		SubGroupNoun         string
		Member               roster.MemberDetail
		Roles                []roster.RoleAssignment
		AllowedRoles         []roster.RoleOption
		AddableSubGroups     []roster.SubGroup
		HasIndividualLogin   bool
		IndividualLoginEmail string
	}{
		baseData:             h.base(r, "Edit "+member.FirstName+" "+member.LastName),
		Scope:                scope,
		SubGroupNoun:         subGroupNoun(unit.UnitType),
		Member:               member,
		Roles:                memberRoles,
		AllowedRoles:         allowedRoles,
		AddableSubGroups:     addableSubGroups,
		HasIndividualLogin:   hasIndividualLogin,
		IndividualLoginEmail: individualLoginEmail,
	}
	h.render(w, h.rosterMemberEdit, data)
}

// AdminRosterCreateMemberLogin creates a brand-new individual login for one
// member (see roster.CreateMemberLogin) — e.g. giving a Scout their own
// login separate from their family's shared one. Scoped by the same
// manageability check as every other roster-admin write.
func (h *Handlers) AdminRosterCreateMemberLogin(w http.ResponseWriter, r *http.Request) {
	unit, actor, scope, ok := h.requireRosterEditor(w, r, "/admin/roster")
	if !ok {
		return
	}
	memberID := r.PathValue("id")

	member, found, err := roster.GetMember(r.Context(), h.Pool, memberID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	manageable, err := scope.CanManageMember(r.Context(), h.Pool, memberID, unit.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !manageable {
		http.Error(w, "this member is outside your "+subGroupNoun(unit.UnitType), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" {
		http.Error(w, "an email address is required", http.StatusBadRequest)
		return
	}

	tempPassword, err := roster.CreateMemberLogin(r.Context(), h.Pool, memberID, member.FamilyID, email, actor.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	h.renderCredentials(w, r, "Individual login created for "+member.FirstName+" "+member.LastName, email, tempPassword)
}

// AdminRosterResetMemberLoginPassword resets one member's individual
// login's password (see roster.ResetMemberLoginPassword) — the
// member-scoped sibling of AdminRosterResetPassword, which only ever
// touches the family-wide login.
func (h *Handlers) AdminRosterResetMemberLoginPassword(w http.ResponseWriter, r *http.Request) {
	unit, actor, scope, ok := h.requireRosterEditor(w, r, "/admin/roster")
	if !ok {
		return
	}
	memberID := r.PathValue("id")

	member, found, err := roster.GetMember(r.Context(), h.Pool, memberID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	manageable, err := scope.CanManageMember(r.Context(), h.Pool, memberID, unit.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !manageable {
		http.Error(w, "this member is outside your "+subGroupNoun(unit.UnitType), http.StatusForbidden)
		return
	}

	tempPassword, err := roster.ResetMemberLoginPassword(r.Context(), h.Pool, memberID, actor.ID)
	if err != nil {
		log.Printf("web: resetting individual login password: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	email, _, err := roster.MemberLoginEmail(r.Context(), h.Pool, memberID)
	if err != nil {
		log.Printf("web: loading individual login email: %v", err)
	}

	h.renderCredentials(w, r, "Individual login password reset for "+member.FirstName+" "+member.LastName, email, tempPassword)
}

func (h *Handlers) AdminRosterMemberUpdate(w http.ResponseWriter, r *http.Request) {
	unit, actor, scope, ok := h.requireRosterEditor(w, r, "/admin/roster")
	if !ok {
		return
	}
	memberID := r.PathValue("id")
	manageable, err := scope.CanManageMember(r.Context(), h.Pool, memberID, unit.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !manageable {
		http.Error(w, "this member is outside your "+subGroupNoun(unit.UnitType), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	memberType := r.FormValue("member_type")
	if memberType != "adult" && memberType != "youth" {
		http.Error(w, "invalid member type", http.StatusBadRequest)
		return
	}

	if err := roster.UpdateMember(r.Context(), h.Pool, memberID,
		strings.TrimSpace(r.FormValue("first_name")), strings.TrimSpace(r.FormValue("last_name")), memberType, actor.ID); err != nil {
		log.Printf("web: updating member: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/roster/members/"+memberID, http.StatusSeeOther)
}

func (h *Handlers) AdminRosterAssignRole(w http.ResponseWriter, r *http.Request) {
	unit, actor, scope, ok := h.requireRosterEditor(w, r, "/admin/roster")
	if !ok {
		return
	}
	memberID := r.PathValue("id")
	manageable, err := scope.CanManageMember(r.Context(), h.Pool, memberID, unit.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !manageable {
		http.Error(w, "this member is outside your "+subGroupNoun(unit.UnitType), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	role := r.FormValue("role")
	if allowed, err := roster.IsAllowedRole(r.Context(), h.Pool, unit.UnitType, unit.ID, scope, role); err != nil || !allowed {
		http.Error(w, "you don't have permission to assign that role", http.StatusForbidden)
		return
	}
	subGroupPtr, err := h.resolveSubGroup(r, unit, scope, r.FormValue("sub_group_id"))
	if err != nil {
		writeError(w, err)
		return
	}

	if err := roster.AssignRole(r.Context(), h.Pool, memberID, unit.ID, subGroupPtr, role, actor.ID); err != nil {
		log.Printf("web: assigning role: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/roster/members/"+memberID, http.StatusSeeOther)
}

func (h *Handlers) AdminRosterRemoveRole(w http.ResponseWriter, r *http.Request) {
	unit, actor, scope, ok := h.requireRosterEditor(w, r, "/admin/roster")
	if !ok {
		return
	}
	roleAssignmentID := r.PathValue("id")

	ra, found, err := roster.GetRoleAssignment(r.Context(), h.Pool, roleAssignmentID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !found {
		http.Redirect(w, r, "/admin/roster", http.StatusSeeOther) // already gone
		return
	}
	if ra.UnitID != unit.ID {
		http.Error(w, "that role assignment doesn't belong to this site", http.StatusBadRequest)
		return
	}
	manageable, err := scope.CanManageMember(r.Context(), h.Pool, ra.MemberID, unit.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !manageable {
		http.Error(w, "this member is outside your "+subGroupNoun(unit.UnitType), http.StatusForbidden)
		return
	}

	if _, _, err := roster.RemoveRole(r.Context(), h.Pool, roleAssignmentID, actor.ID); err != nil {
		log.Printf("web: removing role: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/roster/members/"+ra.MemberID, http.StatusSeeOther)
}

// AdminRosterResetPassword generates a new temporary password for the
// family a member belongs to. Scoped by that member's manageability, same
// as editing them — a Den Leader can reset a password only for a family
// they can otherwise manage a member of.
func (h *Handlers) AdminRosterResetPassword(w http.ResponseWriter, r *http.Request) {
	unit, actor, scope, ok := h.requireRosterEditor(w, r, "/admin/roster")
	if !ok {
		return
	}
	memberID := r.PathValue("id")

	member, found, err := roster.GetMember(r.Context(), h.Pool, memberID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	manageable, err := scope.CanManageMember(r.Context(), h.Pool, memberID, unit.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !manageable {
		http.Error(w, "this member is outside your "+subGroupNoun(unit.UnitType), http.StatusForbidden)
		return
	}

	tempPassword, err := roster.ResetFamilyPassword(r.Context(), h.Pool, member.FamilyID, actor.ID)
	if err != nil {
		log.Printf("web: resetting family password: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	email, err := roster.FamilyEmail(r.Context(), h.Pool, member.FamilyID)
	if err != nil {
		log.Printf("web: loading family email: %v", err)
	}

	h.renderCredentials(w, r, "Password reset", email, tempPassword)
}

// renderCredentials shows a one-time confirmation screen with a login
// email + temporary password — the only place either flow surfaces the
// plaintext password, since only its bcrypt hash is ever stored.
func (h *Handlers) renderCredentials(w http.ResponseWriter, r *http.Request, heading, email, tempPassword string) {
	data := struct {
		baseData
		Heading      string
		Email        string
		TempPassword string
	}{
		baseData:     h.base(r, heading),
		Heading:      heading,
		Email:        email,
		TempPassword: tempPassword,
	}
	h.render(w, h.rosterCredentials, data)
}
