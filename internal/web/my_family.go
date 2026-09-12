package web

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/roster"
	"github.com/47-yonkers/scout-site/internal/units"
)

// This file is the household contact page — /my-family — where an adult
// in a family manages the email, phone and address on file for everyone
// in it, and decides which of those the rest of the unit may see (see
// migration 0015).
//
// Deliberately not gated by CanEditUnitContent: a family updates its own
// details without needing any leader role, the same "manage your own
// stuff" posture as /accounts. It IS gated on being an adult in that
// family, which is the one permission rule on this page:
//
//   - An individual member login (see auth.User.MemberID) reaches it
//     only if that member is an adult. A Scout's own login does not —
//     their contact details and what is shared about them are a parent
//     or guardian's call, and this is the page where that call gets
//     made. They are told to ask one rather than shown a page they
//     can't use.
//   - A family-wide login is the household's login, so it reaches the
//     page as long as the family actually has an adult in it.
//
// An adult's change to a share toggle replaces what that person set for
// themselves. That is the point rather than a side effect: a parent
// deciding their child's phone number stays off the directory has to be
// able to make that stick.
//
// Nothing here touches passwords or logins. Who can sign in, and how, is
// /accounts and the roster admin pages; this page is contact details and
// their privacy, for people who may not have a login at all.

// myFamilyMember decorates roster.MemberDetail with what's on file in
// this unit specifically (den/patrol, roles) — read-only "here's your
// current information" context alongside the editable contact fields
// below it. Sourced from family.RosterForUnit rather than a new query:
// a unit's roster is small, and this is the same lookup /roster already
// does for everyone, just filtered down to this one family.
type myFamilyMember struct {
	roster.MemberDetail
	SubGroupName string
	// RoleLabels, not slugs: this page is read by families.
	RoleLabels []string
}

// adultInOwnFamily is the one permission question this page asks. See
// the file comment for why it is the question.
//
// An error is reported as "no": a page that shows a household's contact
// details is not something to fall open when the check itself fails.
func (h *Handlers) adultInOwnFamily(ctx context.Context, user auth.User) (bool, error) {
	if user.MemberID != nil {
		m, found, err := family.GetMember(ctx, h.Pool, *user.MemberID)
		if err != nil || !found {
			return false, err
		}
		return m.MemberType == "adult", nil
	}
	return family.HasAdult(ctx, h.Pool, user.FamilyID)
}

// notAnAdultMsg is what a Scout's own login is told instead.
const notAnAdultMsg = "Contact details for your family are managed by a parent or guardian — ask them to " +
	"sign in and open My Family. (Your login, password and security keys are still yours, under Security.)"

// requireFamilyAdult resolves the login and checks it, writing the
// refusal itself. Shared by all three handlers on this page so the page
// and the two forms that post to it can never disagree about who may.
func (h *Handlers) requireFamilyAdult(w http.ResponseWriter, r *http.Request, next string) (auth.User, bool) {
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next="+next, http.StatusSeeOther)
		return auth.User{}, false
	}
	adult, err := h.adultInOwnFamily(r.Context(), user)
	if err != nil {
		log.Printf("web: checking whether this login is an adult in its family: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return auth.User{}, false
	}
	if !adult {
		http.Error(w, notAnAdultMsg, http.StatusForbidden)
		return auth.User{}, false
	}
	return user, true
}

