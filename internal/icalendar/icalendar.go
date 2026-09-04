// Package icalendar reads and writes the iCalendar format (RFC 5545) —
// the .ics files every calendar application on a phone or desktop speaks.
//
// It exists for the two directions the site needs and nothing more:
//
//   - Render turns this unit's events into a feed a family can subscribe
//     to from their phone (internal/web/calendar_feed.go).
//   - Parse reads somebody else's feed — a Google Calendar's secret
//     address, most often — so its events can be imported onto the unit
//     calendar (internal/calendar's feed import).
//
// Deliberately no third-party dependency. The subset of RFC 5545 that
// real calendars actually emit for ordinary events is small, and the
// alternative was taking a library with a much larger surface than the
// handful of properties below.
//
// What is deliberately NOT supported, because nothing here needs it:
// VTODO, VJOURNAL, VFREEBUSY, VALARM, attendee/organizer round-tripping,
// and embedded VTIMEZONE definitions (see timeParser's comment for how
// TZID is resolved instead).
package icalendar

import (
	"time"
	// Embeds the IANA timezone database in the binary.
	//
	// This is load-bearing in production and silently unnecessary in
	// development, which is exactly the combination that gets something
	// like it forgotten. The server ships on gcr.io/distroless/static
	// (see Dockerfile), an image with no /usr/share/zoneinfo at all, so
	// time.LoadLocation would fail for every TZID a real feed carries —
	// while working perfectly on any developer machine, which has the
	// system database. Importing this makes the lookup resolve from the
	// binary itself and behave the same in both places.
	_ "time/tzdata"
)

// Event is one calendar entry, in the small shape both directions of this
// package care about. It is deliberately not internal/calendar.Event:
// that type carries the site's own concerns (unit, visibility, approval
// status, sub-group), none of which survive a trip through an .ics file.
type Event struct {
	// UID is the globally unique identifier RFC 5545 requires. For events
	// we publish it is derived from the event's database id; for imported
	// ones it is whatever the source calendar used, which is what makes
	// re-importing the same feed an update rather than a duplicate.
	UID string

	Summary     string // the event title
	Description string
	Location    string
	URL         string

	Start time.Time
	End   time.Time // zero means "no end time given"

	// AllDay events are dates rather than instants: a camp that runs
	// "the 3rd to the 5th" rather than "18:00 to 20:00". They are written
	// as VALUE=DATE, and a phone shows them in the all-day band instead
	// of at a time.
	AllDay bool

	// LastModified drives whether an importing calendar treats a repeated
	// fetch as a change. Zero means "not stated".
	LastModified time.Time

	// Cancelled reports STATUS:CANCELLED on an imported event. A source
	// calendar usually keeps the entry and marks it rather than dropping
	// it, so an importer that ignored this would keep showing events that
	// have been called off.
	Cancelled bool
}

// crlf is required by RFC 5545 section 3.1 — lines are CRLF-delimited,
// not LF. Most parsers tolerate bare LF, but some phone clients do not,
// and a feed that fails to load on one brand of phone is a bad way to
// find out.
const crlf = "\r\n"

// foldAt is the octet limit RFC 5545 section 3.1 puts on a content line
// before it has to be folded onto a continuation line.
const foldAt = 75
