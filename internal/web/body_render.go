package web

// Rendering a news post's body: plain text in, HTML out, with two
// additions a leader would expect and nothing else.
//
// A post is typed into a plain textarea and stored as plain text. Until
// now it was shown through {{.Body}}, which html/template escapes
// wholesale — safe, and also the reason a pasted YouTube link came out
// as inert text a visitor had to copy by hand. This file is the one
// place in the codebase that turns user-typed content into
// template.HTML, and it earns that by constructing every byte of markup
// itself: the text is escaped segment by segment, the only tags that
// appear are the <a> and <iframe> built here, and the only attacker-
// influenced values that reach an attribute are a URL the regex already
// limited to http(s) (escaped again on the way in) and a YouTube video
// id that must be exactly eleven characters from [A-Za-z0-9_-].
//
// Two rules, chosen to match what people are used to from chat and
// forum software:
//
//   - Any http(s) URL in the text becomes a link, opening in a new tab.
//   - A YouTube URL that is a line of its own becomes an embedded
//     player, with a "Watch on YouTube" link under it. The same URL in
//     the middle of a sentence is just a link — a leader who writes
//     "see https://youtu.be/... for the route" wants a link there, not
//     a video splitting the paragraph.
//
// The player is loaded from youtube-nocookie.com, YouTube's privacy-
// enhanced host, which sets no tracking cookie until the visitor presses
// play. Some of this site's readers are minors, and a news page should
// not be the thing that starts a profile on them. internal/csp allows
// frames from that one host and nothing else.

import (
	"html"
	"html/template"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// urlPattern is deliberately narrow: only http and https, and nothing
// that could close an attribute or a tag. A URL a person pasted ends at
// whitespace; the characters excluded here are ones no real link
// contains unescaped and every one of which would matter inside href="".
var urlPattern = regexp.MustCompile("(?i)https?://[^\\s<>\"'`]+")

// youtubeIDPattern is the exact shape of a YouTube video id. Being
// strict about it is what makes it safe to place in an iframe src
// without further escaping.
var youtubeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// renderPostBody turns a plain-text post body into HTML. Line breaks
// survive as newlines for a container styled whitespace-pre-line, the
// same way the escaped body was shown before.
func renderPostBody(body string) template.HTML {
	if body == "" {
		return ""
	}
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if id, start, ok := youtubeVideoID(trimmed); ok && isBareURL(trimmed) {
			out = append(out, youtubeEmbed(id, start, trimmed))
			continue
		}
		out = append(out, linkify(line))
	}
	return template.HTML(strings.Join(out, "\n")) //nolint:gosec // every byte is escaped or constructed here — see the file comment
}

// linkify escapes a line of text and wraps each URL in it as a link.
func linkify(line string) string {
	var sb strings.Builder
	last := 0
	for _, loc := range urlPattern.FindAllStringIndex(line, -1) {
		rawURL, trailing := trimTrailingPunctuation(line[loc[0]:loc[1]])
		sb.WriteString(html.EscapeString(line[last:loc[0]]))
		sb.WriteString(anchor(rawURL, rawURL))
		sb.WriteString(html.EscapeString(trailing))
		last = loc[1]
	}
	sb.WriteString(html.EscapeString(line[last:]))
	return sb.String()
}

// anchor builds the one <a> this file emits. rel="noopener" is what
// keeps a new tab from reaching back to this page; "noreferrer" keeps
// the members-only URL a reader came from out of the other site's logs.
func anchor(href, text string) string {
	return `<a href="` + html.EscapeString(href) + `" target="_blank" rel="noopener noreferrer" class="underline break-all" style="color: var(--unit-color);">` +
		html.EscapeString(text) + `</a>`
}

