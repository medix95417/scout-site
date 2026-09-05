// Package prospect is the record of a family who asked about joining —
// what they told the public "interested in joining" form, and what the
// unit did about it.
//
// Kept as its own package rather than folded into internal/roster
// because a prospect deliberately isn't a member: they have no family,
// no login, no roles, and may never have any. Joining is a separate act
// a leader performs on the roster page, and wiring the two together
// would put a half-real person in the members table on the strength of
// a web form anyone can fill in.
//
// Same separation as every other business-logic package here: data model
// and rules only, no HTTP and no templates.
package prospect

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

// Status values, in the order a prospect normally moves through them.
const (
	StatusNew       = "new"
	StatusContacted = "contacted"
	StatusVisited   = "visited"
	StatusJoined    = "joined"
	StatusDeclined  = "declined"
)

// StatusOption is one status and how it reads on screen.
type StatusOption struct {
	Value string
	Label string
}

// Statuses is every status a leader can set, in workflow order. Matches
// the prospect_status enum in migration 0042 — the drift between the two
// is what prospect_test.go's status test exists to catch.
var Statuses = []StatusOption{
	{StatusNew, "New enquiry"},
	{StatusContacted, "Contacted"},
	{StatusVisited, "Visited a meeting"},
	{StatusJoined, "Joined"},
	{StatusDeclined, "Not joining"},
}

// IsStatus reports whether a value is one this package recognizes — the
// server-side check behind the admin dropdown, since a status arrives as
// a form value and a form value can be anything.
func IsStatus(v string) bool {
	for _, s := range Statuses {
		if s.Value == v {
			return true
		}
	}
	return false
}

// Open reports whether a status still wants someone's attention, which
// is what the admin list defaults to showing.
func Open(status string) bool {
	return status != StatusJoined && status != StatusDeclined
}

