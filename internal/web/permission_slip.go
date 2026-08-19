package web

// This file is digital permission slips / consent forms —
// scout-website-requirements.md's Phase 3 "permission slips" candidate
// (see README.md's "Not in Phase 1" section). A leader (units.CanEditUnitContent,
// same gate as /admin/news) attaches one consent form to an event; a
// parent/guardian signs it once per Scout of theirs attending by typing
// their full name — see internal/permission's package comment for why
// this is per-Scout rather than per-family like an RSVP, and why editing
// a slip's text doesn't invalidate signatures already collected.
//
// Deliberately reachable only from an event's own page
// (/calendar/{id}/permission-slip), not a top-level nav item — a
// permission slip only ever makes sense in the context of one specific
// event.

import (
	"log"
	"net/http"
	"strings"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/permission"
	"github.com/47-yonkers/scout-site/internal/units"
)

type complianceRow struct {
	ScoutName  string
	Signed     bool
	SignerName string
	SignedOn   string
}

type signRow struct {
	ScoutMemberID string
	ScoutName     string
	Signed        bool
	SignerName    string
	SignedOn      string
}

func (h *Handlers) PermissionSlipView(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusSeeOther)
		return
	}

	eventID := r.PathValue("id")
	event, found, err := calendar.GetEvent(r.Context(), h.Pool, eventID, unit.ID)
	if err != nil {
		log.Printf("web: loading event for permission slip: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	roles, err := h.rolesFor(r.Context(), user, unit.ID)
	if err != nil {
		log.Printf("web: loading roles: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	canEdit := units.CanEditUnitContent(roles)

	slip, hasSlip, err := permission.GetSlipForEvent(r.Context(), h.Pool, eventID, unit.ID)
	if err != nil {
		log.Printf("web: loading permission slip: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var signatures []permission.Signature
	if hasSlip {
		signatures, err = permission.SignaturesForSlip(r.Context(), h.Pool, slip.ID)
		if err != nil {
			log.Printf("web: loading permission slip signatures: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	signedByScout := map[string]permission.Signature{}
	for _, sig := range signatures {
		signedByScout[sig.ScoutMemberID] = sig
	}

	var complianceRows []complianceRow
	var signRows []signRow
	canSign := false

	if canEdit && hasSlip {
		roster, err := family.RosterForUnit(r.Context(), h.Pool, unit.ID)
		if err != nil {
			log.Printf("web: loading roster for compliance view: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, m := range roster {
			if m.MemberType != "youth" {
				continue
			}
			row := complianceRow{ScoutName: m.FirstName + " " + m.LastName}
			if sig, ok := signedByScout[m.ID]; ok {
				row.Signed = true
				row.SignerName = sig.SignerName
				row.SignedOn = sig.SignedAt.Format("Mon Jan 2, 2006")
			}
			complianceRows = append(complianceRows, row)
		}
	}

	if hasSlip && user.MemberID == nil {
		// A family-wide login is exactly who's allowed to sign (see
		// PermissionSlipSign) — an individual Scout login only ever sees
		// their own read-only status below, never a sign form, since BSA
		// consent needs a parent/guardian's signature, not the Scout's own.
		canSign = true
		members, err := family.MembersForFamily(r.Context(), h.Pool, user.FamilyID)
		if err != nil {
			log.Printf("web: loading family members for permission slip: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, m := range members {
			if m.MemberType != "youth" {
				continue
			}
			row := signRow{ScoutMemberID: m.ID, ScoutName: m.FirstName + " " + m.LastName}
			if sig, ok := signedByScout[m.ID]; ok {
				row.Signed = true
				row.SignerName = sig.SignerName
				row.SignedOn = sig.SignedAt.Format("Mon Jan 2, 2006")
			}
			signRows = append(signRows, row)
		}
	} else if hasSlip && user.MemberID != nil {
		if m, found, err := family.GetMember(r.Context(), h.Pool, *user.MemberID); err == nil && found && m.MemberType == "youth" {
			row := signRow{ScoutMemberID: m.ID, ScoutName: m.FirstName + " " + m.LastName}
			if sig, ok := signedByScout[m.ID]; ok {
				row.Signed = true
				row.SignerName = sig.SignerName
				row.SignedOn = sig.SignedAt.Format("Mon Jan 2, 2006")
			}
			signRows = append(signRows, row)
		}
	}

	data := struct {
		baseData
		Event          calendar.Event
		HasSlip        bool
		Slip           permission.Slip
		CanEdit        bool
		CanSign        bool
		ComplianceRows []complianceRow
		SignRows       []signRow
	}{
		baseData:       h.base(r, "Permission Slip"),
		Event:          event,
		HasSlip:        hasSlip,
		Slip:           slip,
		CanEdit:        canEdit,
		CanSign:        canSign,
		ComplianceRows: complianceRows,
		SignRows:       signRows,
	}
	h.render(w, h.permissionSlip, data)
}

func (h *Handlers) PermissionSlipSave(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, r.URL.Path)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	eventID := r.PathValue("id")
	if _, found, err := calendar.GetEvent(r.Context(), h.Pool, eventID, unit.ID); err != nil {
		log.Printf("web: loading event for permission slip save: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !found {
		http.NotFound(w, r)
		return
	}

	title := strings.TrimSpace(r.PostFormValue("title"))
	body := r.PostFormValue("body")
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}

	existing, hasSlip, err := permission.GetSlipForEvent(r.Context(), h.Pool, eventID, unit.ID)
	if err != nil {
		log.Printf("web: checking for existing permission slip: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if hasSlip {
		_, err = permission.UpdateSlip(r.Context(), h.Pool, existing.ID, unit.ID, title, body, actor.ID)
	} else {
		_, err = permission.CreateSlip(r.Context(), h.Pool, eventID, unit.ID, title, body, actor.ID)
	}
	if err != nil {
		log.Printf("web: saving permission slip: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/calendar/"+eventID+"/permission-slip", http.StatusSeeOther)
}

func (h *Handlers) PermissionSlipSign(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Only a family-wide login may sign — see PermissionSlipView's
	// CanSign comment. An individual Scout login has no way to reach a
	// sign form in the rendered page, but the server enforces it too
	// rather than trusting that.
	if user.MemberID != nil {
		http.Error(w, "only a parent/guardian login can sign a permission slip", http.StatusForbidden)
		return
	}

	eventID := r.PathValue("id")
	slip, hasSlip, err := permission.GetSlipForEvent(r.Context(), h.Pool, eventID, unit.ID)
	if err != nil {
		log.Printf("web: loading permission slip for signing: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !hasSlip {
		http.NotFound(w, r)
		return
	}

	signerName := strings.TrimSpace(r.PostFormValue("signer_name"))
	if signerName == "" {
		http.Error(w, "type your full name to sign", http.StatusBadRequest)
		return
	}

	scoutMemberID := r.PostFormValue("scout_member_id")
	scout, found, err := family.GetMember(r.Context(), h.Pool, scoutMemberID)
	if err != nil {
		log.Printf("web: loading scout for permission slip signing: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Ownership check: this Scout must actually be a youth member of the
	// signing family — otherwise any logged-in family could sign consent
	// for someone else's Scout just by guessing/inspecting a member ID.
	if !found || scout.FamilyID != user.FamilyID || scout.MemberType != "youth" {
		http.Error(w, "that Scout isn't in your family", http.StatusForbidden)
		return
	}

	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member — has your family been added to the roster yet?", http.StatusBadRequest)
		return
	}

	if _, err := permission.Sign(r.Context(), h.Pool, slip.ID, scoutMemberID, actor.ID, signerName, actor.ID); err != nil {
		log.Printf("web: signing permission slip: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/calendar/"+eventID+"/permission-slip", http.StatusSeeOther)
}
