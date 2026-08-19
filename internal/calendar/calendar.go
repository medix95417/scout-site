// Package calendar manages events and RSVPs, including routing
// SPL/Patrol-Leader-submitted events through the approval workflow before
// they're published.
package calendar

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/approval"
	"github.com/47-yonkers/scout-site/internal/audit"
)

type Event struct {
	ID          string
	UnitID      string
	Title       string
	Description string
	Location    string
	StartsAt    time.Time
	EndsAt      *time.Time
	Visibility  string // "public" | "members"
	Status      string // "draft" | "pending_approval" | "published" | "rejected"
}

// DateRangeDisplay is the human-friendly date/time string used everywhere
// an event's schedule is shown — the calendar list, the month grid, and the
// homepage's upcoming events. See FormatDateRange for the formatting rules.
func (e Event) DateRangeDisplay() string {
	return FormatDateRange(e.StartsAt, e.EndsAt)
}

// FormatDateRange renders a start (and optional end) time as a single
// human-friendly string, collapsing redundant information:
//   - no end time: "Mon Jan 2, 2006 · 3:04 PM"
//   - end time same calendar day as start: "Mon Jan 2, 2006 · 3:04 PM–5:04 PM"
//     (no point repeating the date twice for a same-day event)
//   - end time on a later day (a multi-day event, e.g. a weekend campout):
//     "Fri Jul 3, 2026 6:00 PM – Sun Jul 5, 2026 12:00 PM"
func FormatDateRange(start time.Time, end *time.Time) string {
	const dateFmt = "Mon Jan 2, 2006"
	const timeFmt = "3:04 PM"

	if end == nil {
		return start.Format(dateFmt + " · " + timeFmt)
	}
	if sameDay(start, *end) {
		return fmt.Sprintf("%s · %s–%s", start.Format(dateFmt), start.Format(timeFmt), end.Format(timeFmt))
	}
	return fmt.Sprintf("%s %s – %s %s", start.Format(dateFmt), start.Format(timeFmt), end.Format(dateFmt), end.Format(timeFmt))
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// CreateInput is what a submitter provides; Status/approval routing is
// decided by Create based on the submitter's roles, not passed in by the
// caller — that keeps "can this role publish directly?" logic in one place.
type CreateInput struct {
	UnitID      string
	Title       string
	Description string
	Location    string
	StartsAt    time.Time
	EndsAt      *time.Time
	Visibility  string
	CreatedBy   string // member ID
}

// Create inserts an event. If canPublishDirectly is false (the creator only
// holds a role that requires approval — SPL or Patrol Leader), the event is
// created with status "pending_approval" and a matching approval_requests
// row is opened via the approval package, rather than going live
// immediately.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateInput, canPublishDirectly bool) (Event, error) {
	status := "published"
	if !canPublishDirectly {
		status = "pending_approval"
	}

	var e Event
	err := pool.QueryRow(ctx, `
		INSERT INTO events (unit_id, title, description, location, starts_at, ends_at, visibility, status, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, unit_id, title, description, location, starts_at, ends_at, visibility::text, status::text
	`, in.UnitID, in.Title, in.Description, in.Location, in.StartsAt, in.EndsAt, in.Visibility, status, in.CreatedBy,
	).Scan(&e.ID, &e.UnitID, &e.Title, &e.Description, &e.Location, &e.StartsAt, &e.EndsAt, &e.Visibility, &e.Status)
	if err != nil {
		return Event{}, err
	}

	if !canPublishDirectly {
		if _, err := approval.Submit(ctx, pool, "event", e.ID, in.UnitID, in.CreatedBy); err != nil {
			return Event{}, err
		}
	} else {
		audit.Log(ctx, pool, audit.Entry{
			EntityType: "event",
			EntityID:   e.ID,
			ActorID:    &in.CreatedBy,
			Action:     "create",
			After:      e,
		})
	}

	return e, nil
}

// ListUpcomingForUnit returns published events from today forward, visible
// to a logged-in member (i.e. both public and members-only). Unauthenticated
// visitors should use ListUpcomingPublicForUnit instead.
func ListUpcomingForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Event, error) {
	return queryEvents(ctx, pool, `
		SELECT id, unit_id, title, description, location, starts_at, ends_at, visibility::text, status::text
		FROM events
		WHERE unit_id = $1 AND status = 'published' AND starts_at >= now() - interval '1 day'
		ORDER BY starts_at
	`, unitID)
}

// ListUpcomingPublicForUnit is the same, restricted to public events —
// what the unauthenticated landing page shows.
func ListUpcomingPublicForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Event, error) {
	return queryEvents(ctx, pool, `
		SELECT id, unit_id, title, description, location, starts_at, ends_at, visibility::text, status::text
		FROM events
		WHERE unit_id = $1 AND status = 'published' AND visibility = 'public' AND starts_at >= now() - interval '1 day'
		ORDER BY starts_at
	`, unitID)
}

