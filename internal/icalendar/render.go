package icalendar

import (
	"strings"
	"time"
)

// Render turns events into an .ics document.
//
// calName is what a phone shows as the calendar's name once subscribed
// (X-WR-CALNAME — not in RFC 5545, but understood by Google, Apple and
// Outlook alike, and the only way to stop a subscription showing up named
// after its URL).
//
// Every timestamp is written in UTC. That is a deliberate simplification:
// the alternative is emitting a VTIMEZONE component with the unit's local
// zone and its daylight-saving rules, which is a great deal of machinery
// for no visible difference — a client converts a UTC instant to the
// viewer's own zone for display either way. All-day events are the
// exception, since a date has no timezone to convert.
func Render(calName string, events []Event) []byte {
	var b strings.Builder

	line(&b, "BEGIN:VCALENDAR")
	line(&b, "VERSION:2.0")
	// PRODID identifies what produced the file. RFC 5545 requires it; the
	// value is free-form and only ever read by humans debugging a feed.
	line(&b, "PRODID:-//47 Yonkers Scouting//Scout Site//EN")
	line(&b, "CALSCALE:GREGORIAN")
	// PUBLISH marks this as a one-way feed rather than a meeting request.
	// Without it some clients offer the viewer accept/decline buttons for
	// every event, which makes no sense for a subscription.
	line(&b, "METHOD:PUBLISH")
	if calName != "" {
		line(&b, "X-WR-CALNAME:"+escapeText(calName))
	}

	// A hint to the subscribing client about how often to re-fetch. Purely
	// advisory — clients refresh on their own schedule regardless, and
	// Google in particular is known to take many hours — but without it
	// some clients poll far more aggressively than a Scout calendar
	// warrants.
	line(&b, "REFRESH-INTERVAL;VALUE=DURATION:PT4H")
	line(&b, "X-PUBLISHED-TTL:PT4H")

	stamp := time.Now().UTC()
	for _, e := range events {
		renderEvent(&b, e, stamp)
	}

	line(&b, "END:VCALENDAR")
	return []byte(b.String())
}

func renderEvent(b *strings.Builder, e Event, stamp time.Time) {
	line(b, "BEGIN:VEVENT")
	line(b, "UID:"+escapeText(e.UID))
	// DTSTAMP is when this representation was produced, which RFC 5545
	// requires and which is distinct from the event's own times.
	line(b, "DTSTAMP:"+formatUTC(stamp))

	if e.AllDay {
		line(b, "DTSTART;VALUE=DATE:"+e.Start.Format("20060102"))
		if !e.End.IsZero() {
			// DTEND is exclusive for all-day events: a one-day event on
			// the 3rd ends on the 4th. Callers hand us the inclusive last
			// day, the way a person describes it ("the 3rd to the 5th"),
			// so the conversion happens here rather than being every
			// caller's problem to remember.
			line(b, "DTEND;VALUE=DATE:"+e.End.AddDate(0, 0, 1).Format("20060102"))
		}
	} else {
		line(b, "DTSTART:"+formatUTC(e.Start))
		if !e.End.IsZero() {
			line(b, "DTEND:"+formatUTC(e.End))
		}
	}

	if e.Summary != "" {
		line(b, "SUMMARY:"+escapeText(e.Summary))
	}
	if e.Description != "" {
		line(b, "DESCRIPTION:"+escapeText(e.Description))
	}
	if e.Location != "" {
		line(b, "LOCATION:"+escapeText(e.Location))
	}
	if e.URL != "" {
		// URL is not text-escaped: RFC 5545 types it as a URI, and
		// escaping the commas in a query string would corrupt the link.
		line(b, "URL:"+e.URL)
	}
	if !e.LastModified.IsZero() {
		line(b, "LAST-MODIFIED:"+formatUTC(e.LastModified))
	}
	if e.Cancelled {
		line(b, "STATUS:CANCELLED")
	}
	line(b, "END:VEVENT")
}

func formatUTC(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// escapeText applies RFC 5545 section 3.3.11's escaping for TEXT values.
// Order matters: backslash has to go first, or the backslashes introduced
// by the later replacements would themselves be escaped.
func escapeText(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	// A literal newline inside a value would look like the start of a new
	// property to a parser, so it travels as the two characters \n.
	s = strings.ReplaceAll(s, "\r\n", `\n`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\n`)
	return s
}

// line writes one content line, folded to RFC 5545's octet limit.
//
// Folding inserts CRLF followed by a single space; a parser strips both
// to rejoin. The limit is counted in octets rather than characters, but a
// fold must never land in the middle of a multi-byte character — an
// accented name or an emoji in an event title would be corrupted — so the
// split point walks back to a rune boundary.
func line(b *strings.Builder, s string) {
	for len(s) > foldAt {
		cut := foldAt
		// Back off until the next byte starts a new rune. Continuation
		// bytes in UTF-8 are 10xxxxxx.
		for cut > 1 && s[cut]&0xC0 == 0x80 {
			cut--
		}
		b.WriteString(s[:cut])
		b.WriteString(crlf)
		b.WriteString(" ")
		s = s[cut:]
	}
	b.WriteString(s)
	b.WriteString(crlf)
}
