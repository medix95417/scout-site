package calendar

import (
	"strings"
	"testing"
	"time"

	"github.com/47-yonkers/scout-site/internal/icalendar"
)

// A meeting at 7pm is at 7pm however it reached the site.
//
// That sounds like nothing, and it is the invariant that broke: an
// imported calendar carries a real timezone and gets converted to a real
// instant, while a time typed into the admin form is read as a wall
// clock in time.Local. The two only agree when time.Local is the unit's
// own zone. With the container left on UTC they differ by the local
// offset, and a 7pm meeting imported from a subscribed calendar showed
// up at 11pm — which reads as an import bug and is not one.
//
// Nothing in Go can assert what TZ the container will be started with,
// so what these pin is the relationship: given the right zone, the two
// paths agree; and the importer really is doing the conversion, so
// "fixing" this by making the importer ignore TZID would be a step
// backwards.

const formLayout = "2006-01-02T15:04" // internal/web's <input type="datetime-local">

// asTypedIntoTheForm is what internal/web does with a submitted time.
func asTypedIntoTheForm(t *testing.T, wallClock string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation(formLayout, wallClock, time.Local)
	if err != nil {
		t.Fatalf("parsing %q: %v", wallClock, err)
	}
	return parsed
}

// asImported is what a subscribed feed produces for the same meeting.
func asImported(t *testing.T, dtstart string) time.Time {
	t.Helper()
	feed := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:meeting-1\r\n" +
		dtstart + "\r\nSUMMARY:Troop meeting\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	events, err := icalendar.Parse(strings.NewReader(feed),
		time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("parsing the feed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events from the feed, want 1", len(events))
	}
	return events[0].Start
}

// withLocal runs fn with time.Local set to zone, restoring it after —
// this is what a container's TZ variable controls.
func withLocal(t *testing.T, zone string, fn func()) {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Skipf("no zoneinfo for %s in this environment", zone)
	}
	saved := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = saved })
	fn()
}

func TestTypedAndImportedTimesAgreeInTheUnitsZone(t *testing.T) {
	withLocal(t, "America/New_York", func() {
		for _, c := range []struct{ name, wall, dtstart string }{
			// Summer: the zone is UTC-4.
			{"EDT", "2026-09-15T19:00", "DTSTART;TZID=America/New_York:20260915T190000"},
			// Winter: UTC-5. A fix that hard-coded one offset fails here.
			{"EST", "2026-01-15T19:00", "DTSTART;TZID=America/New_York:20260115T190000"},
		} {
			typed, imported := asTypedIntoTheForm(t, c.wall), asImported(t, c.dtstart)
			if !typed.Equal(imported) {
				t.Errorf("%s: a 7pm meeting typed in is %s but imported is %s — %v apart",
					c.name, typed.UTC(), imported.UTC(), imported.Sub(typed))
			}
			if got := FormatDateRange(imported, nil); !strings.Contains(got, "7:00 PM") {
				t.Errorf("%s: the imported meeting displays as %q, want 7:00 PM", c.name, got)
			}
		}
	})
}

// TestOnUTCTheyDisagree documents the bug rather than the fix, so the
// four-hour gap is a fact in the test suite and not just in a commit
// message. If this ever stops holding, the two paths have been unified
// some other way and the comment above needs revisiting.
func TestOnUTCTheyDisagree(t *testing.T) {
	withLocal(t, "UTC", func() {
		typed := asTypedIntoTheForm(t, "2026-09-15T19:00")
		imported := asImported(t, "DTSTART;TZID=America/New_York:20260915T190000")
		if gap := imported.Sub(typed); gap != 4*time.Hour {
			t.Errorf("gap between a typed and an imported 7pm on a UTC host = %v, want 4h", gap)
		}
	})
}

// TestTheImporterHonoursTZID is the half that must not be "fixed" away.
// Reading the wall clock and ignoring the zone would make the two paths
// agree on a UTC host — and be wrong for every reader.
func TestTheImporterHonoursTZID(t *testing.T) {
	imported := asImported(t, "DTSTART;TZID=America/New_York:20260915T190000")
	if got := imported.UTC().Format(time.RFC3339); got != "2026-09-15T23:00:00Z" {
		t.Errorf("7pm America/New_York imported as %s, want 2026-09-15T23:00:00Z", got)
	}

	// A UTC timestamp is already an instant and must survive untouched.
	utc := asImported(t, "DTSTART:20260915T230000Z")
	if got := utc.UTC().Format(time.RFC3339); got != "2026-09-15T23:00:00Z" {
		t.Errorf("an explicit UTC timestamp came out as %s", got)
	}
}
