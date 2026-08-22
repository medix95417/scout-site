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
	ID                     string
	UnitID                 string
	Title                  string
	Description            string
	Location               string
	StartsAt               time.Time
	EndsAt                 *time.Time
	Visibility             string  // "public" | "members"
	Status                 string  // "draft" | "pending_approval" | "published" | "rejected"
	SubGroupID             *string // nil = whole-unit event; set = scoped to one patrol/den (see migration 0018)
	RequiresPermissionSlip bool    // set by the creator — most events (a weekly meeting) don't need one; see migration 0028
}

// eventColumns is the column list every event query below selects, in the
// exact order queryEvents/GetEvent scan them — kept as one constant so
// adding a column (like RequiresPermissionSlip) is a one-line change
// instead of finding every duplicated SELECT.
//
// description and location are nullable in the schema (0001_init.sql) but
// scan into plain strings, so they're COALESCE'd here — the same
// convention the rest of the codebase already uses for nullable text
// (internal/files already COALESCEs events.location in its own join on
// this very table). Without it a single NULL takes down far more than the
// row it's on: queryEvents aborts the whole scan, so one such row 500s the
// entire /calendar page and blanks the homepage's upcoming-events list for
// every visitor. Event.Create only ever writes "" (its inputs are Go
// strings, never nil), so this isn't reachable through the app's own write
// paths today — it's defence against a hand-written INSERT, a restored
// backup, or a future import path introducing what the schema plainly
// allows.
const eventColumns = `id, unit_id, title, COALESCE(description, ''), COALESCE(location, ''), starts_at, ends_at, visibility::text, status::text, sub_group_id::text, requires_permission_slip`

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
	UnitID                 string
	Title                  string
	Description            string
	Location               string
	StartsAt               time.Time
	EndsAt                 *time.Time
	Visibility             string
	CreatedBy              string  // member ID
	SubGroupID             *string // nil = whole-unit event; set = scoped to one patrol/den
	RequiresPermissionSlip bool
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
		INSERT INTO events (unit_id, title, description, location, starts_at, ends_at, visibility, status, created_by, sub_group_id, requires_permission_slip)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+eventColumns+`
	`, in.UnitID, in.Title, in.Description, in.Location, in.StartsAt, in.EndsAt, in.Visibility, status, in.CreatedBy, in.SubGroupID, in.RequiresPermissionSlip,
	).Scan(&e.ID, &e.UnitID, &e.Title, &e.Description, &e.Location, &e.StartsAt, &e.EndsAt, &e.Visibility, &e.Status, &e.SubGroupID, &e.RequiresPermissionSlip)
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
//
// Filters on COALESCE(ends_at, starts_at), not starts_at alone — a
// multi-day event (a weekend campout, say Fri-Sun) needs to keep
// showing as "upcoming" through its actual last day, not just until a
// day after it started. Single-day events (ends_at NULL) are unaffected,
// since COALESCE falls back to starts_at for those exactly as before.
func ListUpcomingForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Event, error) {
	return queryEvents(ctx, pool, `
		SELECT `+eventColumns+`
		FROM events
		WHERE unit_id = $1 AND status = 'published' AND COALESCE(ends_at, starts_at) >= now() - interval '1 day'
		ORDER BY starts_at
	`, unitID)
}

// ListUpcomingPublicForUnit is the same, restricted to public events —
// what the unauthenticated landing page shows. See ListUpcomingForUnit's
// comment for why this filters on COALESCE(ends_at, starts_at).
func ListUpcomingPublicForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Event, error) {
	return queryEvents(ctx, pool, `
		SELECT `+eventColumns+`
		FROM events
		WHERE unit_id = $1 AND status = 'published' AND visibility = 'public' AND COALESCE(ends_at, starts_at) >= now() - interval '1 day'
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
		SELECT `+eventColumns+`
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
		SELECT `+eventColumns+`
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
		SELECT `+eventColumns+`
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
		SELECT `+eventColumns+`
		FROM events WHERE id = $1 AND unit_id = $2
	`, eventID, unitID).Scan(&e.ID, &e.UnitID, &e.Title, &e.Description, &e.Location, &e.StartsAt, &e.EndsAt, &e.Visibility, &e.Status, &e.SubGroupID, &e.RequiresPermissionSlip)
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
		if err := rows.Scan(&e.ID, &e.UnitID, &e.Title, &e.Description, &e.Location, &e.StartsAt, &e.EndsAt, &e.Visibility, &e.Status, &e.SubGroupID, &e.RequiresPermissionSlip); err != nil {
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

// Attendee is one member's RSVP to an event, decorated with enough
// identity to print without a second round trip — the event attendee
// roster (see internal/web's CalendarEventAttendeesExportPDF).
type Attendee struct {
	MemberID   string
	FirstName  string
	LastName   string
	MemberType string
	FamilyName string
	Response   string // "yes" | "no" | "maybe"
}

// AttendeesForEvent lists every RSVP recorded for an event — "yes"
// responses first, then "maybe", then "no", alphabetically by name
// within each — the order an attendee roster should read in (who's
// actually coming, first).
func AttendeesForEvent(ctx context.Context, pool *pgxpool.Pool, eventID string) ([]Attendee, error) {
	rows, err := pool.Query(ctx, `
		SELECT members.id, members.first_name, members.last_name, members.member_type::text, families.name, rsvps.response::text
		FROM rsvps
		JOIN members ON members.id = rsvps.member_id
		JOIN families ON families.id = members.family_id
		WHERE rsvps.event_id = $1
		ORDER BY (rsvps.response = 'yes') DESC, (rsvps.response = 'maybe') DESC, members.last_name, members.first_name
	`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Attendee
	for rows.Next() {
		var a Attendee
		if err := rows.Scan(&a.MemberID, &a.FirstName, &a.LastName, &a.MemberType, &a.FamilyName, &a.Response); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
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

// FilterVisibleToViewer narrows a unit-wide event list down to what one
// particular logged-in viewer should actually see: every unscoped event
// (SubGroupID == nil) plus any event scoped to a patrol/den the viewer
// belongs to, per viewerSubGroups (see internal/roster.SubGroupIDsFor*).
// canSeeAll bypasses the narrowing entirely — for a leader who holds
// units.CanEditUnitContent, who needs to see every den's events for
// scheduling/oversight, the same "broad content access, narrow only for
// roster edits" posture den_leader already gets elsewhere in this app.
// Pure and order-preserving, so it's easy to test and safe to call on
// both the upcoming-events list and the month-grid list.
func FilterVisibleToViewer(events []Event, viewerSubGroups map[string]bool, canSeeAll bool) []Event {
	if canSeeAll {
		return events
	}
	visible := make([]Event, 0, len(events))
	for _, e := range events {
		if e.SubGroupID == nil || viewerSubGroups[*e.SubGroupID] {
			visible = append(visible, e)
		}
	}
	return visible
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
