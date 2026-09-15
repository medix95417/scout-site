package web

import (
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
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
	// The number is set apart from the word one way or the other — as the
	// numeral patches when this build carries them, as the CSS numeral
	// when it doesn't. Which one is the next test's business.
	heading := htmlBetween(t, out, "<h1", "</h1>")
	if !strings.Contains(heading, `<span class="unit-numeral">47</span>`) &&
		!strings.Contains(heading, `class="unit-numeral-images"`) {
		t.Error("the number isn't set apart from the word at all")
	}
	// Still one heading reading "Pack 47" to anyone not looking at CSS.
	if got := headingName(heading); got != "Pack 47" {
		t.Errorf("the heading reads %q, want %q", got, "Pack 47")
	}
}

// The pack's numerals are committed, so its hero should be drawing them
// rather than the CSS numeral it falls back to.
func TestPackHeroUsesTheCommittedNumerals(t *testing.T) {
	paths := templateNumeralImages("47", "pack")
	if paths == nil {
		t.Fatal("this build carries no pack numerals for 47; they were supposed to be committed")
	}

	data := homePage()
	data.Unit.UnitType = "pack"
	data.Unit.Name = "Pack 47"
	out := renderPage(t, "home.html", data)

	for _, p := range paths {
		if !strings.Contains(out, p) {
			t.Errorf("the pack hero doesn't render %s", p)
		}
	}
	// A pack draws its own artwork, never the troop's.
	if strings.Contains(out, "/static/numerals/troop/") {
		t.Error("the pack hero rendered the troop's numerals")
	}
	// With the patches in place there is no CSS numeral beside them.
	if strings.Contains(out, `<span class="unit-numeral">`) {
		t.Error("the heading has both the patches and the CSS numeral")
	}
	// The word still gets the display face — the artwork doesn't replace
	// the wordmark, it sits beside it.
	if !strings.Contains(out, `<span class="unit-wordmark">Pack</span>`) {
		t.Error("the pack's word lost its display font")
	}
}

// headingName is what the heading reads as, patches included: the
// numeral images stand in for the number they are labelled with, so a
// heading drawn as artwork can be compared with one set in type.
func headingName(heading string) string {
	heading = numeralImageSpan.ReplaceAllString(heading, "$1")
	return strings.Join(strings.Fields(stripTags(heading)), " ")
}

var numeralImageSpan = regexp.MustCompile(`(?s)<span class="unit-numeral-images"[^>]*aria-label="([^"]*)".*?</span>`)