// ListForRangeForUnit returns published events (any visibility) that
// overlap [rangeStart, rangeEnd) at all — including a multi-day event that
// started before rangeStart but is still running, or one that starts
// within the range but ends after it. This is what the month grid needs,
// as opposed to ListUpcomingForUnit's "starting from today" list.
func ListForRangeForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string, rangeStart, rangeEnd time.Time) ([]Event, error) {
	return queryEvents(ctx, pool, `
		SELECT id, unit_id, title, description, location, starts_at, ends_at, visibility::text, status::text
		FROM events
		WHERE unit_id = $1 AND status = 'published'
			AND starts_at < $3 AND COALESCE(ends_at, starts_at) >= $2
		ORDER BY starts_at
	`, unitID, rangeStart, rangeEnd)
}

// ListForRangePublicForUnit is ListForRangeForUnit restricted to public
// events, for the month grid shown to unauthenticated visitors.
func ListForRangePublicForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string, rangeStart, rangeEnd time.Time) ([]Event, error) {
	return queryEvents(ctx, pool, `
		SELECT id, unit_id, title, description, location, starts_at, ends_at, visibility::text, status::text
		FROM events
		WHERE unit_id = $1 AND status = 'published' AND visibility = 'public'
			AND starts_at < $3 AND COALESCE(ends_at, starts_at) >= $2
		ORDER BY starts_at
	`, unitID, rangeStart, rangeEnd)
}

// ListAllForUnit returns every published event for a unit, most recent
// first — used by the file library (internal/web/files.go) to populate its
// "link this file to events" picker, which needs to reach past events too
// (attaching trip photos after the fact), not just ListUpcomingForUnit's
// forward-looking list.
func ListAllForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Event, error) {
	return queryEvents(ctx, pool, `
		SELECT id, unit_id, title, description, location, starts_at, ends_at, visibility::text, status::text
		FROM events
		WHERE unit_id = $1 AND status = 'published'
		ORDER BY starts_at DESC
	`, unitID)
}

// GetEvent looks up a single event, scoped to a unit — used by the
// permission-slip page (internal/web/permission_slip.go) to resolve which
// event a slip is being attached to/viewed for. Deliberately not
// restricted to status = 'published': a leader composing a permission
// slip for an event still pending approval, or reviewing one on a
// rejected event, should still be able to reach it.
func GetEvent(ctx context.Context, pool *pgxpool.Pool, eventID, unitID string) (Event, bool, error) {
	var e Event
	err := pool.QueryRow(ctx, `
		SELECT id, unit_id, title, description, location, starts_at, ends_at, visibility::text, status::text
		FROM events WHERE id = $1 AND unit_id = $2
	`, eventID, unitID).Scan(&e.ID, &e.UnitID, &e.Title, &e.Description, &e.Location, &e.StartsAt, &e.EndsAt, &e.Visibility, &e.Status)
	if err != nil {
		return Event{}, false, nil //nolint:nilerr // "no such event in this unit" is a normal, expected outcome
	}
	return e, true, nil
}

func queryEvents(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) ([]Event, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.UnitID, &e.Title, &e.Description, &e.Location, &e.StartsAt, &e.EndsAt, &e.Visibility, &e.Status); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ErrEventNotFound is returned by SetRSVP when eventID doesn't identify an
// event in unitID — either it doesn't exist, or (the case this exists to
// guard against) it belongs to a different unit. Without this check, a
// logged-in member of one unit could record an RSVP against the *other*
// unit's event by guessing its ID, since nothing about "is this person
// logged in and a member of some unit" says anything about which unit a
// specific event belongs to.
var ErrEventNotFound = errors.New("calendar: no event with that id in this unit")

var validRSVPResponses = map[string]bool{"yes": true, "no": true, "maybe": true}

// SetRSVP records or updates a member's response to an event.
func SetRSVP(ctx context.Context, pool *pgxpool.Pool, eventID, unitID, memberID, response string) error {
	if !validRSVPResponses[response] {
		return fmt.Errorf("calendar: %q is not a valid RSVP response", response)
	}

	tag, err := pool.Exec(ctx, `
		INSERT INTO rsvps (event_id, member_id, response)
		SELECT $1, $3, $4
		WHERE EXISTS (SELECT 1 FROM events WHERE events.id = $1 AND events.unit_id = $2)
		ON CONFLICT (event_id, member_id)
		DO UPDATE SET response = EXCLUDED.response, updated_at = now()
	`, eventID, unitID, memberID, response)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrEventNotFound
	}
	return nil
}

