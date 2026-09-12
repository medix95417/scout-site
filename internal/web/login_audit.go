package web

// Every session this app issues is created here, and recorded in the
// activity log as it is.
//
// Sign-ins are the one thing a leader looking at the log after something
// has gone wrong actually wants first — "who has been in this account,
// and from where" — and until now they were the one thing it didn't
// record. Password changes, role grants and roster edits were all
// there; the sign-in that preceded them wasn't.
//
// The address is recorded alongside, because "Sam signed in" answers
// half a question. It comes from clientIP (see client_ip.go), which is
// deliberately fussy about X-Forwarded-For — a visitor who can choose
// their own apparent address can write whatever they like into this log.

import (
	"log"
	"net/http"

	"github.com/47-yonkers/scout-site/internal/audit"
	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/units"
)

// How the sign-in was authenticated, as the activity log words it.
const (
	signInWithPassword      = "sign_in"
	signInWithAuthenticator = "sign_in (authenticator app)"
	signInWithSecurityKey   = "sign_in (security key)"
)

// startSession creates the session, sets its cookie, records the
// sign-in, and sends the visitor on to where they were going.
//
// One function for all three ways in — password alone, password plus an
// authenticator code, password plus a security key — so a fourth can't
// be added that quietly isn't logged. The pending-login cookies each
// route carries are cleared by the caller before it gets here; what's
// shared is everything from "this login is now authenticated" onwards.
func (h *Handlers) startSession(w http.ResponseWriter, r *http.Request, userID, next, action string) {
	token, expiresAt, err := auth.CreateSession(r.Context(), h.Pool, userID)
	if err != nil {
		log.Printf("web: creating session: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	auth.SetSessionCookie(w, token, expiresAt, h.CookieDomain, h.SecureCookie)
	h.logSignIn(r, userID, action)
	http.Redirect(w, r, sanitizeNextPath(next), http.StatusSeeOther)
}

// logSignIn writes the activity-log entry for a session that has just
// been issued.
//
// Best-effort, like every other audit write (see audit.Log): a log that
// can't be written is not a reason to refuse someone a session they have
// correctly authenticated for. A failure is logged to the process log,
// where a missing sign-in entry can at least be explained later.
//
// The entry is filed against the member who signed in, not the login
// row: that is how the activity log scopes an entry to a unit (see
// entityScopeSQL), and it means a sign-in shows up in the log of each
// unit that person holds a role in — which, for a family login that
// deliberately spans the Pack and the Troop, is the honest answer to
// "was this account used".
func (h *Handlers) logSignIn(r *http.Request, userID, action string) {
	unit, _ := units.UnitFromContext(r.Context())

	user, err := auth.UserByID(r.Context(), h.Pool, userID)
	if err != nil {
		log.Printf("web: loading login %s to record its sign-in: %v", userID, err)
		return
	}
	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		log.Printf("web: resolving the member behind login %s to record its sign-in: %v", userID, err)
		return
	}

	audit.Log(r.Context(), h.Pool, audit.Entry{
		EntityType: "login",
		EntityID:   actor.ID,
		ActorID:    &actor.ID,
		Action:     action,
		// "ip" is the key the activity log reads an address back out of
		// — see audit.LogEntry.IPAddress. The site is recorded too,
		// since one login reaches both and which one was used is part of
		// the story.
		After: map[string]string{
			"ip":   clientIP(r, h.TrustProxyHeaders),
			"site": unit.Slug,
		},
	})
}