// The troop's hero keeps its own look: the display font and the boxed
// numeral belong to the pack, and a troop takes neither even now that it
// has numeral artwork of its own.
func TestTroopHeroKeepsThePlainHeading(t *testing.T) {
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
	// The name still reads as "Troop 47", whether the number is set in
	// type or drawn as patches carrying that label.
	heading := htmlBetween(t, out, "<h1", "</h1>")
	if got := headingName(heading); got != "Troop 47" {
		t.Errorf("the heading reads %q, want %q", got, "Troop 47")
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

// --- Numeral images -------------------------------------------------------

// numeralFS is a stand-in for the embedded static assets.
func numeralFS(names ...string) fstest.MapFS {
	m := fstest.MapFS{}
	for _, n := range names {
		m[n] = &fstest.MapFile{Data: []byte("not really an image")}
	}
	return m
}

func TestNumeralImagesFor(t *testing.T) {
	// Both programs' artwork, in their own directories.
	assets := numeralFS(
		"numerals/troop/4.jpg", "numerals/troop/7.jpg",
		"numerals/pack/4.png", "numerals/pack/7.png", "numerals/pack/0.png",
	)

	t.Run("a troop gets the troop's numerals", func(t *testing.T) {
		got := numeralImagesFor("47", "troop", assets)
		want := []string{"/static/numerals/troop/4.jpg", "/static/numerals/troop/7.jpg"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("digit %d: got %q, want %q", i, got[i], want[i])
			}
		}
	})

	// The two programs' numerals look nothing alike — green on tan
	// against white on red — so one must never be served for the other.
	t.Run("a pack gets the pack's numerals", func(t *testing.T) {
		got := numeralImagesFor("47", "pack", assets)
		want := []string{"/static/numerals/pack/4.png", "/static/numerals/pack/7.png"}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("digit %d: got %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("one program's set doesn't stand in for the other's", func(t *testing.T) {
		troopOnly := numeralFS("numerals/troop/4.jpg", "numerals/troop/7.jpg")
		if got := numeralImagesFor("47", "pack", troopOnly); got != nil {
			t.Errorf("a pack was served the troop's patches: %v", got)
		}
		if got := numeralImagesFor("47", "troop", troopOnly); got == nil {
			t.Error("the troop's own patches weren't found")
		}
	})

	t.Run("a repeated digit is used twice", func(t *testing.T) {
		if got := numeralImagesFor("44", "troop", assets); len(got) != 2 {
			t.Errorf("got %v, want two paths", got)
		}
	})

	// The all-or-nothing rule: one digit missing falls the whole numeral
	// back, rather than mixing a picture and a letterform at different
	// sizes in the same heading.
	t.Run("one missing digit falls the whole numeral back", func(t *testing.T) {
		if got := numeralImagesFor("48", "troop", assets); got != nil {
			t.Errorf("got %v, want nil — there is no image for 8", got)
		}
	})

	t.Run("no images at all", func(t *testing.T) {
		if got := numeralImagesFor("47", "troop", numeralFS()); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("edge cases", func(t *testing.T) {
		if got := numeralImagesFor("", "troop", assets); got != nil {
			t.Errorf("an empty numeral gave %v", got)
		}
		if got := numeralImagesFor("47", "troop", nil); got != nil {
			t.Errorf("a nil file system gave %v", got)
		}
		if got := numeralImagesFor("47", "", assets); got != nil {
			t.Errorf("an empty unit type gave %v", got)
		}
		// The unit type names a directory, so only the two known values
		// may reach the file system.
		for _, bad := range []string{"..", "../pack", "troop/../pack", "TROOP", "crew"} {
			if got := numeralImagesFor("47", bad, assets); got != nil {
				t.Errorf("unit type %q gave %v, want nil", bad, got)
			}
		}
		// Anything that isn't a digit can't name a numeral image either.
		for _, bad := range []string{"4a", "../etc/passwd", "4/7", "4.7", "-4"} {
			if got := numeralImagesFor(bad, "troop", assets); got != nil {
				t.Errorf("numeralImagesFor(%q) gave %v, want nil", bad, got)
			}
		}
	})
}

// The troop's numerals are committed, so its hero should be drawing
// them — this is the half of the change that ships doing something.
func TestTroopHeroUsesTheCommittedNumerals(t *testing.T) {
	paths := templateNumeralImages("47", "troop")
	if paths == nil {
		t.Fatal("this build carries no troop numerals for 47; they were supposed to be committed")
	}

	data := homePage()
	data.Unit.UnitType = "troop"
	data.Unit.Name = "Troop 47"
	out := renderPage(t, "home.html", data)

	for _, p := range paths {
		if !strings.Contains(out, p) {
			t.Errorf("the troop hero doesn't render %s", p)
		}
	}
	// The word stays in the heading font — only a pack's gets the
	// display face.
	if strings.Contains(out, `<span class="unit-wordmark">`) {
		t.Error("the troop's word picked up the pack's display font")
	}
	if !strings.Contains(out, `aria-label="47"`) {
		t.Error("the numeral images aren't labelled, so the heading no longer reads as a number")
	}
}

// Until a complete set of images is committed, the heading has to look
// exactly as it does today — this ships dark, it doesn't ship broken.
func TestHeroFallsBackToTheCSSNumeralWithoutImages(t *testing.T) {
	// A number this build has no complete set for — not a number this
	// unit has, but the path a unit with an unstocked digit takes. Picked
	// by asking rather than assumed, so committing more artwork later
	// turns this into a skip rather than a mystery failure.
	number := ""
	for _, candidate := range []string{"8", "5", "3", "9", "6", "2", "1", "0"} {
		if templateNumeralImages(candidate, "pack") == nil {
			number = candidate
			break
		}
	}
	if number == "" {
		t.Skip("every digit has pack artwork now; there is no fallback left to exercise here")
	}

	data := homePage()
	data.Unit.UnitType = "pack"
	data.Unit.Name = "Pack " + number
	out := renderPage(t, "home.html", data)

	if !strings.Contains(out, `<span class="unit-numeral">`+number+`</span>`) {
		t.Error("without images the heading lost its CSS numeral")
	}
	if strings.Contains(out, `class="unit-numeral-images"`) {
		t.Error("the image markup rendered with no images to put in it")
	}
}
