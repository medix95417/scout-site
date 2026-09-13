package web

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/leaders"
)

// The Leadership & Contact card introduces the unit's first few leaders
// rather than only offering a link to them.

func TestHomeLeaderBioIsShortenedNotDropped(t *testing.T) {
	short := "Eagle Scout, 1998. Runs the summer camp trip."
	if got := homeLeaderBio(short); got != short {
		t.Errorf("a bio that already fits was changed: %q", got)
	}

	long := strings.Repeat("Scouting since forever. ", 40)
	got := homeLeaderBio(long)
	if len(got) > homeLeaderBioLength+len("…") {
		t.Errorf("a long bio came back %d chars, want at most %d", len(got), homeLeaderBioLength+len("…"))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a shortened bio doesn't say it was shortened: %q", got)
	}
	if !strings.HasPrefix(got, "Scouting since forever.") {
		t.Errorf("the shortened bio isn't the start of the real one: %q", got)
	}

	// A multi-line bio becomes one flowing line here — the card gives
	// each leader a few lines, not a formatted block.
	if got := homeLeaderBio("Line one.\n\nLine two."); got != "Line one. Line two." {
		t.Errorf("newlines survived into the card: %q", got)
	}

	if got := homeLeaderBio(""); got != "" {
		t.Errorf("an empty bio produced %q", got)
	}
}

func TestInitial(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Sam Kowalski", "S"},
		{"alex", "A"},
		{"  Robin", "R"},
		{"Ólafur Eiríksson", "Ó"}, // one rune, not the first byte of two
		{"7th Grade Helper", "7"},
		{"", ""},
		{"   ", ""},
		{"!!!", ""},
	}
	for _, c := range cases {
		if got := initial(c.in); got != c.want {
			t.Errorf("initial(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHomeCardShowsTheFeaturedLeaders(t *testing.T) {
	data := homePage()
	data.HasLeaders = true
	data.Leaders = []homeLeader{
		{Name: "Sam Kowalski", RoleTitle: "Scoutmaster", Bio: "Eagle Scout, 1998.", PhotoURL: "/files/abc/download", PhotoFocus: leaders.PhotoFocusTop},
		{Name: "Robin Vega", RoleTitle: "Assistant Scoutmaster", Bio: "Runs the summer camp trip."},
		{Name: "Dana Ruiz", RoleTitle: "Committee Chair"},
	}
	out := renderPage(t, "home.html", data)

	for _, want := range []string{
		"Sam Kowalski", "Scoutmaster", "Eagle Scout, 1998.",
		"Robin Vega", "Runs the summer camp trip.",
		"Dana Ruiz", "Committee Chair",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the card doesn't show %q", want)
		}
	}

	// A photo goes through thumbURL rather than costing a visitor the
	// full-size original for a 48px circle, and keeps its crop.
	if !strings.Contains(out, `src="/files/abc/thumb"`) {
		t.Error("a leader photo wasn't rendered at thumbnail size")
	}
	if !strings.Contains(out, "object-top") {
		t.Error("the leader photo lost its focus crop")
	}
	// No photo means an initial, not an empty circle.
	if !strings.Contains(out, ">R</span>") {
		t.Error("a leader with no photo got no initial standing in for one")
	}
	// The link to the rest is still there — the card is a summary.
	if !strings.Contains(out, "Meet our leaders") {
		t.Error("the card stopped linking through to /leaders")
	}
}

// A unit that has published nothing gets the card exactly as it was: the
// typed paragraph, no empty list and no link to a page saying there are
// no leaders.
func TestHomeCardWithoutLeadersIsUnchanged(t *testing.T) {
	data := homePage()
	data.Leadership = "Contact our Scoutmaster to learn more."
	out := renderPage(t, "home.html", data)

	if !strings.Contains(out, "Contact our Scoutmaster to learn more.") {
		t.Error("the leader-typed paragraph went missing")
	}
	if strings.Contains(out, "Meet our leaders") {
		t.Error("a link to /leaders was offered with no leaders on it")
	}
	// "h-12 w-12" is the leader avatar's own size, and nothing else on
	// this page uses it — unlike "rounded-full", which is also the hero's
	// Follow-us pill and the members-only badge.
	if strings.Contains(out, "h-12 w-12") {
		t.Error("an empty leader list still rendered its markup")
	}
}
