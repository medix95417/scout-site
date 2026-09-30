package web

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/prospect"
)

// The bulk status move: what the page says it did, and the markup that
// lets a tick box on a row belong to a form it is not inside.

func TestBulkMoveNotice(t *testing.T) {
	for _, c := range []struct {
		name     string
		moved    prospect.BulkMove
		selected int
		want     string
	}{
		{
			name:     "the ordinary case",
			moved:    prospect.BulkMove{Moved: 4},
			selected: 4,
			want:     "Moved 4 enquiries to Visited a meeting.",
		},
		{
			name:     "one is singular",
			moved:    prospect.BulkMove{Moved: 1},
			selected: 1,
			want:     "Moved 1 enquiry to Visited a meeting.",
		},
		{
			// The case this message exists for: "moved 3" when four were
			// ticked reads as a partial failure unless the page says why.
			name:     "some were already there",
			moved:    prospect.BulkMove{Moved: 3, Already: 1},
			selected: 4,
			want:     "Moved 3 enquiries to Visited a meeting; 1 was already at Visited a meeting.",
		},
		{
			name:     "all of them were already there",
			moved:    prospect.BulkMove{Already: 2},
			selected: 2,
			want:     "Nothing moved; 2 of those were already at Visited a meeting.",
		},
		{
			// Not reachable from the page — it takes a hand-made request —
			// but a count that doesn't add up is worse than an odd
			// sentence, so the leftovers are accounted for too.
			name:     "an id that isn't this unit's",
			moved:    prospect.BulkMove{Moved: 1},
			selected: 3,
			want:     "Moved 1 enquiry to Visited a meeting; 2 were no longer in this list.",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := bulkMoveNotice(c.moved, c.selected, "Visited a meeting"); got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

// Every number the leader was shown has to be accounted for: moved plus
// already-there plus leftovers is what they ticked, and the sentence
// names each part that isn't zero.
func TestBulkMoveNoticeAccountsForEverySelection(t *testing.T) {
	for _, c := range []struct {
		moved    prospect.BulkMove
		selected int
	}{
		{prospect.BulkMove{Moved: 2, Already: 1}, 4},
		{prospect.BulkMove{Moved: 0, Already: 0}, 2},
		{prospect.BulkMove{Moved: 5}, 5},
	} {
		got := bulkMoveNotice(c.moved, c.selected, "Contacted")
		rest := c.selected - c.moved.Moved - c.moved.Already
		for _, part := range []struct {
			n    int
			what string
		}{
			{c.moved.Already, "already"},
			{rest, "no longer in this list"},
		} {
			if part.n > 0 && !strings.Contains(got, part.what) {
				t.Errorf("%+v of %d: %q doesn't mention the %d %s", c.moved, c.selected, got, part.n, part.what)
			}
			if part.n == 0 && strings.Contains(got, part.what) {
				t.Errorf("%+v of %d: %q mentions %s when there were none", c.moved, c.selected, got, part.what)
			}
		}
	}
}

// The tick boxes sit on rows that already contain forms, so each one
// names the bulk form by id instead. Losing that attribute leaves a page
// of controls that silently submit nothing, which is why it is asserted
// rather than left to the eye.
func TestTheBulkFormOwnsTheRowTickBoxes(t *testing.T) {
	data := prospectsPageData{
		baseData:    testBase("Prospects"),
		Prospects:   []prospectRow{row("a", "new"), row("b", "new")},
		Statuses:    prospect.DefaultStatuses(),
		CanBulkMove: true,
	}
	out := renderPage(t, "admin-prospects.html", data)

	if !strings.Contains(out, `action="/admin/prospects/bulk-status"`) {
		t.Error("the bulk-move form is missing")
	}
	if !strings.Contains(out, `id="prospect-bulk"`) {
		t.Fatal("the bulk-move form has no id, so no tick box can belong to it")
	}
	if got := strings.Count(out, `form="prospect-bulk" name="ids"`); got != 2 {
		t.Errorf("%d tick boxes name the bulk form, want one per row (2)", got)
	}
	// Its own CSRF token, like every other form on the page.
	if !strings.Contains(out, `name="csrf_token"`) {
		t.Error("the bulk-move form lost the CSRF token")
	}
	// Every status is offered as a destination.
	for _, l := range prospect.DefaultStatuses() {
		if !strings.Contains(out, `<option value="`+l.Value+`"`) {
			t.Errorf("%q isn't offered as somewhere to move to", l.Value)
		}
	}
	// And none of them is pre-selected: a destination that defaults to
	// the first status turns a stray click on "Move them" into a bulk
	// reset to "New enquiry".
	picker := out[strings.Index(out, `id="bulk-status"`):]
	picker = picker[:strings.Index(picker, "</select>")]
	if !strings.Contains(picker, `<option value="" selected disabled>`) {
		t.Error("the destination picker has no empty first choice, so it defaults to a real status")
	}
	if strings.Contains(strings.SplitN(picker, `<option value="" selected disabled>`, 2)[1], "selected") {
		t.Error("a real status is pre-selected in the destination picker")
	}
	if !strings.Contains(picker, "required") {
		t.Error("the destination picker isn't required, so the form submits with nothing chosen")
	}
}

// The server refuses an empty status too. `required` is the browser's
// courtesy, not the rule — anything posted without a destination has to
// be turned away rather than treated as a status nobody picked.
func TestABulkMoveNeedsAStatus(t *testing.T) {
	if got := bulkMoveNotice(prospect.BulkMove{}, 3, ""); !strings.Contains(got, "Nothing moved") {
		t.Errorf("an empty move reads as %q", got)
	}
}

// A unit with one status has nowhere to move anyone to, so neither the
// bar nor the boxes are drawn — a tick box whose form isn't on the page
// is a control that does nothing.
func TestNoBulkControlsWithNowhereToMoveTo(t *testing.T) {
	data := prospectsPageData{
		baseData:    testBase("Prospects"),
		Prospects:   []prospectRow{row("a", "new")},
		Statuses:    []prospect.Label{{Value: "new", Label: "New enquiry"}},
		CanBulkMove: false,
	}
	out := renderPage(t, "admin-prospects.html", data)

	if strings.Contains(out, `id="prospect-bulk"`) {
		t.Error("the bulk-move bar rendered for a unit with one status")
	}
	if strings.Contains(out, `name="ids"`) {
		t.Error("tick boxes rendered with no form to belong to")
	}
	// The rest of the page is untouched by that.
	if !strings.Contains(out, "Child a") {
		t.Error("the list lost its rows")
	}
}

// The bar carries the view the leader was looking at, the same as every
// other form on this page: a bulk move must not land them back on the
// default list having lost their filter.
func TestTheBulkFormKeepsTheCurrentView(t *testing.T) {
	data := prospectsPageData{
		baseData:    testBase("Prospects"),
		Prospects:   []prospectRow{row("a", "new")},
		Statuses:    prospect.DefaultStatuses(),
		CanBulkMove: true,
		ShowAll:     true,
		Filter:      "school-night",
		Sort:        "status",
	}
	out := renderPage(t, "admin-prospects.html", data)
	bar := out[strings.Index(out, `id="prospect-bulk"`):]
	bar = bar[:strings.Index(bar, "</form>")]

	for _, want := range []string{
		`name="all" value="1"`,
		`name="category_filter" value="school-night"`,
		`name="sort" value="status"`,
	} {
		if !strings.Contains(bar, want) {
			t.Errorf("the bulk-move form doesn't carry %s", want)
		}
	}
}

// The result of a move is a result, not a problem, and is not shown in
// the box that reports things going wrong.
func TestTheMoveNoticeIsNotAnError(t *testing.T) {
	data := prospectsPageData{
		baseData:    testBase("Prospects"),
		Prospects:   []prospectRow{row("a", "new")},
		Statuses:    prospect.DefaultStatuses(),
		CanBulkMove: true,
		Notice:      "Moved 4 enquiries to Contacted.",
	}
	out := renderPage(t, "admin-prospects.html", data)
	if !strings.Contains(out, "Moved 4 enquiries to Contacted.") {
		t.Fatal("the move notice wasn't shown")
	}
	// In the box that reports a result, not the one that reports a
	// problem. A green box saying a paste failed and a red one saying a
	// move worked are both worse than no message at all.
	green := strings.Index(out, "bg-green-50")
	if green < 0 {
		t.Fatal("no result box was rendered")
	}
	if at := strings.Index(out, "Moved 4 enquiries"); at < green || at-green > 200 {
		t.Error("the move notice isn't inside the result box")
	}

	data.Notice = ""
	data.AddError = "Something went wrong with that list."
	out = renderPage(t, "admin-prospects.html", data)
	if !strings.Contains(out, "bg-red-50") {
		t.Error("an add error no longer renders as an error")
	}
}
