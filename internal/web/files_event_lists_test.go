package web

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/files"
)

// The file library shows two lists of events and they are not the same
// list, which is the whole point and also the easy thing to undo. The
// filter offers only events with something attached — offering the rest
// meant picking a campout off a list of every campout the unit ever ran
// and being shown an empty page. The link controls offer every event,
// because linking is how a file gets attached in the first place, so a
// list of "events that already have files" could never grow.
//
// One data struct feeds both, so a single wrong field name silently
// merges them back together. Only a rendered page catches that.

func fileLibraryFixture() fileLibraryData {
	hasFiles := calendar.Event{ID: "e-camp", Title: "Summer Camp"}
	empty := calendar.Event{ID: "e-meeting", Title: "Troop Meeting"}

	return fileLibraryData{
		baseData: testBase("Files"),
		Events:   []calendar.Event{hasFiles, empty}, // every event
		// only the one with something attached
		FilterEvents:      []calendar.Event{hasFiles},
		SelectedEventIDs:  map[string]bool{},
		CanManage:         true,
		StorageConfigured: true,
		EventGroups: []eventFileGroupView{{
			EventID: "e-camp", EventTitle: "Summer Camp",
			Files: []fileRow{{
				File: files.File{ID: "f1", Filename: "camp.jpg", ContentType: "image/jpeg",
					Category: files.CategoryEventPhoto},
				SizeDisplay:    "100 B",
				LinkedEventIDs: map[string]bool{"e-camp": true},
			}},
		}},
	}
}

// checkboxIDs returns the event ids offered by every checkbox of the
// given input name, in document order.
func checkboxIDs(html, inputName string) []string {
	var out []string
	needle := `<input type="checkbox" name="` + inputName + `" value="`
	for rest := html; ; {
		i := strings.Index(rest, needle)
		if i == -1 {
			return out
		}
		rest = rest[i+len(needle):]
		out = append(out, rest[:strings.IndexByte(rest, '"')])
	}
}

func TestFilterOffersOnlyEventsWithFiles(t *testing.T) {
	out := renderPage(t, "files.html", fileLibraryFixture())

	// name="event_id" is the filter form; name="event_ids" is a link form.
	got := checkboxIDs(out, "event_id")
	if len(got) != 1 || got[0] != "e-camp" {
		t.Errorf("filter offers %v, want just [e-camp] — the empty event should not be listed", got)
	}
	if strings.Count(out, "Troop Meeting") == 0 {
		t.Error("the empty event vanished from the page entirely; it should still be linkable")
	}
}

func TestLinkControlsOfferEveryEvent(t *testing.T) {
	out := renderPage(t, "files.html", fileLibraryFixture())

	// Both link forms — the upload form's and the per-file one — use
	// name="event_ids", and both must offer the event that has nothing
	// attached yet, or nothing could ever be attached to it.
	got := checkboxIDs(out, "event_ids")
	if len(got) < 2 {
		t.Fatalf("only %d link checkboxes rendered: %v", len(got), got)
	}
	var sawEmpty, sawFull int
	for _, id := range got {
		switch id {
		case "e-meeting":
			sawEmpty++
		case "e-camp":
			sawFull++
		}
	}
	if sawEmpty == 0 {
		t.Error("an event with no files yet is not offered for linking, so nothing could ever be attached to it")
	}
	if sawEmpty != sawFull {
		t.Errorf("the two link forms disagree: %d offers of the empty event vs %d of the full one (%v)", sawEmpty, sawFull, got)
	}
}

// TestFilterHidesItselfWhenNothingIsAttached — with no event carrying a
// file, a filter offering nothing is worse than no filter, so the whole
// control goes away rather than rendering an empty box.
func TestFilterHidesItselfWhenNothingIsAttached(t *testing.T) {
	data := fileLibraryFixture()
	data.FilterEvents = nil
	out := renderPage(t, "files.html", data)

	if strings.Contains(out, "Filter/group by event") {
		t.Error("the filter renders with nothing to filter to")
	}
	// The link controls are unaffected — this is exactly the state a new
	// unit is in, and it still has to be able to attach its first photo.
	if len(checkboxIDs(out, "event_ids")) == 0 {
		t.Error("linking is unavailable when no event has files yet, which is when it is most needed")
	}
}