// trimTrailingPunctuation separates the punctuation a sentence puts after
// a URL from the URL itself: "see https://example.org." should link to
// example.org, not to "example.org.". A closing parenthesis is kept when
// the URL opened one (Wikipedia-style links) and trimmed otherwise.
func trimTrailingPunctuation(u string) (cleaned, trailing string) {
	cleaned = u
	for cleaned != "" {
		last := cleaned[len(cleaned)-1]
		switch {
		case strings.IndexByte(".,;:!?", last) >= 0:
		case last == ')' && !strings.Contains(cleaned, "("):
		case last == '\'' || last == '"':
		default:
			return cleaned, u[len(cleaned):]
		}
		cleaned = cleaned[:len(cleaned)-1]
	}
	return cleaned, u[len(cleaned):]
}

// isBareURL reports whether a trimmed line is exactly one URL and
// nothing else — the condition for turning it into an embed.
func isBareURL(trimmed string) bool {
	if trimmed == "" || strings.ContainsAny(trimmed, " \t") {
		return false
	}
	cleaned, _ := trimTrailingPunctuation(trimmed)
	return urlPattern.FindString(trimmed) == trimmed || urlPattern.FindString(trimmed) == cleaned
}

// youtubeVideoID recognises the address forms YouTube hands out and
// returns the video id, plus a start offset in seconds when the link
// carried one. Anything that is not unambiguously a single YouTube video
// is not one: a channel, a playlist page, a search, or an id of the
// wrong shape all return ok=false and fall back to a plain link.
func youtubeVideoID(rawURL string) (id string, start int, ok bool) {
	cleaned, _ := trimTrailingPunctuation(rawURL)
	u, err := url.Parse(cleaned)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", 0, false
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")

	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch host {
	case "youtu.be":
		if len(segments) >= 1 {
			id = segments[0]
		}
	case "youtube.com", "youtube-nocookie.com":
		switch {
		case len(segments) >= 1 && segments[0] == "watch":
			id = u.Query().Get("v")
		case len(segments) >= 2 && (segments[0] == "shorts" || segments[0] == "embed" || segments[0] == "live" || segments[0] == "v"):
			id = segments[1]
		}
	default:
		return "", 0, false
	}
	if !youtubeIDPattern.MatchString(id) {
		return "", 0, false
	}
	if t := u.Query().Get("t"); t != "" {
		start = parseYouTubeOffset(t)
	} else if t := u.Query().Get("start"); t != "" {
		start = parseYouTubeOffset(t)
	}
	return id, start, true
}

// parseYouTubeOffset reads the "t=" forms YouTube itself produces:
// "90", "90s", "1m30s", "1h2m3s". Anything else is zero — a bad offset
// is not worth refusing the whole embed over.
func parseYouTubeOffset(t string) int {
	t = strings.ToLower(strings.TrimSpace(t))
	if n, err := strconv.Atoi(strings.TrimSuffix(t, "s")); err == nil && n >= 0 {
		return n
	}
	total, num := 0, 0
	for _, r := range t {
		switch {
		case r >= '0' && r <= '9':
			num = num*10 + int(r-'0')
		case r == 'h':
			total, num = total+num*3600, 0
		case r == 'm':
			total, num = total+num*60, 0
		case r == 's':
			total, num = total+num, 0
		default:
			return 0
		}
	}
	return total + num
}

// youtubeEmbed builds the player. id has already matched
// youtubeIDPattern and start is an int, so the src is constructed from
// values that cannot carry markup; the original URL is escaped into the
// fallback link the same way every other link is.
func youtubeEmbed(id string, start int, original string) string {
	src := "https://www.youtube-nocookie.com/embed/" + id
	if start > 0 {
		src += "?start=" + strconv.Itoa(start)
	}
	cleaned, _ := trimTrailingPunctuation(original)
	return `<div class="my-3 max-w-2xl"><div class="aspect-video overflow-hidden rounded-md bg-gray-900">` +
		`<iframe class="h-full w-full" src="` + src + `" title="YouTube video" loading="lazy" ` +
		`allow="encrypted-media; picture-in-picture; fullscreen" allowfullscreen referrerpolicy="strict-origin-when-cross-origin" ` +
		`sandbox="allow-scripts allow-same-origin allow-popups allow-popups-to-escape-sandbox allow-presentation"></iframe></div>` +
		`<p class="mt-1 text-xs">` + anchor(cleaned, "Watch on YouTube") + `</p></div>`
}
