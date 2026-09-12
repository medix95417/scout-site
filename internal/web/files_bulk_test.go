package web

import (
	"strings"
	"testing"
	"time"

	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/files"
)

// windowedLibraryFixture is a library with one photo, one document, and
// four events spread across both windows and well outside them.
func windowedLibraryFixture(now time.Time) fileLibraryData {
	day := 24 * time.Hour
	events := []calendar.Event{
		{ID: "e-hike", Title: "Last weekend's hike", StartsAt: now.Add(-3 * day)},
		{ID: "e-campout", Title: "Next weekend's campout", StartsAt: now.Add(4 * day)},
		{ID: "e-oldcamp", Title: "Summer camp last year", StartsAt: now.Add(-300 * day)},
	}
	return fileLibraryData{
		baseData:          testBase("Files"),
		EventChoices:      eventChoices(events, now),
		FilterEvents:      nil,
		SelectedEventIDs:  map[string]bool{},
		CanManage:         true,
		StorageConfigured: true,
		EventGroups: []eventFileGroupView{{
			EventTitle: "Not linked to an event",
			Files: []fileRow{
				{
					File: files.File{ID: "photo-1", Filename: "camp.jpg", ContentType: "image/jpeg",
						Category: files.CategoryEventPhoto},
					SizeDisplay:    "100 B",
					LinkedEventIDs: map[string]bool{},
				},
				{
					File: files.File{ID: "doc-1", Filename: "packing-list.pdf", ContentType: "application/pdf",
						Category: files.CategoryGeneral},
					SizeDisplay:    "100 B",
					LinkedEventIDs: map[string]bool{},
				},
			},
		}},
	}
}

// htmlBetween returns the slice of html between the first occurrence of
// start and the following end marker — used to look at one file's own
// card rather than the whole page.
func htmlBetween(t *testing.T, html, start, end string) string {
	t.Helper()
	i := strings.Index(html, start)
	if i == -1 {
		t.Fatalf("marker %q not found in the rendered page", start)
	}
	rest := html[i:]
	if j := strings.Index(rest[len(start):], end); j != -1 {
		return rest[:len(start)+j]
	}
	return rest
}

func TestLinkedEventsOpenOnTheRightWindow(t *testing.T) {
	now := time.Now()
	out := renderPage(t, "files.html", windowedLibraryFixture(now))

	// Each card's link form is addressed by the file's own id, so the
	// card boundary is the next card's form action.
	photoCard := htmlBetween(t, out, `action="/files/photo-1/link"`, `action="/files/doc-1/link"`)
	docCard := htmlBetween(t, out, `action="/files/doc-1/link"`, `</form>`)

	if !strings.Contains(photoCard, "Events from the last 30 days") {
		t.Error("a photo's link list should say it is showing the last 30 days")
	}
	if !strings.Contains(docCard, "Events in the next 30 days") {
		t.Error("a document's link list should say it is showing the next 30 days")
	}

	// The photo's visible half is the hike; the campout is behind the
	// "show every other event" fold. Both are in the DOM either way, so
	// what's being checked is which side of that fold they land on.
	photoVisible := htmlBetween(t, photoCard, `<div class="max-h-40`, `<details`)
	if !strings.Contains(photoVisible, `value="e-hike"`) {
		t.Error("the photo's list should offer last weekend's hike up front")
	}
	if strings.Contains(photoVisible, `value="e-campout"`) {
		t.Error("the photo's list offers a future event up front; photos are taken at events that already happened")
	}
	if !strings.Contains(photoCard, `value="e-campout"`) {
		t.Error("the future event is missing from the photo's card entirely — it should be behind the fold, not gone")
	}

	docVisible := htmlBetween(t, docCard, `<div class="max-h-40`, `<details`)
	if !strings.Contains(docVisible, `value="e-campout"`) {
		t.Error("the document's list should offer next weekend's campout up front")
	}
	if strings.Contains(docVisible, `value="e-hike"`) {
		t.Error("the document's list offers a past event up front; a packing list is posted before the trip")
	}
}

