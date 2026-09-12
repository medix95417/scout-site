package web

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/content"
	"github.com/47-yonkers/scout-site/internal/csp"
	"github.com/47-yonkers/scout-site/internal/files"
)

func TestNormalizeAddress(t *testing.T) {
	cases := []struct{ in, want string }{
		{"47 Oak Street\nYonkers, NY 10701", "47 Oak Street, Yonkers, NY 10701"},
		{"47 Oak Street\r\nYonkers, NY 10701", "47 Oak Street, Yonkers, NY 10701"},
		{"  47 Oak Street  \n\n  Yonkers, NY 10701  ", "47 Oak Street, Yonkers, NY 10701"},
		// A trailing comma on a line the leader typed shouldn't double up.
		{"47 Oak Street,\nYonkers, NY 10701", "47 Oak Street, Yonkers, NY 10701"},
		{"St. Mark's Parish Hall", "St. Mark's Parish Hall"},
		{"", ""},
		{"   \n  \n ", ""},
	}
	for _, c := range cases {
		if got := normalizeAddress(c.in); got != c.want {
			t.Errorf("normalizeAddress(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDirectionsURL(t *testing.T) {
	got := directionsURL("47 Oak Street\nYonkers, NY 10701")
	want := "https://www.google.com/maps/dir/?api=1&destination=47+Oak+Street%2C+Yonkers%2C+NY+10701"
	if got != want {
		t.Errorf("directionsURL = %q, want %q", got, want)
	}

	// No address, no panel — that's how the template decides.
	if got := directionsURL("  \n "); got != "" {
		t.Errorf("a blank address produced %q, want no link at all", got)
	}

	// An address is leader-typed, and it ends up inside a URL.
	tricky := directionsURL(`Hall & Annex, "rear" entrance <here>`)
	for _, raw := range []string{" ", `"`, "<", "&h"} {
		if strings.Contains(strings.TrimPrefix(tricky, "https://www.google.com/maps/dir/?api=1&destination="), raw) {
			t.Errorf("the address reached the query string unescaped: %q", tricky)
		}
	}
}

func TestSafeMapEmbedURL(t *testing.T) {
	good := []struct{ name, in, want string }{
		{
			"a Google Maps embed",
			"https://www.google.com/maps/embed?pb=!1m18!1m12!1m3!1d3021",
			"https://www.google.com/maps/embed?pb=!1m18!1m12!1m3!1d3021",
		},
		{
			// The bug this case exists for: OpenStreetMap's share dialog
			// hands out this path — no ".html" — and only the ".html"
			// spelling was allowed, so a link copied straight out of
			// OSM was refused and the homepage showed nothing.
			"an OpenStreetMap embed on the bare path OSM hands out now",
			"https://www.openstreetmap.org/export/embed?bbox=-73.88%2C40.90%2C-73.87%2C40.91&layer=mapnik",
			"https://www.openstreetmap.org/export/embed?bbox=-73.88%2C40.90%2C-73.87%2C40.91&layer=mapnik",
		},
		{
			// The other half of the same bug: what the dialog copies is
			// a whole element, entity-escaped, and that is what gets
			// pasted. Taking the src out of it is this site's job, not
			// the leader's.
			"the whole iframe snippet OpenStreetMap copies",
			`<iframe width="425" height="350" src="https://www.openstreetmap.org/export/embed?bbox=-73.88%2C40.90%2C-73.87%2C40.91&amp;layer=mapnik" style="border: 1px solid black"></iframe><br/><small><a href="https://www.openstreetmap.org/#map=18/40.91/-73.87">View Larger Map</a></small>`,
			"https://www.openstreetmap.org/export/embed?bbox=-73.88%2C40.90%2C-73.87%2C40.91&layer=mapnik",
		},
		{
			"the whole iframe snippet Google Maps copies",
			`<iframe src="https://www.google.com/maps/embed?pb=!1m18!1m12" width="600" height="450" style="border:0;" allowfullscreen="" loading="lazy"></iframe>`,
			"https://www.google.com/maps/embed?pb=!1m18!1m12",
		},
		{
			"an OpenStreetMap embed",
			"https://www.openstreetmap.org/export/embed.html?bbox=-73.9%2C40.9%2C-73.8%2C41.0&marker=40.95%2C-73.86",
			"https://www.openstreetmap.org/export/embed.html?bbox=-73.9%2C40.9%2C-73.8%2C41.0&marker=40.95%2C-73.86",
		},
		{
			"one pasted with stray whitespace",
			"  https://www.google.com/maps/embed?pb=xyz  ",
			"https://www.google.com/maps/embed?pb=xyz",
		},
		{
			"a fragment is dropped rather than passed through",
			"https://www.google.com/maps/embed?pb=xyz#anything",
			"https://www.google.com/maps/embed?pb=xyz",
		},
	}
	for _, c := range good {
		t.Run(c.name, func(t *testing.T) {
			if got := safeMapEmbedURL(c.in); got != c.want {
				t.Errorf("safeMapEmbedURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	// This value becomes the src of an iframe on the public homepage, so
	// anything that isn't exactly one of the two embed endpoints has to
	// come back empty.
	bad := []struct{ name, in string }{
		{"empty", ""},
		{"blank", "   "},
		{"plain http", "http://www.google.com/maps/embed?pb=xyz"},
		{"a lookalike host", "https://www.google.com.evil.example/maps/embed?pb=xyz"},
		{"a lookalike path", "https://www.google.com/maps/embedded-elsewhere?pb=xyz"},
		{"a path that merely starts the same", "https://www.google.com/maps/embed.evil/x"},
		{"another page on an allowed host", "https://www.google.com/search?q=x"},
		{"the OSM site rather than its embed", "https://www.openstreetmap.org/#map=12/40.9/-73.8"},
		{"a subdomain of an allowed host", "https://maps.google.com/maps/embed?pb=xyz"},
		{"javascript", "javascript:alert(1)"},
		{"a data URL", "data:text/html,<script>alert(1)</script>"},
		{"credentials smuggled into the authority", "https://www.google.com@evil.example/maps/embed"},
		{"a protocol-relative URL", "//www.google.com/maps/embed?pb=xyz"},
		// Unpicking a snippet is a convenience, not a way in: the src
		// that comes out of one faces the same allowlist.
		{"an iframe snippet pointed somewhere else", `<iframe src="https://evil.example/maps/embed?pb=x"></iframe>`},
		{"an iframe snippet with a javascript src", `<iframe src="javascript:alert(1)"></iframe>`},
		{"an iframe with no src at all", `<iframe width="425"></iframe>`},
		{"the short link Google Share offers first", "https://maps.app.goo.gl/abc123"},
		{"an ordinary Google Maps page URL", "https://www.google.com/maps/place/Yonkers,+NY/@40.93,-73.89,13z"},
	}
	for _, c := range bad {
		t.Run("refuses "+c.name, func(t *testing.T) {
			if got := safeMapEmbedURL(c.in); got != "" {
				t.Errorf("safeMapEmbedURL(%q) = %q, want it refused", c.in, got)
			}
		})
	}
}

// The admin page has to offer the two fields, or none of the above can
// ever be filled in.
func TestHomepageOffersTheAddressAndMapFields(t *testing.T) {
	for _, unitType := range []string{"troop", "pack"} {
		slugs := map[string]content.SectionDef{}
		for _, def := range content.HomepageSections(unitType) {
			slugs[def.Slug] = def
		}
		addr, ok := slugs["home-meeting-address"]
		if !ok {
			t.Fatalf("%s homepage has no meeting-address field", unitType)
		}
		if addr.Kind != "" {
			t.Errorf("%s: the address should be a plain text box, got kind %q", unitType, addr.Kind)
		}
		mapField, ok := slugs["home-meeting-map"]
		if !ok {
			t.Fatalf("%s homepage has no map field", unitType)
		}
		// "map", not "url": an <input type="url"> refuses the whole
		// <iframe> snippet both providers' embed dialogs copy, before
		// the form is even submitted.
		if mapField.Kind != "map" {
			t.Errorf("%s: the map field should be a map field, got kind %q", unitType, mapField.Kind)
		}
		// The help has to say the map is optional and what the cost of
		// using one is — it is the only place a leader is told.
		if !strings.Contains(mapField.Help, "OpenStreetMap") || !strings.Contains(mapField.Help, "every visit") {
			t.Errorf("%s: the map field's help doesn't explain the choice: %q", unitType, mapField.Help)
		}
	}
}

// --- The homepage itself ---------------------------------------------------

// homeFixture mirrors the anonymous struct Handlers.Home renders
// home.html with.
type homeFixture struct {
	baseData
	Events              []calendar.Event
	News                []homeNewsItem
	Activities          []homeActivity
	Hero                string
	HeroImageURL        string
	HeroSize            string
	ProgramItems        []string
	ProgramImageURL     string
	Meeting             string
	MeetingAddress      string
	MapEmbedURL         string
	DirectionsURL       string
	MapSearchURL        string
	Leadership          string
	HasLeaders          bool
	SocialURL           string
	StorefrontActive    bool
	StorefrontName      string
	StorefrontButtonURL string
	JoinFormOpen        bool
}

func homePage() homeFixture {
	return homeFixture{
		baseData:   testBase(""),
		Meeting:    "Tuesdays at 7pm during the school year.",
		Leadership: "Contact our Scoutmaster to learn more.",
	}
}

func TestLeadershipCardLinksToTheLeadersPage(t *testing.T) {
	data := homePage()
	data.HasLeaders = true
	card := leadershipCard(t, renderPage(t, "home.html", data))

	if !strings.Contains(card, `<a href="/leaders"`) {
		t.Fatalf("the Leadership & Contact card doesn't link to the leaders page: %s", card)
	}
	if !strings.Contains(card, "Meet our leaders") {
		t.Error("there's no link at the foot of the card, only the heading")
	}
	// The typed-in blurb is still the body of the card.
	if !strings.Contains(card, "Contact our Scoutmaster to learn more.") {
		t.Error("linking the card dropped the text a leader wrote")
	}
}

// A link to a page that says "no leaders listed yet" is worse than no
// link, so it isn't offered until there is something to read.
func TestNoLeadersMeansNoLink(t *testing.T) {
	// Scoped to the card: the site nav links to /leaders for everyone,
	// and that is a separate decision from what this card offers.
	card := leadershipCard(t, renderPage(t, "home.html", homePage()))

	if strings.Contains(card, `href="/leaders"`) {
		t.Errorf("the card links to an empty leaders page: %s", card)
	}
	if !strings.Contains(card, "Leadership &amp; Contact") {
		t.Error("the card's heading went missing along with the link")
	}
	if !strings.Contains(card, "Contact our Scoutmaster to learn more.") {
		t.Error("the card lost the text a leader wrote")
	}
}

// leadershipCard cuts the Leadership & Contact section out of a rendered
// homepage.
func leadershipCard(t *testing.T, html string) string {
	t.Helper()
	return htmlBetween(t, html, `<section class="rounded-xl border border-gray-200 bg-white p-5 flex flex-col">`, "</section>")
}

func TestMeetingCardShowsDirectionsOnlyWithAnAddress(t *testing.T) {
	plain := renderPage(t, "home.html", homePage())
	if strings.Contains(plain, "Get directions") {
		t.Error("a unit that hasn't given an address is offered directions to nowhere")
	}
	if strings.Contains(plain, "<iframe") {
		t.Error("a map was embedded with no address and no map link")
	}

	data := homePage()
	data.MeetingAddress = "47 Oak Street, Yonkers, NY 10701"
	data.DirectionsURL = directionsURL(data.MeetingAddress)
	data.MapSearchURL = mapsSearchURL(data.MeetingAddress)
	withAddress := renderPage(t, "home.html", data)

	if !strings.Contains(withAddress, "Get directions") {
		t.Fatal("an address produced no directions panel")
	}
	if !strings.Contains(withAddress, "47 Oak Street, Yonkers, NY 10701") {
		t.Error("the address itself isn't shown, so nobody can read it without clicking")
	}
	// The href is the directions endpoint with this address in it.
	// Asserted loosely on the encoding, since html/template escapes a
	// query string's "+" and "&" its own way and that is its business.
	href := htmlBetween(t, withAddress, `href="https://www.google.com/maps/dir/`, `"`)
	if !strings.Contains(href, "api=1") || !strings.Contains(href, "destination=47") ||
		!strings.Contains(href, "Oak") || !strings.Contains(href, "Yonkers") {
		t.Errorf("the directions link isn't pointed at the address: %q", href)
	}
	if !strings.Contains(withAddress, `rel="noopener noreferrer"`) {
		t.Error("the outbound link doesn't carry noopener/noreferrer")
	}
	// Still no third party on the page itself until a unit opts in.
	if strings.Contains(withAddress, "<iframe") {
		t.Error("an address alone embedded a map; that has to stay opt-in")
	}
}

func TestMeetingCardEmbedsTheMapWhenOneIsGiven(t *testing.T) {
	data := homePage()
	data.MeetingAddress = "47 Oak Street, Yonkers, NY 10701"
	data.DirectionsURL = directionsURL(data.MeetingAddress)
	data.MapEmbedURL = safeMapEmbedURL("https://www.openstreetmap.org/export/embed.html?bbox=1%2C2%2C3%2C4")
	out := renderPage(t, "home.html", data)

	if !strings.Contains(out, `src="https://www.openstreetmap.org/export/embed.html?bbox=1%2c2%2c3%2c4"`) &&
		!strings.Contains(out, `src="https://www.openstreetmap.org/export/embed.html?bbox=1%2C2%2C3%2C4"`) {
		t.Errorf("the map wasn't embedded; rendered: %s", htmlBetween(t, out, "Meeting Info", "Leadership"))
	}
	if !strings.Contains(out, `loading="lazy"`) {
		t.Error("the map isn't lazy, so it costs every visitor a request before they scroll to it")
	}
	// The map is the picture; the directions link is still what gets you
	// there, and it has to be present alongside.
	if !strings.Contains(out, "Get directions") {
		t.Error("the map replaced the directions link instead of sitting above it")
	}
}

// A refused map link must leave no trace on the page — the handler
// passes safeMapEmbedURL's answer, and that answer is "".
func TestARefusedMapLinkRendersNothing(t *testing.T) {
	data := homePage()
	data.MeetingAddress = "47 Oak Street"
	data.DirectionsURL = directionsURL(data.MeetingAddress)
	data.MapEmbedURL = safeMapEmbedURL("https://evil.example/maps/embed?pb=x")
	out := renderPage(t, "home.html", data)

	if strings.Contains(out, "evil.example") {
		t.Error("a refused map URL still reached the page")
	}
	if strings.Contains(out, "<iframe") {
		t.Error("an iframe was rendered for a refused map URL")
	}
	if !strings.Contains(out, "Get directions") {
		t.Error("refusing the map also lost the directions link")
	}
}

// A map with no address used to render nothing at all: the whole panel
// was gated on the address, so a unit that pasted an embed link and
// stopped there got an empty homepage and no hint why.
func TestMeetingCardShowsAMapWithoutAnAddress(t *testing.T) {
	data := homePage()
	data.MapEmbedURL = safeMapEmbedURL("https://www.openstreetmap.org/export/embed?bbox=1%2C2%2C3%2C4")
	if data.MapEmbedURL == "" {
		t.Fatal("the fixture's own map URL was refused")
	}
	out := renderPage(t, "home.html", data)

	if !strings.Contains(out, "<iframe") {
		t.Error("a map was given but nothing was embedded")
	}
	if !strings.Contains(out, "openstreetmap.org/export/embed") {
		t.Error("the map URL didn't reach the page")
	}
	// No address means no directions to offer, and no empty link where
	// the address would have been.
	if strings.Contains(out, "Get directions") {
		t.Error("directions were offered to an address nobody gave")
	}
	if strings.Contains(out, "Opens in your phone") {
		t.Error("the directions footnote showed with no directions link above it")
	}
}

// Whatever a leader pastes is turned into the stored URL — or refused —
// at the moment they can still be told, rather than silently becoming an
// empty space on the homepage. The check is the same function the
// homepage uses, so the two can't drift apart.
func TestSavingAMapSectionValidatesThePaste(t *testing.T) {
	src, err := readSource("web.go")
	if err != nil {
		t.Fatalf("reading web.go: %v", err)
	}
	body, ok := functionBody(src, "HomeContentSave")
	if !ok {
		t.Fatal("HomeContentSave not found in web.go")
	}
	if !strings.Contains(body, "safeMapEmbedURL(") {
		t.Error("HomeContentSave doesn't run a map paste through safeMapEmbedURL, so a bad one saves silently")
	}
	if !strings.Contains(body, "map=rejected") {
		t.Error("a refused map paste doesn't send the leader anything to read")
	}
}

// The admin page has to actually render that refusal, and has to take
// the paste in a field a whole <iframe> snippet fits in — an
// <input type="url"> refuses one before the form is even submitted.
func TestMapSectionTakesASnippetAndReportsARefusal(t *testing.T) {
	var mapDef content.SectionDef
	for _, def := range content.HomepageSections("troop") {
		if def.Slug == "home-meeting-map" {
			mapDef = def
		}
	}
	if mapDef.Kind != "map" {
		t.Fatalf("the map section's Kind is %q, want \"map\"", mapDef.Kind)
	}

	data := struct {
		baseData
		Sections              []homeAdminRow
		HeroSections          []homeAdminRow
		MapRejected           bool
		PublicImageGroups     []files.EventFileGroup
		PublicImagesUngrouped []files.File
		PublicMediaGroups     []files.EventFileGroup
		PublicMediaUngrouped  []files.File
	}{
		Sections:    []homeAdminRow{{Slug: mapDef.Slug, Label: mapDef.Label, Help: mapDef.Help, Kind: mapDef.Kind}},
		MapRejected: true,
	}
	out := renderPage(t, "content-admin.html", data)

	if !strings.Contains(out, "<textarea") {
		t.Error("the map field isn't a textarea, so a pasted <iframe> snippet can't be submitted")
	}
	if strings.Contains(htmlBetween(t, out, "home-meeting-map", "</section>"), `type="url"`) {
		t.Error("the map field is still a url input, which rejects the snippet the provider copies")
	}
	if !strings.Contains(out, "wasn't a map embed") {
		t.Error("a refused paste produces no message on the page")
	}
	if !strings.Contains(out, "COPY HTML") {
		t.Error("the refusal doesn't say where to get a link that would work")
	}
}

// The allowlist and the Content-Security-Policy have to name the same
// endpoints. They are two lists in two packages, and a map that passes
// safeMapEmbedURL but isn't in frame-src is a blank box in the browser
// with a console error nobody is looking at — which is the shape of the
// bug that sent a leader here in the first place.
func TestEveryAllowedMapEmbedIsAlsoInTheCSP(t *testing.T) {
	policy := csp.Policy("nonce")
	for _, allowed := range allowedMapEmbeds {
		if !strings.Contains(policy, allowed+" ") && !strings.Contains(policy, allowed+";") {
			t.Errorf("%s is accepted by safeMapEmbedURL but missing from frame-src, so the browser will refuse it", allowed)
		}
	}
}
