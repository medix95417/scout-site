package web

import (
	"strconv"
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/emailtemplate"
	"github.com/47-yonkers/scout-site/internal/prospect"
)

// The "Emails to prospects" section keeps every campaign a unit ever
// sent, which after a couple of recruiting seasons makes it the longest
// thing on a page whose actual subject is the enquiries below it. So it
// collapses, and shows only the newest few — with the rest one link
// away, and the saved templates reachable regardless.

func campaignHistory(n int) []campaignRow {
	rows := make([]campaignRow, 0, n)
	for i := n; i >= 1; i-- {
		rows = append(rows, campaignRow{
			ID: "c" + strconv.Itoa(i), Subject: "Message " + strconv.Itoa(i), Status: "sent",
			Sent: true, SentOn: "3 Sep 2026", RecipientCount: 12,
		})
	}
	return rows
}

// prospectsPage builds the page through newProspectsView — the same
// function the handler calls — so these tests exercise the real slicing
// and the real links rather than a restatement of them. total is how
// many campaigns exist; showAllCampaigns is whether the leader asked to
// see all of them.
func prospectsPage(total int, showAllCampaigns bool, templates []emailtemplate.Template) prospectsPageData {
	return prospectsPageData{
		baseData:       testBase("Prospects"),
		prospectsView:  newProspectsView(campaignHistory(total), false, showAllCampaigns),
		Statuses:       prospect.Statuses,
		SavedTemplates: templates,
	}
}

func TestProspectsShowsOnlyTheNewestCampaigns(t *testing.T) {
	out := renderPage(t, "admin-prospects.html", prospectsPage(9, false, nil))

	// Nine exist, newest first, so 9 down to 5 are shown and 4 down to 1
	// are not. Checking both directions: a test that only counts rows
	// would pass on the wrong five.
	for _, want := range []string{"Message 9", "Message 8", "Message 7", "Message 6", "Message 5"} {
		if !strings.Contains(out, want) {
			t.Errorf("%s should be listed but is not", want)
		}
	}
	for _, notWant := range []string{"Message 4", "Message 3", "Message 2", "Message 1"} {
		if strings.Contains(out, notWant) {
			t.Errorf("%s should be behind the link but is on the page", notWant)
		}
	}
	if got := strings.Count(out, "/admin/prospect-campaigns/c"); got != 5 {
		t.Errorf("%d campaign links rendered, want 5", got)
	}
	// And the section still says how much history there is, so a
	// collapsed accordion is not a lie about what's in it.
	if !strings.Contains(out, "9 messages") {
		t.Errorf("the summary does not say how many messages exist:\n%s", section(out))
	}
}

func TestProspectsOffersTheRestBehindALink(t *testing.T) {
	out := renderPage(t, "admin-prospects.html", prospectsPage(9, false, nil))

	if !strings.Contains(out, "View 4 older messages") {
		t.Errorf("no link to the older messages:\n%s", section(out))
	}
	if !strings.Contains(out, `href="/admin/prospects?campaigns=all"`) {
		t.Errorf("the link does not ask for the full history:\n%s", section(out))
	}

	// Following it shows everything, and offers the way back.
	all := renderPage(t, "admin-prospects.html", prospectsPage(9, true, nil))
	for i := 1; i <= 9; i++ {
		if !strings.Contains(all, "Message "+strconv.Itoa(i)) {
			t.Errorf("Message %d missing from the full history", i)
		}
	}
	if !strings.Contains(all, "Show only the most recent") {
		t.Errorf("no way back to the short list:\n%s", section(all))
	}
}

// TestShortHistoryOffersNoLinks — five or fewer and there is nothing to
// hide, so neither link should appear.
func TestShortHistoryOffersNoLinks(t *testing.T) {
	out := renderPage(t, "admin-prospects.html", prospectsPage(5, false, nil))
	for _, notWant := range []string{"older message", "Show only the most recent"} {
		if strings.Contains(out, notWant) {
			t.Errorf("%q appears with only five campaigns:\n%s", notWant, section(out))
		}
	}
}

// TestSavedTemplatesSurviveTheTruncation is the specific thing that was
// asked for. Whatever hides the older messages must not hide the saved
// templates with them: that link is why a leader opens this section.
func TestSavedTemplatesSurviveTheTruncation(t *testing.T) {
	templates := []emailtemplate.Template{
		{ID: "t1", Name: "Open house", Subject: "Come and see us"},
		{ID: "t2", Name: "Sign-up night", Subject: "Join Pack 47"},
	}

	for _, tc := range []struct {
		name  string
		total int
		all   bool
	}{
		{"long history, truncated", 40, false},
		{"long history, fully shown", 40, true},
		{"short history", 2, false},
		{"no campaigns at all", 0, false},
	} {
		out := renderPage(t, "admin-prospects.html", prospectsPage(tc.total, tc.all, templates))
		if !strings.Contains(out, "Saved templates (2)") {
			t.Errorf("%s: the saved templates link is missing:\n%s", tc.name, section(out))
		}
		if !strings.Contains(out, "Open house") || !strings.Contains(out, "Sign-up night") {
			t.Errorf("%s: a saved template is missing", tc.name)
		}
		if !strings.Contains(out, "/admin/prospect-templates/t1/delete") {
			t.Errorf("%s: the template delete form is missing", tc.name)
		}
	}
}

