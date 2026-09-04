package web

import (
	"log"
	"net/http"
	"time"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/calendar"
)

// The page where somebody creates, replaces, or removes the private
// calendar link they add to their phone. The feed itself is served by
// calendar_feed.go; this is only its management.

type calendarSubscribeData struct {
	baseData
	HasLink   bool
	CreatedAt string
	LastUsed  string // "" when the feed has never been fetched
	// NewURL is set exactly once, on the response to generating a link.
	// The token is stored hashed, so this is the only moment it can be
	// shown — reloading the page afterwards shows HasLink and no URL.
	NewURL string
	// WebcalURL is the same address with a webcal:// scheme, which phones
	// treat as "subscribe" rather than "download a copy". A downloaded
	// copy never updates, which is the single most common way this
	// feature disappoints people, so the subscribe form is offered first.
	WebcalURL string
}

func (h *Handlers) CalendarSubscribe(w http.ResponseWriter, r *http.Request) {
	unit, user, ok := h.requireUnitMember(w, r, "/settings/calendar")
	if !ok {
		return
	}

	data := calendarSubscribeData{baseData: h.base(r, "Calendar on your phone")}
	created, lastUsed, exists, err := calendar.FeedTokenExists(r.Context(), h.Pool, user.ID, unit.ID)
	if err != nil {
		log.Printf("web: loading calendar feed token state: %v", err)
	}
	if exists {
		data.HasLink = true
		data.CreatedAt = created.Format("2 January 2006")
		if lastUsed != nil {
			data.LastUsed = lastUsed.Format("2 January 2006, 15:04")
		}
	}
	h.render(w, h.calendarSubscribe, data)
}

// CalendarSubscribeRegenerate issues a new link, replacing any existing
// one.
//
// This renders the page directly rather than redirecting, which is a
// deliberate break from the Post/Redirect/Get pattern used elsewhere: the
// new token is only in memory for the length of this request, and a
// redirect would either lose it or require smuggling a secret through a
// query string, where it would land in logs and browser history.
func (h *Handlers) CalendarSubscribeRegenerate(w http.ResponseWriter, r *http.Request) {
	unit, user, ok := h.requireUnitMember(w, r, "/settings/calendar")
	if !ok {
		return
	}

	token, err := auth.RandomToken(calendar.FeedTokenBytes)
	if err != nil {
		log.Printf("web: generating calendar feed token: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := calendar.SetFeedToken(r.Context(), h.Pool, user.ID, unit.ID, token); err != nil {
		log.Printf("web: storing calendar feed token: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	full := h.siteURL(r) + "/feed/" + token + ".ics"
	data := calendarSubscribeData{
		baseData:  h.base(r, "Calendar on your phone"),
		HasLink:   true,
		CreatedAt: time.Now().Format("2 January 2006"),
		NewURL:    full,
		// webcal:// is not a real protocol so much as a convention every
		// major calendar app honours: it hands the URL to the calendar
		// rather than the browser, which is the difference between
		// subscribing and downloading a snapshot.
		WebcalURL: "webcal://" + unitHostFromURL(h.siteURL(r)) + "/feed/" + token + ".ics",
	}
	h.render(w, h.calendarSubscribe, data)
}

func (h *Handlers) CalendarSubscribeRemove(w http.ResponseWriter, r *http.Request) {
	unit, user, ok := h.requireUnitMember(w, r, "/settings/calendar")
	if !ok {
		return
	}
	if err := calendar.DeleteFeedToken(r.Context(), h.Pool, user.ID, unit.ID); err != nil {
		log.Printf("web: removing calendar feed token: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings/calendar", http.StatusSeeOther)
}

func unitHostFromURL(siteURL string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if len(siteURL) > len(prefix) && siteURL[:len(prefix)] == prefix {
			return siteURL[len(prefix):]
		}
	}
	return siteURL
}
