package icalendar

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// MaxFeedBytes caps how much of a remote feed is read. A Google calendar
// with years of history is comfortably under a megabyte; anything past
// this is either not a calendar or not one worth importing, and reading
// it unbounded would let a hostile or broken URL exhaust memory.
const MaxFeedBytes = 5 << 20

// maxExpansion bounds how many occurrences a single recurring event may
// produce. "Every Tuesday, forever" is an entirely normal thing to find
// in a scouting calendar, so expansion has to stop somewhere regardless
// of the window it is asked for.
const maxExpansion = 500

// ErrNotCalendar is returned when the content fetched is not an
// iCalendar document at all — nearly always because the URL was an HTML
// page (a calendar's sharing page rather than its .ics address) or a
// login redirect. Worth distinguishing so the admin page can say
// something more useful than "parse error".
var ErrNotCalendar = errors.New("icalendar: not an iCalendar document")

// Parse reads an .ics document and returns its events, with recurring
// events expanded into individual occurrences that fall between from and
// until.
//
// Expansion happens here rather than at display time because the site
// stores imported events as ordinary rows on its own calendar — there is
// nowhere for an unexpanded RRULE to live. The window is what keeps
// "every Tuesday, forever" finite.
func Parse(r io.Reader, from, until time.Time) ([]Event, error) {
	lines, err := unfold(io.LimitReader(r, MaxFeedBytes))
	if err != nil {
		return nil, err
	}
	if !containsCalendarStart(lines) {
		return nil, ErrNotCalendar
	}

	var (
		events  []Event
		current *parsedEvent
	)
	for _, raw := range lines {
		name, params, value := splitProperty(raw)
		switch strings.ToUpper(name) {
		case "BEGIN":
			if strings.EqualFold(value, "VEVENT") {
				current = &parsedEvent{}
			}
			continue
		case "END":
			if strings.EqualFold(value, "VEVENT") && current != nil {
				events = append(events, current.expand(from, until)...)
				current = nil
			}
			continue
		}
		if current == nil {
			// A property outside a VEVENT — VCALENDAR's own headers, or a
			// VTIMEZONE definition we deliberately don't read. Skipped
			// rather than treated as an error: real feeds are full of
			// components this package has no interest in.
			continue
		}
		current.set(strings.ToUpper(name), params, value)
	}
	return events, nil
}

// parsedEvent accumulates one VEVENT's properties before expansion.
type parsedEvent struct {
	ev       Event
	rrule    string
	exdates  map[time.Time]bool
	hasStart bool
}

func (p *parsedEvent) set(name string, params map[string]string, value string) {
	switch name {
	case "UID":
		p.ev.UID = unescapeText(value)
	case "SUMMARY":
		p.ev.Summary = unescapeText(value)
	case "DESCRIPTION":
		p.ev.Description = unescapeText(value)
	case "LOCATION":
		p.ev.Location = unescapeText(value)
	case "URL":
		p.ev.URL = value
	case "STATUS":
		p.ev.Cancelled = strings.EqualFold(value, "CANCELLED")
	case "RRULE":
		p.rrule = value
	case "DTSTART":
		if t, allDay, err := parseTime(params, value); err == nil {
			p.ev.Start, p.ev.AllDay, p.hasStart = t, allDay, true
		}
	case "DTEND":
		if t, allDay, err := parseTime(params, value); err == nil {
			if allDay {
				// DTEND is exclusive for dates (RFC 5545 3.6.1), so a
				// one-day event on the 3rd arrives as DTEND 20260904.
				// Converted back to the inclusive last day, which is how
				// the rest of this codebase and every human thinks about
				// it.
				t = t.AddDate(0, 0, -1)
			}
			p.ev.End = t
		}
	case "LAST-MODIFIED":
		if t, _, err := parseTime(params, value); err == nil {
			p.ev.LastModified = t
		}
	case "EXDATE":
		// Exceptions to a recurrence: the Tuesday the troop didn't meet.
		// One EXDATE line may carry several comma-separated dates, and a
		// feed may carry several EXDATE lines.
		if p.exdates == nil {
			p.exdates = map[time.Time]bool{}
		}
		for _, one := range strings.Split(value, ",") {
			if t, _, err := parseTime(params, strings.TrimSpace(one)); err == nil {
				p.exdates[t.UTC()] = true
			}
		}
	}
}

