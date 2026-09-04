package icalendar

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	got, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad test timestamp %q: %v", s, err)
	}
	return got
}

// A feed shaped the way Google Calendar actually emits one: CRLF endings,
// folded lines, a TZID-qualified local time, an escaped description, a
// weekly recurrence with an exception, and an all-day event.
const googleish = "BEGIN:VCALENDAR\r\n" +
	"PRODID:-//Google Inc//Google Calendar 70.9054//EN\r\n" +
	"VERSION:2.0\r\n" +
	"CALSCALE:GREGORIAN\r\n" +
	"METHOD:PUBLISH\r\n" +
	"X-WR-CALNAME:Troop 47\r\n" +
	"BEGIN:VTIMEZONE\r\n" +
	"TZID:America/New_York\r\n" +
	"END:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\n" +
	"DTSTART;TZID=America/New_York:20260901T190000\r\n" +
	"DTEND;TZID=America/New_York:20260901T203000\r\n" +
	"RRULE:FREQ=WEEKLY;BYDAY=TU\r\n" +
	"EXDATE;TZID=America/New_York:20260915T190000\r\n" +
	"UID:weekly-meeting@google.com\r\n" +
	"SUMMARY:Troop Meeting\r\n" +
	"LOCATION:St Paul's Hall\\, Yonkers\r\n" +
	"DESCRIPTION:Bring your handbook.\\nUniform please\\; full Class A.\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"DTSTART;VALUE=DATE:20260918\r\n" +
	"DTEND;VALUE=DATE:20260921\r\n" +
	"UID:fall-camp@google.com\r\n" +
	"SUMMARY:Fall Camporee\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"DTSTART:20260905T140000Z\r\n" +
	"UID:cancelled-thing@google.com\r\n" +
	"SUMMARY:Car Wash\r\n" +
	"STATUS:CANCELLED\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func TestParseGoogleShapedFeed(t *testing.T) {
	from := mustParse(t, "2026-09-01T00:00:00Z")
	until := mustParse(t, "2026-09-30T00:00:00Z")

	events, err := Parse(strings.NewReader(googleish), from, until)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	byName := map[string][]Event{}
	for _, e := range events {
		byName[e.Summary] = append(byName[e.Summary], e)
	}

	// September 2026 Tuesdays: 1, 8, 15, 22, 29. The 15th is excluded by
	// EXDATE, leaving four.
	meetings := byName["Troop Meeting"]
	if len(meetings) != 4 {
		var got []string
		for _, m := range meetings {
			got = append(got, m.Start.Format("2006-01-02"))
		}
		t.Errorf("weekly meeting expanded to %d occurrences, want 4 (got %v)", len(meetings), got)
	}
	for _, m := range meetings {
		if m.Start.Format("2006-01-02") == "2026-09-15" {
			t.Error("the EXDATE'd 15th was imported anyway")
		}
	}

	// 19:00 in New York during daylight saving is 23:00 UTC.
	if len(meetings) > 0 {
		if got := meetings[0].Start.UTC().Format("2006-01-02T15:04Z"); got != "2026-09-01T23:00Z" {
			t.Errorf("TZID time converted to %s, want 2026-09-01T23:00Z", got)
		}
	}

	// Each occurrence needs a distinct UID or the importer collapses them.
	seen := map[string]bool{}
	for _, m := range meetings {
		if seen[m.UID] {
			t.Errorf("duplicate UID %q across occurrences", m.UID)
		}
		seen[m.UID] = true
	}

	if len(meetings) > 0 {
		want := "Bring your handbook.\nUniform please; full Class A."
		if meetings[0].Description != want {
			t.Errorf("description unescaped to %q, want %q", meetings[0].Description, want)
		}
		if meetings[0].Location != "St Paul's Hall, Yonkers" {
			t.Errorf("location unescaped to %q", meetings[0].Location)
		}
	}

	camp := byName["Fall Camporee"]
	if len(camp) != 1 {
		t.Fatalf("camporee: got %d events, want 1", len(camp))
	}
	if !camp[0].AllDay {
		t.Error("camporee should be all-day")
	}
	// DTEND 20260921 is exclusive, so the inclusive last day is the 20th.
	if got := camp[0].End.Format("2006-01-02"); got != "2026-09-20" {
		t.Errorf("all-day end = %s, want 2026-09-20 (exclusive DTEND converted)", got)
	}

	wash := byName["Car Wash"]
	if len(wash) != 1 || !wash[0].Cancelled {
		t.Error("STATUS:CANCELLED was not carried through")
	}
}

