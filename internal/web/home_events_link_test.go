package web

import (
	"strings"
	"testing"
	"time"

	"github.com/47-yonkers/scout-site/internal/calendar"
)

// Each upcoming event on the homepage links to that event on the
// calendar page. The target is the id the calendar's own "Upcoming
// events" list puts on each entry, and both lists are drawn from the
// same query — so an event shown here always has somewhere to land.

func homePageWithEvents() homeFixture {
	data := homePage()
	data.Events = []calendar.Event{
		{ID: "11111111-1111-1111-1111-111111111111", Title: "Fall Campout", Location: "Harriman State Park", StartsAt: time.Now().Add(72 * time.Hour)},
		{ID: "22222222-2222-2222-2222-222222222222", Title: "Pack Meeting", StartsAt: time.Now().Add(120 * time.Hour)},
	}
	return data
}

func TestHomeEventsLinkToTheCalendar(t *testing.T) {
	out := renderPage(t, "home.html", homePageWithEvents())

	for _, id := range []string{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"} {
		if !strings.Contains(out, `href="/calendar#event-`+id+`"`) {
			t.Errorf("no link to event %s", id)
		}
	}
	// The title is inside the link, not beside it — the whole card is
	// the target, which is what makes this usable with a thumb.
	link := htmlBetween(t, out, `<a href="/calendar#event-11111111-1111-1111-1111-111111111111"`, "</a>")
	for _, want := range []string{"Fall Campout", "Harriman State Park"} {
		if !strings.Contains(link, want) {
			t.Errorf("%q is outside the link, so only part of the card is clickable", want)
		}
	}
}

// The anchor has to exist on the other end. A link to an id the
// calendar page never renders scrolls nowhere and looks broken, so the
// two are checked against each other rather than separately.
func TestTheCalendarCarriesTheIDsTheHomepageLinksTo(t *testing.T) {
	home := readTemplate(t, "home.html")
	cal := readTemplate(t, "calendar.html")

	if !strings.Contains(home, `href="/calendar#event-{{.ID}}"`) {
		t.Fatal("the homepage doesn't build its event links from the event id")
	}
	if !strings.Contains(cal, `id="event-{{.ID}}"`) {
		t.Error("the calendar page no longer gives each event that id — the homepage's links now scroll nowhere")
	}
}

// A homepage with no events must not render an empty link.
func TestHomeWithNoEventsLinksNowhere(t *testing.T) {
	out := renderPage(t, "home.html", homePage())
	if strings.Contains(out, "/calendar#event-") {
		t.Error("an event link rendered with no events to link to")
	}
	if !strings.Contains(out, "No public events scheduled right now") {
		t.Error("the empty-state message went missing")
	}
}
