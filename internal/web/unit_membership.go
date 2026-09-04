package web

import (
	"context"
	"log"
	"net/http"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/units"
)

// Unit membership: "does this login belong to the unit whose site is being
// asked for", as distinct from "is this login signed in" and from "what is
// this login allowed to do here".
//
// The distinction matters because one login deliberately spans both units
// (see scout-website-requirements.md Section 2: a family with an older
// Scout in the Troop and a younger one in the Pack signs in once and uses
// both sites, and COOKIE_DOMAIN=.47-yonkers.org is set precisely to make
// that work). Signing in therefore says nothing about which unit's pages
// you may read.
//
// Before this existed, members-only pages checked only "signed in", so a
// Pack-only family could read the Troop's roster — names, and wherever a
// family had released them, parent email addresses and phone numbers —
// simply by visiting the other subdomain. It worked in both directions,
// and the roster PDF export came with it.
//
// Membership is held per person per unit in role_assignments, which is
// what makes the two-unit family work correctly here rather than being a
// special case: Robin Okonkwo holds a parent role in the Troop AND one in
// the Pack, so both checks pass, while a family with a Scout in only one
// unit passes only that one. Nothing about a shared login is treated
// differently from a single-unit one — the roles decide.

// isUnitMember reports whether this login holds any role in this unit.
//
// Any role at all is enough: this is the membership question, not the
// permission question. A parent, a Scout and a Scoutmaster are all
// equally "in the unit"; what separates them is capabilities, which every
// caller that cares already checks separately via capabilitiesFor.
//
// Deliberately built on rolesFor, the same lookup capabilitiesFor uses,
// so membership and permission can never disagree about who holds what.
func (h *Handlers) isUnitMember(ctx context.Context, user auth.User, unitID string) (bool, error) {
	roles, err := h.rolesFor(ctx, user, unitID)
	if err != nil {
		return false, err
	}
	return len(roles) > 0, nil
}

// viewerIsUnitMember answers the membership question for the current
// request, for the many places that previously branched on `loggedIn` to
// decide between public and members-only data.
//
// Returns false for a signed-out visitor and for a signed-in one who
// belongs to the other unit — both of whom should see exactly the public
// site. An error reading roles also returns false: failing closed shows
// too little rather than too much, and the alternative on a members-only
// page is leaking it to whoever provoked the error.
func (h *Handlers) viewerIsUnitMember(r *http.Request) bool {
	unit, ok := units.UnitFromContext(r.Context())
	if !ok {
		return false
	}
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		return false
	}
	member, err := h.isUnitMember(r.Context(), user, unit.ID)
	if err != nil {
		log.Printf("web: checking unit membership: %v", err)
		return false
	}
	return member
}

// requireUnitMember gates a page that only this unit's own families may
// see at all.
//
// A signed-out visitor is sent to log in, as before. A signed-in visitor
// who belongs to the other unit gets an explanation rather than a bare
// 403: following a link to the wrong unit's roster is an ordinary,
// innocent thing for a two-site family's friend to do, and "forbidden"
// with no reason reads like a fault.
func (h *Handlers) requireUnitMember(w http.ResponseWriter, r *http.Request, redirectPath string) (units.Unit, auth.User, bool) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next="+redirectPath, http.StatusSeeOther)
		return unit, auth.User{}, false
	}

	member, err := h.isUnitMember(r.Context(), user, unit.ID)
	if err != nil {
		log.Printf("web: checking unit membership: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return unit, user, false
	}
	if !member {
		http.Error(w, "This page is for "+unit.Name+" families, and your account isn't on "+unit.Name+"'s roster. "+
			"If your Scout is in the other unit, use that unit's website instead — the same login works there. "+
			"If you think this is wrong, ask a leader to check your family's roster entry.",
			http.StatusForbidden)
		return unit, user, false
	}
	return unit, user, true
}