// expand turns one VEVENT into the occurrences that fall in [from, until].
func (p *parsedEvent) expand(from, until time.Time) []Event {
	if !p.hasStart || p.ev.UID == "" {
		// Both are required by RFC 5545. Something without them isn't an
		// event we can store or match on a later import, so it is
		// dropped rather than guessed at.
		return nil
	}
	if p.rrule == "" {
		if p.ev.Start.Before(from) || p.ev.Start.After(until) {
			return nil
		}
		return []Event{p.ev}
	}

	rule := parseRRule(p.rrule)
	if rule.freq == "" {
		// An RRULE we don't understand. Better to import the first
		// occurrence than to drop the event entirely and have a leader
		// wonder where their meeting went.
		return []Event{p.ev}
	}

	duration := time.Duration(0)
	if !p.ev.End.IsZero() {
		duration = p.ev.End.Sub(p.ev.Start)
	}

	var out []Event
	for i, occ := range rule.occurrences(p.ev.Start, from, until) {
		if p.exdates[occ.UTC()] {
			continue
		}
		e := p.ev
		e.Start = occ
		if !p.ev.End.IsZero() {
			e.End = occ.Add(duration)
		}
		// Each occurrence needs its own identity, or importing a weekly
		// meeting would collapse every date onto one row. RFC 5545 pairs
		// UID with RECURRENCE-ID for exactly this; the same information
		// is folded into the UID here because the importer matches on a
		// single column.
		e.UID = fmt.Sprintf("%s-%s", p.ev.UID, occ.UTC().Format("20060102T150405Z"))
		out = append(out, e)
		if i >= maxExpansion {
			break
		}
	}
	return out
}

// rrule is the subset of RFC 5545 section 3.3.10 that ordinary calendars
// actually use for a repeating event.
type rrule struct {
	freq     string // DAILY | WEEKLY | MONTHLY | YEARLY
	interval int
	count    int
	until    time.Time
	byDay    []time.Weekday
}

func parseRRule(s string) rrule {
	r := rrule{interval: 1}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.ToUpper(k) {
		case "FREQ":
			switch strings.ToUpper(v) {
			case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
				r.freq = strings.ToUpper(v)
			}
		case "INTERVAL":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				r.interval = n
			}
		case "COUNT":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				r.count = n
			}
		case "UNTIL":
			if t, _, err := parseTime(nil, v); err == nil {
				r.until = t
			}
		case "BYDAY":
			for _, d := range strings.Split(v, ",") {
				// A BYDAY value may carry an ordinal prefix ("2TU" =
				// the second Tuesday). The ordinal is not supported;
				// the weekday still is, which degrades a monthly
				// "second Tuesday" into "every Tuesday that month"
				// rather than losing the event.
				d = strings.TrimLeft(strings.ToUpper(strings.TrimSpace(d)), "+-0123456789")
				if wd, ok := weekdays[d]; ok {
					r.byDay = append(r.byDay, wd)
				}
			}
		}
	}
	return r
}

var weekdays = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday,
	"WE": time.Wednesday, "TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

// occurrences walks the recurrence forward from start, returning those
// that land inside [from, until].
func (r rrule) occurrences(start, from, until time.Time) []time.Time {
	var out []time.Time
	stop := until
	if !r.until.IsZero() && r.until.Before(stop) {
		stop = r.until
	}

	emitted := 0
	// Walking one step at a time from the event's real start keeps COUNT
	// honest — COUNT is counted from the series start, not from the
	// window, so jumping straight to the window would over-count.
	for cur, steps := start, 0; !cur.After(stop) && steps <= maxExpansion; steps++ {
		candidates := []time.Time{cur}
		if r.freq == "WEEKLY" && len(r.byDay) > 0 {
			// A weekly rule may name several days ("every Tuesday and
			// Thursday"), each relative to the week cur sits in.
			candidates = nil
			weekStart := cur.AddDate(0, 0, -int(cur.Weekday()))
			for _, wd := range r.byDay {
				d := weekStart.AddDate(0, 0, int(wd))
				candidates = append(candidates, time.Date(d.Year(), d.Month(), d.Day(),
					cur.Hour(), cur.Minute(), cur.Second(), 0, cur.Location()))
			}
		}
		for _, c := range candidates {
			if c.Before(start) || c.After(stop) {
				continue
			}
			if r.count > 0 && emitted >= r.count {
				return out
			}
			emitted++
			if !c.Before(from) {
				out = append(out, c)
			}
		}
		cur = r.step(cur)
	}
	return out
}

