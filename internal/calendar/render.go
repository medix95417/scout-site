package calendar

import (
	"github.com/47-yonkers/scout-site/internal/icalendar"
)

// RenderFeed turns this unit's events into a subscribable .ics document.
//
// siteURL is the unit's public base ("https://troop.47-yonkers.org"),
// used to give each entry a link back to the event on the site — tapping
// an event on a phone then opens the real page, with its attachments and
// RSVP, rather than dead-ending in the calendar app.
func RenderFeed(unitName, siteURL string, events []Event) []byte {
	out := make([]icalendar.Event, 0, len(events))
	for _, e := range events {
		ie := icalendar.Event{
			// The event's own id makes the UID stable across refreshes,
			// so a subscribing client updates an entry in place instead
			// of deleting and re-adding it (which on some phones means a
			// spurious notification every time the feed is polled).
			UID:         e.ID + "@" + unitHost(siteURL),
			Summary:     e.Title,
			Description: e.Description,
			Location:    e.Location,
			Start:       e.StartsAt,
			URL:         siteURL + "/calendar",
		}
		if e.EndsAt != nil {
			ie.End = *e.EndsAt
		}
		out = append(out, ie)
	}
	return icalendar.Render(unitName, out)
}

// unitHost strips the scheme from a site URL, for the right-hand side of
// a UID. RFC 5545 wants a globally unique value and conventionally uses
// an address-like form; the exact text only matters in that it must be
// stable for a given event.
func unitHost(siteURL string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if len(siteURL) > len(prefix) && siteURL[:len(prefix)] == prefix {
			return siteURL[len(prefix):]
		}
	}
	return siteURL
}
