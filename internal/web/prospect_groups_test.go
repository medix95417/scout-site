package web

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/prospect"
)

// The by-status view splits the list into one accordion per status, so
// a leader can open the one they are working through and leave the
// rest closed.

func testStatuses() []prospect.Label {
	return []prospect.Label{
		{Value: "new", Label: "New enquiry", SortOrder: 1},
		{Value: "contacted", Label: "Contacted", SortOrder: 2},
		{Value: "joined", Label: "Joined", Closed: true, SortOrder: 3},
	}
}

func row(id, status string) prospectRow {
	return prospectRow{Prospect: prospect.Prospect{ID: id, Status: status, ChildName: "Child " + id}}
}

func TestGroupByStatus(t *testing.T) {
	text := map[string]string{"new": "New enquiry", "contacted": "Contacted", "joined": "Joined"}
	groups := groupByStatus([]prospectRow{
		row("a", "contacted"), row("b", "new"), row("c", "contacted"),
	}, testStatuses(), text)

	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3 — one per status", len(groups))
	}
	// Workflow order, from the labels, not from what the rows contain.
	for i, want := range []string{"new", "contacted", "joined"} {
		if groups[i].Value != want {
			t.Errorf("group %d is %q, want %q", i, groups[i].Value, want)
		}
	}
	if len(groups[1].Prospects) != 2 {
		t.Errorf("Contacted holds %d, want 2", len(groups[1].Prospects))
	}
	// An empty status keeps its heading: "nobody is at Joined" is worth
	// seeing, and a group that vanishes makes the page jump about as a
	// leader works through it.
	if len(groups[2].Prospects) != 0 {
		t.Errorf("Joined should be empty, holds %d", len(groups[2].Prospects))
	}
}

// The first group with anything in it opens, so the page lands on work
// to do rather than on a row of closed headings.
func TestTheFirstNonEmptyGroupOpens(t *testing.T) {
	text := map[string]string{"new": "New enquiry", "contacted": "Contacted", "joined": "Joined"}

	groups := groupByStatus([]prospectRow{row("a", "contacted")}, testStatuses(), text)
	if groups[0].Open {
		t.Error("an empty first group opened")
	}
	if !groups[1].Open {
		t.Error("the first group with anything in it didn't open")
	}

	// Exactly one, always — two open accordions is not "show me only
	// these".
	open := 0
	for _, g := range groups {
		if g.Open {
			open++
		}
	}
	if open != 1 {
		t.Errorf("%d groups open, want 1", open)
	}

	// And nothing at all opens nothing, rather than panicking on an
	// empty list.
	empty := groupByStatus(nil, testStatuses(), text)
	for _, g := range empty {
		if g.Open {
			t.Error("a group opened with no enquiries anywhere")
		}
	}
}

// A prospect sitting on a status the live list no longer covers —
// retired, or gone — keeps a group of its own at the end. It is the
// row that most needs looking at, so dropping it is the one thing this
// must not do.
func TestAnUnknownStatusKeepsItsOwnGroup(t *testing.T) {
	text := map[string]string{"new": "New enquiry", "retired-one": "Left voicemail"}
	groups := groupByStatus([]prospectRow{
		row("a", "new"), row("b", "retired-one"),
	}, testStatuses(), text)

	if len(groups) != 4 {
		t.Fatalf("got %d groups, want 4 — the three statuses plus the unknown one", len(groups))
	}
	last := groups[3]
	if last.Value != "retired-one" {
		t.Fatalf("the last group is %q, want the unknown status", last.Value)
	}
	if last.Label != "Left voicemail" {
		t.Errorf("a retired status reads as %q, want its own name", last.Label)
	}
	if len(last.Prospects) != 1 {
		t.Errorf("the unknown status's group holds %d, want 1", len(last.Prospects))
	}

	// Every row is somewhere. Losing one would be losing a real family
	// off a page whose whole job is not to.
	total := 0
	for _, g := range groups {
		total += len(g.Prospects)
	}
	if total != 2 {
		t.Errorf("%d rows across the groups, want 2", total)
	}
}

// The grouped markup only appears in the by-status view; newest-first
// stays a flat chronological list, which is what it is for.
func TestOnlyTheByStatusViewIsGrouped(t *testing.T) {
	base := prospectsPageData{
		baseData:  testBase("Prospects"),
		Prospects: []prospectRow{row("a", "new")},
		Statuses:  prospect.DefaultStatuses(),
	}

	flat := base
	flat.Sort = "newest"
	out := renderPage(t, "admin-prospects.html", flat)
	if strings.Contains(out, `name="prospect-status"`) {
		t.Error("the newest-first view rendered status accordions")
	}
	if !strings.Contains(out, "Child a") {
		t.Error("the flat list lost its rows")
	}

	grouped := base
	grouped.Sort = "status"
	grouped.Groups = []prospectStatusGroup{
		{Value: "new", Label: "New enquiry", Prospects: base.Prospects, Open: true},
		{Value: "contacted", Label: "Contacted"},
	}
	out = renderPage(t, "admin-prospects.html", grouped)
	if !strings.Contains(out, `name="prospect-status"`) {
		t.Error("the by-status view isn't an exclusive accordion")
	}
	if !strings.Contains(out, "New enquiry") || !strings.Contains(out, "Contacted") {
		t.Error("a status heading went missing")
	}
	if !strings.Contains(out, "Child a") {
		t.Error("the grouped list lost its rows")
	}
	// The row markup is one partial shared by both views, so a form in
	// it must still carry the page's token.
	if !strings.Contains(out, `name="csrf_token"`) {
		t.Error("the shared row partial lost the CSRF token")
	}
}