// --- Month grid ---------------------------------------------------------
//
// A graphical, Google-Calendar-style month view alongside the existing
// upcoming-events list. Building the grid is pure (no DB access) — the
// handler fetches events for the visible range with ListForRangeForUnit /
// ListForRangePublicForUnit, then hands them to BuildMonthGrid.

// DayEvent is an Event as it appears on one particular day of a month
// grid. IsFirstDay/IsLastDay let the template draw a multi-day event as a
// continuous-looking bar — rounded corners on its first and last day,
// square where it continues from the day before or into the day after.
type DayEvent struct {
	Event
	IsFirstDay bool
	IsLastDay  bool
}

// DayCell is one square of a month grid.
type DayCell struct {
	Date    time.Time
	InMonth bool // false for the leading/trailing days from adjacent months that fill out a 7-column grid
	IsToday bool
	Events  []DayEvent
}

// MonthGrid is a full month laid out as calendar weeks, plus enough to
// build "previous/next/today" navigation links around it.
type MonthGrid struct {
	Label      string // e.g. "August 2026"
	PrevParam  string // e.g. "2026-07" — value for the page's ?month= query param
	NextParam  string
	TodayParam string // current real-world month, for a "Today" link when viewing a different one
	Weeks      [][]DayCell
}

// BuildMonthGrid lays events out into a 7-column, whole-weeks grid for the
// given month. today is used to mark the current day and to compute
// TodayParam — pass time.Now().In(time.Local) (or equivalent) from the
// caller rather than calling time.Now() here, keeping this function pure
// and easy to reason about/test.
func BuildMonthGrid(events []Event, year int, month time.Month, today time.Time) MonthGrid {
	loc := today.Location()
	firstOfMonth := time.Date(year, month, 1, 0, 0, 0, 0, loc)
	lastOfMonth := firstOfMonth.AddDate(0, 1, -1)

	// Grid runs from the Sunday on/before the 1st through the Saturday
	// on/after the last day, so partial weeks at either edge still show a
	// full row rather than a ragged one.
	gridStart := firstOfMonth.AddDate(0, 0, -int(firstOfMonth.Weekday()))
	gridEnd := lastOfMonth.AddDate(0, 0, 6-int(lastOfMonth.Weekday()))

	todayY, todayM, todayD := today.Date()

	var weeks [][]DayCell
	var week []DayCell
	for d := gridStart; !d.After(gridEnd); d = d.AddDate(0, 0, 1) {
		dy, dm, dd := d.Date()
		cellDate := time.Date(dy, dm, dd, 0, 0, 0, 0, loc)
		cell := DayCell{
			Date:    cellDate,
			InMonth: dm == month,
			IsToday: dy == todayY && dm == todayM && dd == todayD,
		}

		for _, e := range events {
			sy, sm, sd := e.StartsAt.Date()
			startDate := time.Date(sy, sm, sd, 0, 0, 0, 0, loc)
			endDate := startDate
			if e.EndsAt != nil {
				ey, em, ed := e.EndsAt.Date()
				endDate = time.Date(ey, em, ed, 0, 0, 0, 0, loc)
			}
			if cellDate.Before(startDate) || cellDate.After(endDate) {
				continue
			}
			cell.Events = append(cell.Events, DayEvent{
				Event:      e,
				IsFirstDay: cellDate.Equal(startDate),
				IsLastDay:  cellDate.Equal(endDate),
			})
		}

		week = append(week, cell)
		if len(week) == 7 {
			weeks = append(weeks, week)
			week = nil
		}
	}

	prev := firstOfMonth.AddDate(0, -1, 0)
	next := firstOfMonth.AddDate(0, 1, 0)
	return MonthGrid{
		Label:      firstOfMonth.Format("January 2006"),
		PrevParam:  prev.Format("2006-01"),
		NextParam:  next.Format("2006-01"),
		TodayParam: today.Format("2006-01"),
		Weeks:      weeks,
	}
}

// GridRange returns the [start, end) instants a month grid spans, in the
// given location — what the caller should pass to ListForRangeForUnit /
// ListForRangePublicForUnit to fetch exactly the events BuildMonthGrid can
// place. Kept in sync with BuildMonthGrid's own grid-edge math on purpose;
// see the comment there for why the grid extends into neighboring months.
func GridRange(year int, month time.Month, loc *time.Location) (start, end time.Time) {
	firstOfMonth := time.Date(year, month, 1, 0, 0, 0, 0, loc)
	lastOfMonth := firstOfMonth.AddDate(0, 1, -1)
	gridStart := firstOfMonth.AddDate(0, 0, -int(firstOfMonth.Weekday()))
	gridEnd := lastOfMonth.AddDate(0, 0, 6-int(lastOfMonth.Weekday()))
	return gridStart, gridEnd.AddDate(0, 0, 1)
}
