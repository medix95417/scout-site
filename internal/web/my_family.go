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

// This file is the self-service contact-info page — /my-family — where
// any logged-in login manages its own household's email/phone/address
// and whether each is released to the rest of the unit (see migration
// 0015). Deliberately not gated by CanEditUnitContent: a family should be
// able to update its own contact info and privacy choices without needing
// any leader role, same "manage your own stuff" posture as /accounts.
// An individual member login (see auth.User.MemberID) only ever manages
// its own contact fields, not the rest of its family's — matching the
// "just their own stuff" rule used everywhere else in this codebase — but
// still shares in editing the one household address, since that's their
// home too.

// myFamilyMember decorates roster.MemberDetail with what's on file in
// this unit specifically (den/patrol, roles) — read-only "here's your
// current information" context alongside the editable contact fields
// below it. Sourced from family.RosterForUnit rather than a new query:
// a unit's roster is small, and this is the same lookup /roster already
// does for everyone, just filtered down to this one family.
type myFamilyMember struct {
	roster.MemberDetail
	SubGroupName string
	Roles        []string
}

func (h *Handlers) MyFamily(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next=/my-family", http.StatusSeeOther)
		return
	}

	var memberIDs []string
	if user.MemberID != nil {
		memberIDs = []string{*user.MemberID}
	} else {
		members, err := family.MembersForFamily(r.Context(), h.Pool, user.FamilyID)
		if err != nil {
			log.Printf("web: loading family members: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, m := range members {
			memberIDs = append(memberIDs, m.ID)
		}
	}

	unitRoster, err := family.RosterForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading unit roster: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
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
			m.Roles = e.Roles
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
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	memberID := r.PathValue("id")

	owns := false
	var err error
	if user.MemberID != nil {
		owns = *user.MemberID == memberID
	} else {
		owns, err = family.MemberBelongsToFamily(r.Context(), h.Pool, memberID, user.FamilyID)
	}
	if err != nil {
		log.Printf("web: checking family membership: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !owns {
		http.Error(w, "that's not your own contact info to edit", http.StatusForbidden)
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
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
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