// An existing link has to be visible without unfolding anything: the
// form replaces the whole set on save, so a link the leader can't see is
// one they can't tell they're keeping.
func TestAnExistingLinkIsAlwaysVisible(t *testing.T) {
	now := time.Now()
	data := windowedLibraryFixture(now)
	data.EventGroups[0].Files[0].LinkedEventIDs = map[string]bool{"e-oldcamp": true}

	out := renderPage(t, "files.html", data)
	photoCard := htmlBetween(t, out, `action="/files/photo-1/link"`, `action="/files/doc-1/link"`)
	visible := htmlBetween(t, photoCard, `<div class="max-h-40`, `<details`)

	if !strings.Contains(visible, `value="e-oldcamp"`) {
		t.Error("an event the photo is already linked to is hidden behind the fold")
	}
	if !strings.Contains(visible, `value="e-oldcamp" checked`) {
		t.Error("the existing link is not pre-checked")
	}
}

func TestBulkBarAndItsCheckboxes(t *testing.T) {
	out := renderPage(t, "files.html", windowedLibraryFixture(time.Now()))

	if !strings.Contains(out, `<form id="files-bulk" method="post" action="/files/bulk"`) {
		t.Fatal("the bulk action form is missing")
	}
	// The checkboxes live inside the file cards and join the form by id.
	// Nesting them in it instead would be invalid HTML, since each card
	// already contains its own rename/delete forms.
	for _, id := range []string{"photo-1", "doc-1"} {
		want := `<input type="checkbox" form="files-bulk" name="file_ids" value="` + id + `"`
		if !strings.Contains(out, want) {
			t.Errorf("no bulk checkbox for %s", id)
		}
	}
	for _, action := range []string{"public", "private", "link", "move", "unlink"} {
		if !strings.Contains(out, `name="action" value="`+action+`"`) {
			t.Errorf("the bulk bar offers no %q action", action)
		}
	}
	// The event dropdown groups by window so the likely events are first.
	for _, group := range []string{"Last 30 days", "Next 30 days", "Everything else"} {
		if !strings.Contains(out, `<optgroup label="`+group+`">`) {
			t.Errorf("the bulk event dropdown has no %q group", group)
		}
	}
}

// A member who can't manage files gets no bulk controls at all — the
// handler refuses them anyway, but offering buttons that 403 is its own
// kind of broken.
func TestNoBulkControlsWithoutPermission(t *testing.T) {
	data := windowedLibraryFixture(time.Now())
	data.CanManage = false
	out := renderPage(t, "files.html", data)

	if strings.Contains(out, `id="files-bulk"`) {
		t.Error("the bulk form renders for someone who can't manage files")
	}
	if strings.Contains(out, "data-bulk-file") {
		t.Error("bulk checkboxes render for someone who can't manage files")
	}
}

func TestBulkResultMessage(t *testing.T) {
	cases := []struct {
		action, count string
		want          string
	}{
		{"public", "3", "3 files are now public"},
		{"public", "1", "1 file are now public"},
		{"private", "2", "2 files are now members-only."},
		{"link", "0", "already linked"},
		{"link", "4", "4 files added to that event."},
		{"move", "4", "4 files moved to that event"},
		{"unlink", "0", "weren't linked"},
		{"unlink", "6", "Removed 6 event link(s)."},
		{"none", "", "Nothing was selected"},
		{"noevent", "", "Choose an event first"},
		{"", "", ""},
		{"nonsense", "1", ""},
	}
	for _, c := range cases {
		got := bulkResultMessage(c.action, c.count)
		if c.want == "" {
			if got != "" {
				t.Errorf("bulkResultMessage(%q, %q) = %q, want no message", c.action, c.count, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("bulkResultMessage(%q, %q) = %q, want it to contain %q", c.action, c.count, got, c.want)
		}
	}
}

// A count that didn't come from our own redirect shouldn't reach the
// page as anything but a number.
func TestBulkResultMessageIgnoresAJunkCount(t *testing.T) {
	got := bulkResultMessage("public", "<script>alert(1)</script>")
	if strings.Contains(got, "<script>") {
		t.Fatalf("the message carries the raw count back to the page: %q", got)
	}
	if !strings.Contains(got, "0 files") {
		t.Errorf("an unparseable count should read as zero, got %q", got)
	}
}