func (r rrule) step(t time.Time) time.Time {
	switch r.freq {
	case "DAILY":
		return t.AddDate(0, 0, r.interval)
	case "WEEKLY":
		return t.AddDate(0, 0, 7*r.interval)
	case "MONTHLY":
		return t.AddDate(0, r.interval, 0)
	case "YEARLY":
		return t.AddDate(r.interval, 0, 0)
	}
	// Unreachable for a rule that parsed, but a rule that somehow has no
	// frequency must still advance or the caller's loop never ends.
	return t.AddDate(0, 0, 1)
}

// parseTime reads the three DTSTART/DTEND forms real calendars emit:
//
//	20260901T190000Z         an instant in UTC
//	20260901T190000          local to the TZID parameter, or floating
//	20260901                 a date, meaning an all-day event
//
// A TZID naming a zone the binary can't resolve falls back to UTC rather
// than dropping the event: a meeting an hour or two out of place is a
// smaller failure than one that silently never imported. (Resolution
// itself is only reliable because the package embeds tzdata — see the
// import comment in icalendar.go.)
func parseTime(params map[string]string, value string) (time.Time, bool, error) {
	value = strings.TrimSpace(value)
	switch {
	case len(value) == 8:
		t, err := time.Parse("20060102", value)
		return t, true, err
	case strings.HasSuffix(value, "Z"):
		t, err := time.Parse("20060102T150405Z", value)
		return t.UTC(), false, err
	default:
		loc := time.UTC
		if tzid := params["TZID"]; tzid != "" {
			if l, err := time.LoadLocation(tzid); err == nil {
				loc = l
			}
		}
		t, err := time.ParseInLocation("20060102T150405", value, loc)
		return t.UTC(), false, err
	}
}

// unfold reads the document and rejoins RFC 5545 folded lines. A
// continuation line is one beginning with a space or tab; both it and the
// preceding CRLF are removed to recover the original value.
func unfold(r io.Reader) ([]string, error) {
	sc := bufio.NewScanner(r)
	// A single content line can legitimately be long (a description with
	// a pasted agenda), and the default 64KB token limit would break it.
	sc.Buffer(make([]byte, 0, 64*1024), MaxFeedBytes)

	var lines []string
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += line[1:]
			continue
		}
		lines = append(lines, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("icalendar: reading feed: %w", err)
	}
	return lines, nil
}

func containsCalendarStart(lines []string) bool {
	for _, l := range lines {
		if strings.EqualFold(strings.TrimSpace(l), "BEGIN:VCALENDAR") {
			return true
		}
	}
	return false
}

// splitProperty breaks "NAME;PARAM=value:the value" into its three parts.
//
// The colon that ends the name-and-parameters section is the first one
// that is not inside a quoted parameter value — a parameter may legally
// contain a colon if quoted, which a naive Cut on ":" would split in the
// wrong place (TZID values with a colon do occur in the wild).
func splitProperty(line string) (name string, params map[string]string, value string) {
	inQuotes := false
	colon := -1
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inQuotes = !inQuotes
		case ':':
			if !inQuotes {
				colon = i
			}
		}
		if colon >= 0 {
			break
		}
	}
	if colon < 0 {
		return line, nil, ""
	}

	head, value := line[:colon], line[colon+1:]
	parts := splitUnquoted(head, ';')
	name = parts[0]
	if len(parts) > 1 {
		params = make(map[string]string, len(parts)-1)
		for _, p := range parts[1:] {
			k, v, ok := strings.Cut(p, "=")
			if !ok {
				continue
			}
			params[strings.ToUpper(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return name, params, value
}

func splitUnquoted(s string, sep byte) []string {
	var out []string
	inQuotes := false
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			inQuotes = !inQuotes
		}
		if s[i] == sep && !inQuotes {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// unescapeText reverses escapeText. \\ has to be handled in the same pass
// as the others rather than last, or an escaped backslash followed by an
// n ("\\n", a literal backslash then the letter n) would wrongly become a
// newline.
func unescapeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n', 'N':
			b.WriteByte('\n')
		case '\\', ';', ',':
			b.WriteByte(s[i])
		default:
			// Not a defined escape. Keep both characters rather than
			// swallowing the backslash, so a Windows path in a
			// description survives.
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
