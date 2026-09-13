package web

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/content"
	"github.com/47-yonkers/scout-site/internal/files"
)

// The "why us" intro under the hero: a unit's own answer to the first
// question a family asks, in the first place they look.

// It has to be offered on Edit Homepage, or it can never be filled in,
// and it has to sit directly after the hero in that list so the order a
// leader edits in matches the order the page reads in.
func TestHomepageOffersTheWhyField(t *testing.T) {
	for _, unitType := range []string{"troop", "pack"} {
		defs := content.HomepageSections(unitType)

		var whyAt, heroImageAt = -1, -1
		var why content.SectionDef
		for i, def := range defs {
			switch def.Slug {
			case "home-why":
				whyAt, why = i, def
			case "home-hero-image":
				heroImageAt = i
			}
		}
		if whyAt == -1 {
			t.Fatalf("%s homepage has no \"why us\" field", unitType)
		}
		if whyAt != heroImageAt+1 {
			t.Errorf("%s: the why field is at %d, want directly after the hero image at %d", unitType, whyAt, heroImageAt)
		}
		// A plain text box: this is a paragraph someone writes, not a
		// link, an image, or a map.
		if why.Kind != "" {
			t.Errorf("%s: the why field should be a plain text box, got kind %q", unitType, why.Kind)
		}
		// The space is left open deliberately. A placeholder here would
		// put stock copy about Scouting in general on the live site
		// under a heading naming this unit specifically.
		if why.Placeholder != "" {
			t.Errorf("%s: the why field has default copy %q; it is meant to be left open for the unit to write", unitType, why.Placeholder)
		}
		if !strings.Contains(why.Help, "blank") {
			t.Errorf("%s: the help doesn't tell a leader what happens if they leave it empty: %q", unitType, why.Help)
		}
	}
}

func TestHomeShowsTheWhySectionOnlyWhenWritten(t *testing.T) {
	// Nothing written: no heading, no empty block, no trace. Matched on
	// the whole heading rather than a bare "Why ", which some other bit
	// of copy could legitimately contain one day.
	empty := homePage()
	blank := renderPage(t, "home.html", empty)
	if strings.Contains(blank, "Why "+empty.Unit.Name) {
		t.Error("an empty why section still put its heading on the page")
	}

	data := homePage()
	data.Why = "We are a family-run pack that camps every month, and every leader here is a parent."
	out := renderPage(t, "home.html", data)

	if !strings.Contains(out, data.Why) {
		t.Error("the text a leader wrote isn't on the page")
	}
	// The heading is built from the unit's name, so it reads "Why Pack
	// 47" without anyone typing that.
	if !strings.Contains(out, "Why "+data.Unit.Name) {
		t.Errorf("the heading isn't named for the unit; wanted %q", "Why "+data.Unit.Name)
	}
	// Near the top means near the top: above the news and events row.
	whyAt := strings.Index(out, "Why "+data.Unit.Name)
	newsAt := strings.Index(out, "Latest News")
	if whyAt == -1 || newsAt == -1 || whyAt > newsAt {
		t.Errorf("the why section is at %d and Latest News at %d; it belongs above the news", whyAt, newsAt)
	}
	// Line breaks a leader typed survive, the same as every other
	// free-text section on this page.
	if !strings.Contains(out, "whitespace-pre-line") {
		t.Error("the why text doesn't preserve the line breaks someone typed")
	}
}

// Whitespace is not content: a field holding only spaces or newlines
// must count as empty, or a stray keystroke leaves a heading over
// nothing on the public homepage.
func TestWhitespaceOnlyWhyIsTreatedAsEmpty(t *testing.T) {
	data := homePage()
	data.Why = ""
	if out := renderPage(t, "home.html", data); strings.Contains(out, "Why "+data.Unit.Name) {
		t.Error("an empty why section rendered a heading")
	}

	src, err := readSource("web.go")
	if err != nil {
		t.Fatalf("reading web.go: %v", err)
	}
	body, ok := functionBody(src, "Home")
	if !ok {
		t.Fatal("Home not found in web.go")
	}
	if !strings.Contains(body, `strings.TrimSpace(text["home-why"])`) {
		t.Error("the why text isn't trimmed, so a field of spaces would render a heading over nothing")
	}
}

// "Not set yet — the placeholder below is what's currently showing on
// the live site" is only true where there is a placeholder. Two text
// sections have none — the meeting address and this one — and for those
// the notice told a leader that copy they could not see was published.
func TestNoPlaceholderMeansNoClaimThatOneIsLive(t *testing.T) {
	rows := []homeAdminRow{
		{Slug: "home-why", Label: "\"Why us\" intro (optional)", Kind: "", Placeholder: "", Saved: false},
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
	}{Sections: rows}

	out := renderPage(t, "content-admin.html", data)
	if strings.Contains(out, "placeholder below is what's currently showing") {
		t.Error("a section with no placeholder claims one is live on the site")
	}

	// And it still says so where there genuinely is default copy.
	rows[0] = homeAdminRow{Slug: "home-meeting", Label: "Meeting info", Kind: "", Placeholder: "Contact us for our meeting time.", Saved: false}
	data.Sections = rows
	out = renderPage(t, "content-admin.html", data)
	if !strings.Contains(out, "placeholder below is what's currently showing") {
		t.Error("a section that does have default copy stopped saying so")
	}
}
