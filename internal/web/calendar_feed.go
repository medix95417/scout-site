package web

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/roster"
	"github.com/47-yonkers/scout-site/internal/units"
)

// The personal calendar subscription: a secret .ics address a family adds
// to the calendar app on their phone, so unit events appear alongside
// everything else in their life and update on their own.
//
// There is no anonymous feed. The unit's calendar carries members-only
// events and den/patrol-scoped ones, and an open link would either leak
// those or be limited to public events and therefore not worth
// subscribing to. Every link belongs to one person and shows what that
// person would see on /calendar.

// feedWindowPast is how far back the feed reaches. A calendar app wants
// some history so last month isn't blank when you scroll up, but a
// subscription is fundamentally about what's coming.
const feedWindowPast = 90 * 24 * time.Hour

// feedWindowFuture bounds the other end, which also bounds how far
// recurring imported events are expanded.
const feedWindowFuture = 365 * 24 * time.Hour

// CalendarFeed serves one person's .ics.
//
// Deliberately NOT behind auth.WithUser: a calendar client cannot log in,
// so the token in the path is the whole of the authentication. Everything
// this handler grants therefore follows from resolving that token, and it
// resolves it against the unit the hostname picked out, so a token issued
// for one subdomain is not valid on the other.
func (h *Handlers) CalendarFeed(w http.ResponseWriter, r *http.Request) {
	unit, ok := units.UnitFromContext(r.Context())
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Go's ServeMux wildcards match a whole path segment, so the route is
	// registered as {token} and the .ics suffix is stripped here. The
	// suffix is worth keeping in the URL: some calendar clients decide
	// how to treat a subscription by its file extension, and a human
	// glancing at the link can tell what it is.
	token := strings.TrimSuffix(r.PathValue("token"), ".ics")
	owner, err := calendar.ResolveFeedToken(r.Context(), h.Pool, token, unit.ID)
	if err != nil {
		if !errors.Is(err, calendar.ErrNoFeedToken) {
			log.Printf("web: resolving calendar feed token: %v", err)
		}
		// A revoked, mistyped, or wrong-unit token is a 404 rather than a
		// 401: there is nothing to authenticate with, and 404 tells a
		// prober nothing about whether the token merely expired.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	user, err := auth.UserByID(r.Context(), h.Pool, owner.UserID)
	if err != nil {
		log.Printf("web: loading feed token owner: %v", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	now := time.Now()
	events, err := calendar.ListForRangeForUnit(r.Context(), h.Pool, unit.ID,
		now.Add(-feedWindowPast), now.Add(feedWindowFuture))
	if err != nil {
		log.Printf("web: loading events for feed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Scoping happens on every request rather than being baked into the
	// token, so a Scout moving patrol or a parent's role ending is
	// reflected at the next refresh with nothing to reissue.
	//
	// This deliberately goes through the same capabilitiesFor and
	// FilterVisibleToViewer the /calendar page uses. Any other approach
	// would be a second implementation of "what may this person see",
	// free to drift away from the first.
	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil {
		log.Printf("web: loading capabilities for feed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var subGroupIDs []string
	if user.MemberID != nil {
		subGroupIDs, err = roster.SubGroupIDsForMember(r.Context(), h.Pool, *user.MemberID, unit.ID)
	} else {
		subGroupIDs, err = roster.SubGroupIDsForFamily(r.Context(), h.Pool, user.FamilyID, unit.ID)
	}
	if err != nil {
		log.Printf("web: loading sub-groups for feed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	viewerSubGroups := make(map[string]bool, len(subGroupIDs))
	for _, id := range subGroupIDs {
		viewerSubGroups[id] = true
	}
	events = calendar.FilterVisibleToViewer(events, viewerSubGroups, units.CanEditUnitContent(caps))

	body := calendar.RenderFeed(unit.Name, h.siteURL(r), events)

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	// Named so a manual download lands as something recognisable rather
	// than "feed".
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s.ics"`, unit.Slug))
	// The URL is a secret and its content is personal: no shared cache
	// anywhere should hold it.
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	if _, err := w.Write(body); err != nil {
		log.Printf("web: writing calendar feed: %v", err)
	}
}

// siteURL reconstructs this unit's public base URL, for the event links
// embedded in the feed. Behind Caddy the request itself always arrives
// over plain HTTP, so the scheme comes from the forwarded header when
// present rather than from r.TLS, which would always say "no".
func (h *Handlers) siteURL(r *http.Request) string {
	scheme := "https"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS == nil && (r.Host == "localhost" || len(r.Host) > 9 && r.Host[:9] == "127.0.0.1") {
		// Local development over plain HTTP.
		scheme = "http"
	}
	return scheme + "://" + r.Host
}