// TestTheSectionIsAnAccordion. Two things have to hold: the history is
// inside a <details> so it can be put away, and "Write a new message"
// is NOT inside the <summary>, or clicking the button would toggle the
// panel instead of following the link.
func TestTheSectionIsAnAccordion(t *testing.T) {
	out := renderPage(t, "admin-prospects.html", prospectsPage(9, false, nil))
	sec := section(out)

	if !strings.Contains(sec, "<details") || !strings.Contains(sec, "<summary") {
		t.Fatalf("the section is not collapsible:\n%s", sec)
	}
	summary := sec[strings.Index(sec, "<summary"):]
	summary = summary[:strings.Index(summary, "</summary>")]
	if strings.Contains(summary, "prospect-campaigns/new") {
		t.Errorf("the new-message button is inside the summary, so clicking it toggles the panel:\n%s", summary)
	}
	if !strings.Contains(sec, "prospect-campaigns/new") {
		t.Error("the new-message button is missing from the section entirely")
	}

	// Closed by default, since shortening the page is the point...
	if strings.Contains(strings.SplitN(sec, "<summary", 2)[0], "open") {
		t.Errorf("the accordion starts open, which defeats collapsing it:\n%s", sec[:200])
	}
	// ...but open when the leader followed a link into it, or they would
	// click "view older" and arrive at a section that shut itself.
	opened := section(renderPage(t, "admin-prospects.html", prospectsPage(9, true, nil)))
	if !strings.Contains(strings.SplitN(opened, "<summary", 2)[0], "open") {
		t.Errorf("following the link lands on a closed accordion:\n%s", opened[:200])
	}
}

// TestTheTwoViewTogglesDoNotClobberEachOther. This page has an
// independent "show closed enquiries" filter; a campaign link that
// dropped it would silently re-hide a family somebody was looking at.
func TestTheTwoViewTogglesDoNotClobberEachOther(t *testing.T) {
	cases := []struct {
		showAll, allCampaigns bool
		want                  string
	}{
		{false, false, "/admin/prospects"},
		{true, false, "/admin/prospects?all=1"},
		{false, true, "/admin/prospects?campaigns=all"},
		{true, true, "/admin/prospects?all=1&campaigns=all"},
	}
	for _, c := range cases {
		if got := prospectsURL(c.showAll, c.allCampaigns); got != c.want {
			t.Errorf("prospectsURL(%v, %v) = %q, want %q", c.showAll, c.allCampaigns, got, c.want)
		}
	}

	// The view's own links, across both toggles. The interesting cell is
	// the last one: closing the campaign history must not also drop the
	// enquiry filter, and vice versa.
	for _, v := range []struct {
		showClosed, allCampaigns          bool
		wantToggleClosed, wantAllCampaign string
	}{
		{false, false, "/admin/prospects?all=1", "/admin/prospects?campaigns=all"},
		{true, false, "/admin/prospects", "/admin/prospects?all=1&campaigns=all"},
		{false, true, "/admin/prospects?all=1&campaigns=all", "/admin/prospects?campaigns=all"},
		{true, true, "/admin/prospects?campaigns=all", "/admin/prospects?all=1&campaigns=all"},
	} {
		got := newProspectsView(campaignHistory(9), v.showClosed, v.allCampaigns)
		if got.ToggleClosedURL != v.wantToggleClosed {
			t.Errorf("showClosed=%v allCampaigns=%v: ToggleClosedURL = %q, want %q",
				v.showClosed, v.allCampaigns, got.ToggleClosedURL, v.wantToggleClosed)
		}
		if got.AllCampaignsURL != v.wantAllCampaign {
			t.Errorf("showClosed=%v allCampaigns=%v: AllCampaignsURL = %q, want %q",
				v.showClosed, v.allCampaigns, got.AllCampaignsURL, v.wantAllCampaign)
		}
	}

	// And the links the page actually renders carry the other toggle
	// through — built by newProspectsView, not restated here.
	data := prospectsPageData{
		baseData:      testBase("Prospects"),
		prospectsView: newProspectsView(campaignHistory(9), true, false),
		Statuses:      prospect.Statuses,
		ShowAll:       true,
	}
	if data.ToggleClosedURL != "/admin/prospects" {
		t.Errorf("the closed-enquiries toggle = %q, want it to clear both", data.ToggleClosedURL)
	}
	out := renderPage(t, "admin-prospects.html", data)
	if !strings.Contains(out, `href="/admin/prospects?all=1&amp;campaigns=all"`) {
		t.Errorf("the older-messages link dropped the closed-enquiries filter:\n%s", section(out))
	}
}

// section narrows a rendered page to the emails block, so a failure
// prints that rather than the whole document.
func section(out string) string {
	i := strings.Index(out, "Emails to prospects")
	if i == -1 {
		return out
	}
	end := strings.Index(out[i:], "</section>")
	if end == -1 {
		end = len(out) - i
	}
	return out[i-400 : i+end]
}