func TestParseRejectsHTML(t *testing.T) {
	// The classic mistake: pasting the calendar's sharing page instead of
	// its .ics address.
	_, err := Parse(strings.NewReader("<!doctype html><html><body>Sign in</body></html>"),
		time.Now(), time.Now().AddDate(1, 0, 0))
	if err != ErrNotCalendar {
		t.Errorf("got %v, want ErrNotCalendar", err)
	}
}

func TestParseWindowExcludesOutsideEvents(t *testing.T) {
	events, err := Parse(strings.NewReader(googleish),
		mustParse(t, "2026-11-01T00:00:00Z"), mustParse(t, "2026-11-30T00:00:00Z"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, e := range events {
		if e.Summary != "Troop Meeting" {
			t.Errorf("event %q outside the window was imported", e.Summary)
		}
		if e.Start.Before(mustParse(t, "2026-11-01T00:00:00Z")) {
			t.Errorf("%s starts before the window", e.Summary)
		}
	}
}

func TestUnboundedRecurrenceIsCapped(t *testing.T) {
	feed := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:daily@x\r\n" +
		"DTSTART:20260101T120000Z\r\nRRULE:FREQ=DAILY\r\nSUMMARY:Daily\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	// A century-wide window against a daily rule would be ~36,000 events
	// without the cap.
	events, err := Parse(strings.NewReader(feed),
		mustParse(t, "2026-01-01T00:00:00Z"), mustParse(t, "2126-01-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(events) > maxExpansion+1 {
		t.Errorf("daily rule produced %d events, cap is %d", len(events), maxExpansion)
	}
	if len(events) == 0 {
		t.Error("cap swallowed the event entirely")
	}
}

func TestCountIsCountedFromSeriesStartNotWindow(t *testing.T) {
	// COUNT=3 from January. A window starting in February must not see
	// three more; the series is already exhausted by then.
	feed := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:c@x\r\n" +
		"DTSTART:20260105T120000Z\r\nRRULE:FREQ=WEEKLY;COUNT=3\r\nSUMMARY:Limited\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	events, err := Parse(strings.NewReader(feed),
		mustParse(t, "2026-02-01T00:00:00Z"), mustParse(t, "2026-12-31T00:00:00Z"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d occurrences after the series ended, want 0", len(events))
	}
}

func TestRenderRoundTrips(t *testing.T) {
	in := []Event{{
		UID:     "event-1@troop.47-yonkers.org",
		Summary: "Court of Honour, Autumn",
		// Deliberately awkward: characters that must be escaped, and a
		// line long enough to force folding.
		Description: "Parents welcome; refreshments after.\nPlease arrive by 18:45 — the ceremony begins promptly and the hall doors are locked once it starts.",
		Location:    "St Paul's Hall, Yonkers",
		Start:       mustParse(t, "2026-10-14T23:00:00Z"),
		End:         mustParse(t, "2026-10-15T00:30:00Z"),
	}}

	out := Render("Troop 47", in)

	for _, want := range []string{"BEGIN:VCALENDAR", "METHOD:PUBLISH", "X-WR-CALNAME:Troop 47", "END:VCALENDAR"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("rendered feed missing %q", want)
		}
	}
	// RFC 5545 requires CRLF; a bare LF anywhere means a line was written
	// without it.
	if strings.Contains(strings.ReplaceAll(string(out), "\r\n", ""), "\n") {
		t.Error("output contains a bare LF; every line must end CRLF")
	}
	for _, line := range strings.Split(string(out), "\r\n") {
		if len(line) > foldAt+1 { // +1 for the leading space on a continuation
			t.Errorf("line exceeds the fold limit (%d octets): %q", len(line), line)
		}
	}

	back, err := Parse(strings.NewReader(string(out)),
		mustParse(t, "2026-01-01T00:00:00Z"), mustParse(t, "2027-01-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("re-parsing our own output: %v", err)
	}
	if len(back) != 1 {
		t.Fatalf("round trip produced %d events, want 1", len(back))
	}
	got := back[0]
	if got.Summary != in[0].Summary {
		t.Errorf("summary: got %q want %q", got.Summary, in[0].Summary)
	}
	if got.Description != in[0].Description {
		t.Errorf("description: got %q want %q", got.Description, in[0].Description)
	}
	if got.Location != in[0].Location {
		t.Errorf("location: got %q want %q", got.Location, in[0].Location)
	}
	if !got.Start.Equal(in[0].Start) {
		t.Errorf("start: got %s want %s", got.Start, in[0].Start)
	}
	if !got.End.Equal(in[0].End) {
		t.Errorf("end: got %s want %s", got.End, in[0].End)
	}
}

func TestRenderAllDayRoundTrips(t *testing.T) {
	in := []Event{{
		UID:     "camp@troop",
		Summary: "Fall Camporee",
		Start:   mustParse(t, "2026-09-18T00:00:00Z"),
		End:     mustParse(t, "2026-09-20T00:00:00Z"), // inclusive last day
		AllDay:  true,
	}}
	back, err := Parse(strings.NewReader(string(Render("T", in))),
		mustParse(t, "2026-01-01T00:00:00Z"), mustParse(t, "2027-01-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(back) != 1 {
		t.Fatalf("got %d events", len(back))
	}
	if !back[0].AllDay {
		t.Error("all-day flag lost in the round trip")
	}
	// The inclusive last day must survive the exclusive-DTEND conversion
	// in both directions.
	if got := back[0].End.Format("2006-01-02"); got != "2026-09-20" {
		t.Errorf("all-day end round-tripped to %s, want 2026-09-20", got)
	}
}

func TestFoldingNeverSplitsARune(t *testing.T) {
	// RFC 5545 section 3.1 folds at 75 octets but forbids splitting a
	// multi-octet character across the boundary. Unfolding rejoins the
	// bytes either way, so the damage only shows on the wire: each line a
	// client reads must itself be valid UTF-8. Anything reading the feed
	// line-by-line (a log, a diff, a stricter parser) sees mojibake
	// otherwise.
	//
	// The loop slides the character across the boundary so one iteration
	// lands its bytes exactly astride offset 75.
	for pad := 60; pad < 80; pad++ {
		var b strings.Builder
		line(&b, strings.Repeat("a", pad)+"⛺"+strings.Repeat("b", 20))

		for i, l := range strings.Split(strings.TrimSuffix(b.String(), crlf), crlf) {
			if !utf8.ValidString(strings.TrimPrefix(l, " ")) {
				t.Fatalf("pad=%d: folded line %d is not valid UTF-8: %q", pad, i, l)
			}
		}
	}
}

func TestEscapeUnescapeRoundTrip(t *testing.T) {
	for _, s := range []string{
		`plain`,
		`semi;colon`,
		`comma,separated`,
		`back\slash`,
		"new\nline",
		`all; of \them, at once`,
		`a literal \n that is not a newline`,
	} {
		if got := unescapeText(escapeText(s)); got != s {
			t.Errorf("round trip of %q gave %q", s, got)
		}
	}
}

func TestPropertyWithQuotedParameter(t *testing.T) {
	// A quoted parameter value containing a colon must not be mistaken
	// for the value separator.
	name, params, value := splitProperty(`DTSTART;TZID="America/New_York":20260901T190000`)
	if name != "DTSTART" {
		t.Errorf("name = %q", name)
	}
	if params["TZID"] != "America/New_York" {
		t.Errorf("TZID = %q", params["TZID"])
	}
	if value != "20260901T190000" {
		t.Errorf("value = %q", value)
	}
}