// Prospect is one enquiry.
type Prospect struct {
	ID          string
	UnitID      string
	ParentName  string
	ParentEmail string
	ParentPhone string
	ChildName   string
	ChildAge    *int // nil when they didn't say
	ChildGrade  string
	ChildSchool string
	Message     string
	Status      string
	Notes       string
	// EmailOptOut is set when this family has asked not to be included in
	// recruiting emails — by themselves through the unsubscribe link, or
	// by a leader on the admin page. Either way RecipientsForStatuses
	// skips them, and the record stays so the next campaign doesn't
	// quietly add them back.
	EmailOptOut bool
	OptOutAt    *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// StatusLabel is this prospect's status as it reads on screen.
func (p Prospect) StatusLabel() string {
	for _, s := range Statuses {
		if s.Value == p.Status {
			return s.Label
		}
	}
	return p.Status
}

// Open reports whether this prospect still needs attention.
func (p Prospect) Open() bool { return Open(p.Status) }

// Field length caps. Enforced here as well as by CHECK constraints in
// migration 0042: these produce a sentence the person filling in the
// form can act on, the constraints are the guarantee that nothing longer
// reaches the table whatever a future caller does.
const (
	MaxName    = 120
	MaxEmail   = 200
	MaxPhone   = 40
	MaxGrade   = 40
	MaxSchool  = 120
	MaxMessage = 2000
	MaxNotes   = 4000

	// MinAge/MaxAge bound a plausible Scouting age. Deliberately wide:
	// the point is to reject a typo or a bot filling every field with the
	// same number, not to enforce a program's real age range, which
	// differs between a Pack and a Troop and isn't this form's business.
	MinAge = 3
	MaxAge = 21
)

// ErrInvalid is returned by Create for input a person can fix, wrapped
// with which field and why. Anything else is a real failure.
var ErrInvalid = errors.New("prospect: invalid submission")

// New is the submitted, not-yet-validated form.
type New struct {
	UnitID      string
	ParentName  string
	ParentEmail string
	ParentPhone string
	ChildName   string
	ChildAge    *int
	ChildGrade  string
	ChildSchool string
	Message     string
}

// Create validates and stores an enquiry.
//
// No audit entry: audit_log.actor_id is a member, and the whole point of
// a prospect is that nobody involved is one yet. The row's own
// created_at is the record that it arrived; everything a leader does to
// it afterwards is audited by UpdateStatus.
func Create(ctx context.Context, pool *pgxpool.Pool, in New) (Prospect, error) {
	in.ParentName = strings.TrimSpace(in.ParentName)
	in.ParentEmail = strings.TrimSpace(in.ParentEmail)
	in.ParentPhone = strings.TrimSpace(in.ParentPhone)
	in.ChildName = strings.TrimSpace(in.ChildName)
	in.ChildGrade = strings.TrimSpace(in.ChildGrade)
	in.ChildSchool = strings.TrimSpace(in.ChildSchool)
	in.Message = strings.TrimSpace(in.Message)

	switch {
	case in.ParentName == "":
		return Prospect{}, fmt.Errorf("%w: your name is required", ErrInvalid)
	case len(in.ParentName) > MaxName:
		return Prospect{}, fmt.Errorf("%w: that name is too long", ErrInvalid)
	case in.ParentEmail == "" || !strings.Contains(in.ParentEmail, "@"):
		return Prospect{}, fmt.Errorf("%w: a valid email address is required so we can reply", ErrInvalid)
	case len(in.ParentEmail) > MaxEmail:
		return Prospect{}, fmt.Errorf("%w: that email address is too long", ErrInvalid)
	case len(in.ParentPhone) > MaxPhone:
		return Prospect{}, fmt.Errorf("%w: that phone number is too long", ErrInvalid)
	case in.ChildName == "":
		return Prospect{}, fmt.Errorf("%w: your child's name is required", ErrInvalid)
	case len(in.ChildName) > MaxName:
		return Prospect{}, fmt.Errorf("%w: that name is too long", ErrInvalid)
	case in.ChildAge != nil && (*in.ChildAge < MinAge || *in.ChildAge > MaxAge):
		return Prospect{}, fmt.Errorf("%w: enter an age between %d and %d, or leave it blank", ErrInvalid, MinAge, MaxAge)
	case len(in.ChildGrade) > MaxGrade:
		return Prospect{}, fmt.Errorf("%w: that grade is too long", ErrInvalid)
	case len(in.ChildSchool) > MaxSchool:
		return Prospect{}, fmt.Errorf("%w: that school name is too long", ErrInvalid)
	case len(in.Message) > MaxMessage:
		return Prospect{}, fmt.Errorf("%w: please keep the message under %d characters", ErrInvalid, MaxMessage)
	}

	return scan(pool.QueryRow(ctx, `
		INSERT INTO prospects (unit_id, parent_name, parent_email, parent_phone,
			child_name, child_age, child_grade, child_school, message)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+columns,
		in.UnitID, in.ParentName, in.ParentEmail, in.ParentPhone,
		in.ChildName, in.ChildAge, in.ChildGrade, in.ChildSchool, in.Message))
}

const columns = `id::text, unit_id::text, parent_name, parent_email, parent_phone,
	child_name, child_age, child_grade, child_school, message, status::text, notes,
	email_opt_out, opt_out_at, created_at, updated_at`

func scan(row pgx.Row) (Prospect, error) {
	var p Prospect
	err := row.Scan(&p.ID, &p.UnitID, &p.ParentName, &p.ParentEmail, &p.ParentPhone,
		&p.ChildName, &p.ChildAge, &p.ChildGrade, &p.ChildSchool, &p.Message,
		&p.Status, &p.Notes, &p.EmailOptOut, &p.OptOutAt, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// ErrNotFound is returned when a prospect id isn't this unit's — the
// same cross-tenant-safe "not found" the rest of the codebase returns
// rather than distinguishing "doesn't exist" from "isn't yours".
var ErrNotFound = errors.New("prospect: not found in this unit")

// Get loads one prospect, scoped to its unit.
func Get(ctx context.Context, pool *pgxpool.Pool, unitID, id string) (Prospect, error) {
	p, err := scan(pool.QueryRow(ctx,
		`SELECT `+columns+` FROM prospects WHERE id = $1 AND unit_id = $2`, id, unitID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Prospect{}, ErrNotFound
	}
	return p, err
}

// ListForUnit returns a unit's prospects, newest first. openOnly narrows
// to the ones still needing attention.
func ListForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string, openOnly bool) ([]Prospect, error) {
	sql := `SELECT ` + columns + ` FROM prospects WHERE unit_id = $1`
	if openOnly {
		sql += ` AND status NOT IN ('joined', 'declined')`
	}
	sql += ` ORDER BY created_at DESC`

	rows, err := pool.Query(ctx, sql, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Prospect
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountOpenForUnit is how many prospects still need attention — for the
// nav badge, so a leader doesn't have to remember to look.
func CountOpenForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM prospects WHERE unit_id = $1 AND status NOT IN ('joined', 'declined')`,
		unitID).Scan(&n)
	return n, err
}

// UpdateStatus records where a prospect has got to and what was said.
// Audited: this is a leader acting on someone's enquiry, and "we called
// them and they're coming Tuesday" is exactly the kind of thing the next
// leader needs to be able to find.
func UpdateStatus(ctx context.Context, pool *pgxpool.Pool, unitID, id, status, notes, actorID string) (Prospect, error) {
	if !IsStatus(status) {
		return Prospect{}, fmt.Errorf("%w: unknown status %q", ErrInvalid, status)
	}
	if len(notes) > MaxNotes {
		return Prospect{}, fmt.Errorf("%w: those notes are too long", ErrInvalid)
	}

	before, err := Get(ctx, pool, unitID, id)
	if err != nil {
		return Prospect{}, err
	}

	after, err := scan(pool.QueryRow(ctx, `
		UPDATE prospects SET status = $1, notes = $2, updated_at = now()
		WHERE id = $3 AND unit_id = $4
		RETURNING `+columns, status, strings.TrimSpace(notes), id, unitID))
	if err != nil {
		return Prospect{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "prospect",
		EntityID:   after.ID,
		ActorID:    &actorID,
		Action:     "update",
		Before:     before,
		After:      after,
	})
	return after, nil
}

// Delete removes an enquiry outright — for the spam that a public form
// eventually attracts, which shouldn't have to sit in the list marked
// "not joining" forever.
func Delete(ctx context.Context, pool *pgxpool.Pool, unitID, id, actorID string) error {
	before, err := Get(ctx, pool, unitID, id)
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM prospects WHERE id = $1 AND unit_id = $2`, id, unitID); err != nil {
		return err
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "prospect",
		EntityID:   id,
		ActorID:    &actorID,
		Action:     "delete",
		Before:     before,
	})
	return nil
}
