package web

import (
	"io/fs"
	"strings"
	"unicode"
)

// The homepage hero's unit name, split so the two halves can be styled
// apart: "Pack 47" is a word and a unit numeral, and a Cub Scout pack
// wants them to look like it — the numeral set as the red-on-white
// numeral patch that goes on the uniform sleeve, the word in something
// with more personality than the site's body face.
//
// Split here rather than in the template because "the last word, if it
// is a number" is a rule with edge cases (a unit with no number, a name
// ending in a word), and a template is the wrong place to get those
// right.

// unitNameParts is a unit's name divided into the part that is words and
// the part that is its number.
type unitNameParts struct {
	Label   string // "Pack", or the whole name when it has no trailing number
	Numeral string // "47", or "" when the name doesn't end in one
}

// splitUnitName divides a unit name at its trailing number.
//
// Only an actual trailing run of digits counts: "Pack 47" splits, and
// "Pack 47 Yonkers" or "Lakeside Pack" do not, because there is no
// numeral at the end to set apart. A name that doesn't split comes back
// whole in Label, which is what the template falls back to rendering.
func splitUnitName(name string) unitNameParts {
	name = strings.TrimSpace(name)
	fields := strings.Fields(name)
	if len(fields) < 2 {
		return unitNameParts{Label: name}
	}

	last := fields[len(fields)-1]
	for _, r := range last {
		if !unicode.IsDigit(r) {
			return unitNameParts{Label: name}
		}
	}
	return unitNameParts{
		Label:   strings.Join(fields[:len(fields)-1], " "),
		Numeral: last,
	}
}

// numeralImageExts are the file types a numeral image may be supplied
// as, tried in this order for each digit.
var numeralImageExts = []string{".jpg", ".png", ".jpeg", ".gif", ".svg"}

// numeralImagesFor returns the static paths for each digit of a unit
// numeral — the real numeral patches, committed under static/numerals —
// or nil when this build doesn't carry a complete set for this kind of
// unit.
//
// Kept per unit type, because the two programs' numerals are different
// objects: Scouts BSA numerals are green on tan, Cub Scout ones white
// on red. One shared set would put one program's patch on the other's
// homepage.
//
// All or nothing on purpose. A unit whose number is 47 with an image for
// 4 and none for 7 would otherwise get one digit as a picture and the
// other as text, at different sizes and weights, which looks broken in a
// way that "no images at all" does not. Missing one digit falls the
// whole numeral back.
//
// Self-hosted rather than linked from wherever the artwork came from:
// clipart sites tend to serve http://, and a browser refuses mixed
// content on an https:// page, so hotlinking means numerals that never
// appear at all. img-src in the Content-Security-Policy refuses them a
// second time. See static/numerals/README.md.
func numeralImagesFor(numeral, unitType string, assets fs.FS) []string {
	if numeral == "" || unitType == "" || assets == nil {
		return nil
	}
	// The unit type names a directory, so it may only ever be one of the
	// two known values — never whatever happened to be in the database.
	if unitType != "troop" && unitType != "pack" {
		return nil
	}

	paths := make([]string, 0, len(numeral))
	for _, digit := range numeral {
		if !unicode.IsDigit(digit) {
			return nil
		}
		found := ""
		for _, ext := range numeralImageExts {
			name := "numerals/" + unitType + "/" + string(digit) + ext
			if f, err := assets.Open(name); err == nil {
				f.Close()
				found = "/static/" + name
				break
			}
		}
		if found == "" {
			return nil
		}
		paths = append(paths, found)
	}
	return paths
}
