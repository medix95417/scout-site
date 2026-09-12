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
	"net/url"
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
// same two entries appear in the Content-Security-Policy (see
// internal/csp), so a browser refuses what this function would too.
var allowedMapEmbeds = []string{
	// Google Maps: Share → Embed a map.
	"https://www.google.com/maps/embed",
	// OpenStreetMap: Share → HTML. No account, no cookie, no tracking —
	// the one to recommend, and the help text does.
	"https://www.openstreetmap.org/export/embed.html",
}

// safeMapEmbedURL returns the URL only if it is one of the allowed map
// embeds, and "" otherwise.
//
// Parsed rather than prefix-matched on the raw string: "https://www.google.com/maps/embed"
// is a prefix of "https://www.google.com/maps/embed.evil.example/..." only
// if you compare text, and the point of this function is that it doesn't.
func safeMapEmbedURL(raw string) string {
	raw = strings.TrimSpace(raw)
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
