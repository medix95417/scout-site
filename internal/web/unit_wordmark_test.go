package web

import (
	"strings"
	"testing"
)

// A pack's hero name is split into its word and its unit numeral so the
// two can be styled apart. A troop's is not, and neither is a name with
// no number at the end of it.

func TestSplitUnitName(t *testing.T) {
	cases := []struct {
		name, in, label, numeral string
	}{
		{"the ordinary case", "Pack 47", "Pack", "47"},
		{"a troop splits the same way", "Troop 47", "Troop", "47"},
		{"a leading zero is kept", "Pack 0047", "Pack", "0047"},
		{"several words before the number", "Cub Scout Pack 47", "Cub Scout Pack", "47"},
		{"surrounding whitespace is ignored", "  Pack 47  ", "Pack", "47"},
		{"inner whitespace is collapsed", "Pack   47", "Pack", "47"},

		// Nothing to set apart: the whole name is the label.
		{"no number at all", "Lakeside Pack", "Lakeside Pack", ""},
		{"the number isn't last", "Pack 47 Yonkers", "Pack 47 Yonkers", ""},
		{"one word", "Pack", "Pack", ""},
		{"just a number", "47", "47", ""},
		{"empty", "", "", ""},
		{"only whitespace", "   ", "", ""},
		{"not digits, despite looking numeric", "Pack 47a", "Pack 47a", ""},
		{"a number with a suffix", "Pack 2nd", "Pack 2nd", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitUnitName(c.in)
			if got.Label != c.label || got.Numeral != c.numeral {
				t.Errorf("splitUnitName(%q) = {Label:%q Numeral:%q}, want {Label:%q Numeral:%q}",
					c.in, got.Label, got.Numeral, c.label, c.numeral)
			}
		})
	}
}

// Whatever the split, the two halves put back together have to be the
// name — this is a styling change, not a renaming one.
func TestSplitUnitNameLosesNothing(t *testing.T) {
	for _, in := range []string{"Pack 47", "Troop 47", "Cub Scout Pack 47", "Lakeside Pack", "Pack", "47", ""} {
		got := splitUnitName(in)
		rejoined := strings.TrimSpace(got.Label + " " + got.Numeral)
		want := strings.Join(strings.Fields(in), " ")
		if rejoined != want {
			t.Errorf("splitUnitName(%q) rejoins to %q, want %q", in, rejoined, want)
		}
	}
}

func TestPackHeroSplitsTheNameForStyling(t *testing.T) {
	data := homePage()
	data.Unit.UnitType = "pack"
	data.Unit.Name = "Pack 47"
	out := renderPage(t, "home.html", data)

	if !strings.Contains(out, `<span class="unit-wordmark">Pack</span>`) {
		t.Error("the word isn't marked up for the display font")
	}
	if !strings.Contains(out, `<span class="unit-numeral">47</span>`) {
		t.Error("the number isn't marked up as a unit numeral")
	}
	// Still one heading reading "Pack 47" to anyone not looking at CSS.
	heading := htmlBetween(t, out, "<h1", "</h1>")
	text := strings.Join(strings.Fields(stripTags(heading)), " ")
	if text != "Pack 47" {
		t.Errorf("the heading reads %q, want %q", text, "Pack 47")
	}
}

func TestTroopHeroIsUnchanged(t *testing.T) {
	data := homePage()
	data.Unit.UnitType = "troop"
	data.Unit.Name = "Troop 47"
	out := renderPage(t, "home.html", data)

	// Matched on the markup, not the bare class name: base.html defines
	// both classes in its stylesheet on every page, so looking for the
	// name alone finds the CSS and passes for the wrong reason.
	if strings.Contains(out, `<span class="unit-wordmark">`) || strings.Contains(out, `<span class="unit-numeral">`) {
		t.Error("a troop's hero picked up the pack's styling")
	}
	if !strings.Contains(out, "Troop 47") {
		t.Error("the troop's name went missing")
	}
}

// A pack whose name has no trailing number has nothing to set apart, and
// must not end up with an empty numeral box beside its name.
func TestPackWithoutANumberGetsThePlainHeading(t *testing.T) {
	data := homePage()
	data.Unit.UnitType = "pack"
	data.Unit.Name = "Lakeside Pack"
	out := renderPage(t, "home.html", data)

	if strings.Contains(out, `<span class="unit-numeral">`) {
		t.Error("a pack with no unit number still rendered a numeral")
	}
	if !strings.Contains(out, "Lakeside Pack") {
		t.Error("the pack's name went missing")
	}
}

// The display font is fetched only where it is used. A troop renders
// none of it, so it should not pay for the download.
func TestOnlyAPackLoadsTheDisplayFont(t *testing.T) {
	pack := homePage()
	pack.Unit.UnitType = "pack"
	if out := renderPage(t, "home.html", pack); !strings.Contains(out, "Luckiest+Guy") {
		t.Error("a pack doesn't load the display font its heading asks for")
	}

	troop := homePage()
	troop.Unit.UnitType = "troop"
	if out := renderPage(t, "home.html", troop); strings.Contains(out, "Luckiest+Guy") {
		t.Error("a troop downloads a font it never renders")
	}
}

// stripTags removes markup so a heading's text can be compared.
func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}
