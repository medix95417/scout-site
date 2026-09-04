package web

import (
	"errors"
	"log"
	"net/http"

	"github.com/47-yonkers/scout-site/internal/calendar"
)

// Managing the external calendars this unit imports from. Gated on
// CanEditUnitContent — the same permission that lets someone create an
// event by hand, which is what importing amounts to at scale.

type feedRow struct {
	calendar.Feed
	LastFetched string // pre-formatted; "" if never
	Healthy     bool
}

type calendarFeedsData struct {
	baseData
	Feeds []feedRow
	Error string
}

func (h *Handlers) AdminCalendarFeeds(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireContentEditor(w, r, "/admin/calendar-feeds")
	if !ok {
		return
	}
	h.renderCalendarFeeds(w, r, unit.ID, r.URL.Query().Get("error"))
}

func (h *Handlers) renderCalendarFeeds(w http.ResponseWriter, r *http.Request, unitID, errMsg string) {
	feeds, err := calendar.FeedsForUnit(r.Context(), h.Pool, unitID)
	if err != nil {
		log.Printf("web: listing calendar feeds: %v", err)
	}
	rows := make([]feedRow, 0, len(feeds))
	for _, f := range feeds {
		row := feedRow{Feed: f, Healthy: f.LastStatus == "ok"}
		if f.LastFetchedAt != nil {
			row.LastFetched = f.LastFetchedAt.Format("2 Jan 2006, 15:04")
		}
		rows = append(rows, row)
	}
	h.render(w, h.calendarFeeds, calendarFeedsData{
		baseData: h.base(r, "Imported calendars"),
		Feeds:    rows,
		Error:    errMsg,
	})
}

func (h *Handlers) AdminCalendarFeedAdd(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/calendar-feeds")
	if !ok {
		return
	}

	feed, err := calendar.AddFeed(r.Context(), h.Pool, unit.ID,
		r.FormValue("name"), r.FormValue("url"), r.FormValue("visibility"), actor.ID)
	if err != nil {
		if errors.Is(err, calendar.ErrFeedURLNotAllowed) {
			// Deliberately specific: the two ways this realistically
			// happens are a pasted sharing page and a pasted internal
			// address, and neither is obvious from "invalid URL".
			h.renderCalendarFeeds(w, r, unit.ID,
				"That address can't be used. It needs to be a public http:// or https:// link to an .ics "+
					"calendar file — in Google Calendar, Settings → your calendar → Integrate calendar → "+
					"\"Secret address in iCal format\".")
			return
		}
		log.Printf("web: adding calendar feed: %v", err)
		h.renderCalendarFeeds(w, r, unit.ID, "Could not add that calendar. It may already be subscribed.")
		return
	}

	// Fetch straight away so the leader finds out now whether the address
	// works, rather than at the next scheduled refresh.
	res := calendar.RefreshFeed(r.Context(), h.Pool, calendar.NewFeedClient(), feed)
	if res.Err != nil {
		log.Printf("web: first fetch of calendar feed %s: %v", feed.ID, res.Err)
	}
	http.Redirect(w, r, "/admin/calendar-feeds", http.StatusSeeOther)
}

func (h *Handlers) AdminCalendarFeedRefresh(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireContentEditor(w, r, "/admin/calendar-feeds")
	if !ok {
		return
	}
	feed, err := calendar.GetFeed(r.Context(), h.Pool, r.PathValue("id"), unit.ID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if res := calendar.RefreshFeed(r.Context(), h.Pool, calendar.NewFeedClient(), feed); res.Err != nil {
		log.Printf("web: refreshing calendar feed %s: %v", feed.ID, res.Err)
	}
	http.Redirect(w, r, "/admin/calendar-feeds", http.StatusSeeOther)
}

func (h *Handlers) AdminCalendarFeedToggle(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/calendar-feeds")
	if !ok {
		return
	}
	enabled := r.FormValue("enabled") == "1"
	if err := calendar.SetFeedEnabled(r.Context(), h.Pool, r.PathValue("id"), unit.ID, enabled, actor.ID); err != nil {
		log.Printf("web: toggling calendar feed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/calendar-feeds", http.StatusSeeOther)
}

func (h *Handlers) AdminCalendarFeedDelete(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/calendar-feeds")
	if !ok {
		return
	}
	if err := calendar.DeleteFeed(r.Context(), h.Pool, r.PathValue("id"), unit.ID, actor.ID); err != nil {
		log.Printf("web: deleting calendar feed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/calendar-feeds", http.StatusSeeOther)
}