func (h *Handlers) MyFamily(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, ok := h.requireFamilyAdult(w, r, "/my-family")
	if !ok {
		return
	}

	// Everyone in the household, including the Scouts — an adult manages
	// all of it, which is the whole point of the page. (It used to show
	// an individual login only its own row, back when a Scout's login
	// could open this too.)
	var memberIDs []string
	members, err := family.MembersForFamily(r.Context(), h.Pool, user.FamilyID)
	if err != nil {
		log.Printf("web: loading family members: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for _, m := range members {
		memberIDs = append(memberIDs, m.ID)
	}

	unitRoster, err := family.RosterForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading unit roster: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	unitRoster = h.labelRoster(r.Context(), unit.ID, unitRoster)
	onFile := make(map[string]family.RosterEntry, len(unitRoster))
	for _, e := range unitRoster {
		onFile[e.ID] = e
	}

	var details []myFamilyMember
	for _, id := range memberIDs {
		d, found, err := roster.GetMember(r.Context(), h.Pool, id)
		if err != nil {
			log.Printf("web: loading member %s: %v", id, err)
			continue
		}
		if !found {
			continue
		}
		m := myFamilyMember{MemberDetail: d}
		if e, ok := onFile[id]; ok {
			m.SubGroupName = e.SubGroupName
			m.RoleLabels = e.RoleLabels
		}
		details = append(details, m)
	}

	data := struct {
		baseData
		Members []myFamilyMember
	}{
		baseData: h.base(r, "My Family"),
		Members:  details,
	}
	h.render(w, h.myFamily, data)
}

func (h *Handlers) MyFamilyUpdateMember(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, ok := h.requireFamilyAdult(w, r, "/my-family")
	if !ok {
		return
	}
	memberID := r.PathValue("id")

	// Being an adult is permission over your OWN household and no one
	// else's — the member id still has to be one of this family's.
	owns, err := family.MemberBelongsToFamily(r.Context(), h.Pool, memberID, user.FamilyID)
	if err != nil {
		log.Printf("web: checking family membership: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !owns {
		http.Error(w, "that's not someone in your family", http.StatusForbidden)
		return
	}

	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member", http.StatusBadRequest)
		return
	}

	if err := roster.SetContactInfo(r.Context(), h.Pool, memberID,
		strings.TrimSpace(r.FormValue("email")), strings.TrimSpace(r.FormValue("home_phone")), strings.TrimSpace(r.FormValue("cell_phone")),
		r.FormValue("release_email") == "1", r.FormValue("release_phone") == "1", actor.ID); err != nil {
		log.Printf("web: updating contact info: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/my-family", http.StatusSeeOther)
}

func (h *Handlers) FamilyDirectory(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	if _, _, ok := h.requireUnitMember(w, r, "/directory"); !ok {
		return
	}

	entries, err := roster.DirectoryForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading family directory: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := struct {
		baseData
		Entries []roster.DirectoryEntry
	}{
		baseData: h.base(r, "Family Directory"),
		Entries:  entries,
	}
	h.render(w, h.familyDirectory, data)
}

// DirectoryExportPDF generates a downloadable PDF of the family directory —
// same release-filtered contact info the on-screen page shows, just laid
// out for printing/saving rather than browsing.
func (h *Handlers) DirectoryExportPDF(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	if _, _, ok := h.requireUnitMember(w, r, "/directory"); !ok {
		return
	}

	entries, err := roster.DirectoryForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading family directory: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	families := make([]pdfFamily, 0, len(entries))
	for _, e := range entries {
		fam := pdfFamily{Name: e.FamilyName, Address: e.Address}
		for _, m := range e.Members {
			fam.Members = append(fam.Members, pdfMember{
				Name:      m.FirstName + " " + m.LastName,
				Email:     m.Email,
				HomePhone: m.HomePhone,
				CellPhone: m.CellPhone,
			})
		}
		families = append(families, fam)
	}

	data, err := familyDirectoryPDF(unit.Name+" — Family Directory", families)
	if err != nil {
		log.Printf("web: rendering directory PDF: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writePDF(w, "family-directory.pdf", data)
}

func (h *Handlers) MyFamilyUpdateAddress(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, ok := h.requireFamilyAdult(w, r, "/my-family")
	if !ok {
		return
	}

	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member", http.StatusBadRequest)
		return
	}

	if err := roster.SetFamilyAddress(r.Context(), h.Pool, user.FamilyID,
		strings.TrimSpace(r.FormValue("address")), r.FormValue("release_address") == "1", actor.ID); err != nil {
		log.Printf("web: updating family address: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/my-family", http.StatusSeeOther)
}
