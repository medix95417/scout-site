package web

// Getting to the meeting.
//
// "Where do you meet" is the question a family asks before any other, and
// the homepage's answer was a paragraph of text someone had to retype
// into their phone. A unit can now record the address itself, and the
// homepage turns it into a panel that opens the visitor's own map app
// with directions already set — one tap, on the phone they are holding.
//
// A unit can also paste a map embed link (their map provider's
// Share → Embed), and the panel shows the map itself above that link.
// It stays optional, and off until someone pastes one, for a reason
// worth being explicit about: an embedded map is a third party watching
// every visit to this site's front page, including from children's
// families, whether or not anyone looks at the map. A unit that wants
// that trade can make it; a unit that does nothing makes no such request
// of its visitors, and the directions link still works because nothing
// is fetched until it is clicked.

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

// normalizeAddress flattens a leader-typed address into one line.
//
// The admin field is a text box, because people write an address the way
// they would on an envelope. A map service wants it comma-separated, so
// the line breaks become commas here rather than a rule the leader has
// to remember.
func normalizeAddress(address string) string {
	var parts []string
	for _, line := range strings.Split(strings.ReplaceAll(address, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(strings.Trim(strings.TrimSpace(line), ",")); line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, ", ")
}

// directionsURL builds the "get directions" link for an address.
//
// Google's cross-platform directions URL is used because it is the one
// that behaves everywhere: a phone hands it to whichever map app is
// installed, a desktop opens the web map, and it needs no API key or
// account. Nothing is requested until a visitor clicks it.
//
// Returns "" for a blank address, which is how the template decides
// whether there is a panel to render at all.
func directionsURL(address string) string {
	one := normalizeAddress(address)
	if one == "" {
		return ""
	}
	return "https://www.google.com/maps/dir/?api=1&destination=" + url.QueryEscape(one)
}

// mapsSearchURL is the same address as a plain map view rather than a
// route — what the map panel links to when a unit has given an address
// but the visitor may want to look around rather than set off.
func mapsSearchURL(address string) string {
	one := normalizeAddress(address)
	if one == "" {
		return ""
	}
	return "https://www.google.com/maps/search/?api=1&query=" + url.QueryEscape(one)
}

// allowedMapEmbeds is every map an embed link may come from: the exact
// origin and path prefix each provider's own "embed this map" dialog
// produces.
//
// An allowlist rather than a check that the URL is https and looks like a
// map, because this value ends up as the src of an iframe on the public
// homepage. Anything not on this list is refused, so a pasted link to
// somewhere else — by mistake, or by someone who has got at the admin
// page — cannot put an arbitrary page inside this site's front page. The
// same entries appear in the Content-Security-Policy (see internal/csp),
// so a browser refuses what this function would too — which is also why
// adding one here means adding it there, and why a test checks that the
// two lists agree.
var allowedMapEmbeds = []string{
	// Google Maps: Share → Embed a map.
	"https://www.google.com/maps/embed",
	// OpenStreetMap: Share → HTML. No account, no cookie, no tracking —
	// the one to recommend, and the help text does.
	//
	// Both spellings, because OpenStreetMap's share dialog has emitted
	// both: the older ".html" and the bare path it hands out now. Only
	// the second was listed here at first, so the link a unit copied
	// today was refused and the homepage showed no map and said nothing
	// about why. Neither is going to stop working, so both stay.
	"https://www.openstreetmap.org/export/embed",
	"https://www.openstreetmap.org/export/embed.html",
}

// iframeSrcPattern pulls the src out of a pasted <iframe> tag.
//
// Both providers' "embed this map" dialogs hand over a whole element —
// <iframe src="..." width=... ></iframe> — and that is what lands in a
// leader's clipboard. Asking someone to select the part between two
// quotation marks and nothing else is a demand the paste should not make,
// so a whole snippet is accepted and the src taken out of it. Nothing is
// trusted by having been extracted: the result goes through exactly the
// same allowlist a typed URL does.
var iframeSrcPattern = regexp.MustCompile(`(?is)<iframe\b[^>]*?\bsrc\s*=\s*["']([^"']+)["']`)

// mapEmbedCandidate turns what a leader pasted into the URL to check.
//
// Two shapes arrive: a bare URL, or the provider's whole <iframe> tag.
// Either way the attribute value is HTML — the src of a copied snippet
// spells its separators "&amp;" — so entities are decoded here rather
// than being carried into the query string, where "&amp;layer=mapnik"
// would become a parameter named "amp;layer".
func mapEmbedCandidate(raw string) string {
	raw = strings.TrimSpace(raw)
	if m := iframeSrcPattern.FindStringSubmatch(raw); m != nil {
		raw = m[1]
	}
	return strings.TrimSpace(html.UnescapeString(raw))
}

// safeMapEmbedURL returns the URL only if it is one of the allowed map
// embeds, and "" otherwise.
//
// Parsed rather than prefix-matched on the raw string: "https://www.google.com/maps/embed"
// is a prefix of "https://www.google.com/maps/embed.evil.example/..." only
// if you compare text, and the point of this function is that it doesn't.
func safeMapEmbedURL(raw string) string {
	raw = mapEmbedCandidate(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	for _, allowed := range allowedMapEmbeds {
		a, err := url.Parse(allowed)
		if err != nil {
			continue
		}
		if u.Host != a.Host {
			continue
		}
		// Exactly the embed endpoint, or something below it — never a
		// sibling path that merely starts with the same characters.
		if u.Path == a.Path || strings.HasPrefix(u.Path, a.Path+"/") {
			// Rebuilt from the parsed parts rather than echoed back, so
			// whatever reaches the page is a URL this function
			// understood — no fragment, no credentials, no stray
			// whitespace.
			clean := url.URL{Scheme: "https", Host: u.Host, Path: u.Path, RawQuery: u.RawQuery}
			return clean.String()
		}
	}
	return ""
}
