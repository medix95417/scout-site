package web

// NOTE: this file is event_window.go, singular, and has to stay that
// way. Go reads a trailing "_windows" on a filename as a GOOS build
// constraint, so event_windows.go compiles on Windows only — which here
// meant every function in it silently vanished from the package and the
// build failed on the call sites instead, with nothing pointing at the
// filename.

import (
	"time"

	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/files"
)

// Linking a file to an event is a question with a direction in time, and
// which direction depends on what the file is.
//
// Photos and video arrive AFTER the thing they show: nobody uploads
// pictures of a campout that hasn't happened. Documents go the other way
// — a packing list, a permission form, a map is posted BEFORE the trip
// it belongs to. A unit that has been running for a few years has
// hundreds of events, and offering all of them in both cases is how a
// photo ends up filed under next spring's camporee because two events
// share a name.
//
// So each list opens on the events that plausibly match: the last 30
// days for a photo or video, the next 30 days for a document. Nothing is
// removed — every other event is one click away behind "show every
// event" — because "plausibly" is not "certainly", and a leader
// uploading last summer's photos in January is doing something perfectly
// ordinary.
const eventWindowLength = 30 * 24 * time.Hour

// The three buckets every event falls into, exactly one each.
const (
	// eventWindowRecent: started within the last 30 days and has already
	// started. What a photo/video upload offers.
	eventWindowRecent = "recent"
	// eventWindowUpcoming: starts within the next 30 days. What a
	// document upload offers.
	eventWindowUpcoming = "upcoming"
	// eventWindowOther: everything else — older than 30 days, or further
	// out than 30 days. Only shown when a leader asks to see everything.
	eventWindowOther = "other"
)

// classifyEventWindow buckets one event's start time relative to now.
//
// The two windows are disjoint by construction — an event that has
// started is never "upcoming", one that hasn't is never "recent" — so an
// event is rendered exactly once in a list that shows several buckets,
// and a checkbox is never duplicated (which would post the same event id
// twice on save).
func classifyEventWindow(startsAt, now time.Time) string {
	switch {
	case startsAt.After(now):
		if startsAt.Before(now.Add(eventWindowLength)) {
			return eventWindowUpcoming
		}
	default:
		if startsAt.After(now.Add(-eventWindowLength)) {
			return eventWindowRecent
		}
	}
	return eventWindowOther
}

// windowForCategory is which bucket a file of this category opens on.
// Anything that isn't an event photo is treated as a document, matching
// how files.CategoryGeneral is the fallback everywhere else.
func windowForCategory(category string) string {
	if category == files.CategoryEventPhoto {
		return eventWindowRecent
	}
	return eventWindowUpcoming
}

// eventChoice is one event as the "link this file to an event" controls
// render it: the event itself, plus which bucket it lands in so the
// template (and, on the upload form where the category can still change,
// a few lines of JavaScript) can show or hide it.
type eventChoice struct {
	calendar.Event
	Window string
}

// eventChoices classifies a unit's events for the link controls.
func eventChoices(events []calendar.Event, now time.Time) []eventChoice {
	out := make([]eventChoice, 0, len(events))
	for _, e := range events {
		out = append(out, eventChoice{Event: e, Window: classifyEventWindow(e.StartsAt, now)})
	}
	return out
}

// splitEventChoices divides a file's event list into the ones its
// checkbox list shows up front and the ones behind "show every event".
//
// An event this file is ALREADY linked to is always in the first group,
// whatever its date. That is not a nicety: the link form replaces the
// whole set with whatever comes back (see files.SetEventLinks), so a
// leader who opens it, ticks one more event and saves would otherwise be
// looking at a list that doesn't mention the link they already have. It
// would still be preserved — a checked box inside a closed <details>
// posts like any other — but "I can't see it" and "it's gone" are the
// same thing to the person doing it.
func splitEventChoices(choices []eventChoice, category string, linked map[string]bool) (shown, rest []eventChoice) {
	want := windowForCategory(category)
	for _, c := range choices {
		if c.Window == want || linked[c.ID] {
			shown = append(shown, c)
			continue
		}
		rest = append(rest, c)
	}
	return shown, rest
}

// eventsForFile / otherEventsForFile are splitEventChoices' two halves,
// as template functions.
//
// Two functions over one that returns a pair, because a Go template
// action can't destructure one; and computed per file at render time
// rather than stored on each row, because the alternative is every file
// in the library carrying its own copy of the unit's entire event list.
func eventsForFile(choices []eventChoice, category string, linked map[string]bool) []eventChoice {
	shown, _ := splitEventChoices(choices, category, linked)
	return shown
}

func otherEventsForFile(choices []eventChoice, category string, linked map[string]bool) []eventChoice {
	_, rest := splitEventChoices(choices, category, linked)
	return rest
}
