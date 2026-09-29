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

// The statuses a unit starts with. They are no longer a fixed list:
// migration 0048 moved them into prospect_labels, one row per unit, so
// a leader can add their own — see labels.go. These constants remain
// for the two values the rest of the system still names directly: the
// status a new enquiry lands on, and the two the defaults mark closed.

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
	// Category is the unit's own filing of this enquiry — which program
	// they asked about, how they heard of us — independent of Status,
	// which is where they have got to. "" means uncategorised, which is
	// what the public form produces: a category is a leader's decision,
	// so nothing is guessed on their behalf.
	Category string
	// Source is how this enquiry arrived: SourceForm when a family
	// filled in the public form, SourceLeader when someone entered it
	// on the admin page. It decides whether an automatic reply is
	// appropriate — see migration 0049.
	Source string
	Notes  string
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

// LabelIn renders a stored value using a unit's label text (see
// LabelText), falling back to the raw value.
//
// The fallback is the point: a prospect can be sitting on a status
// that has since been retired, and a campaign's audience can name one
// that was never this unit's at all. Showing the slug is ugly; showing
// nothing loses the fact that it was set.
func LabelIn(text map[string]string, value string) string {
	if value == "" {
		return ""
	}
	if s, ok := text[value]; ok {
		return s
	}
	return value
}

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

	// MinAge/MaxAge bound a plausible age. Deliberately wide: the point
	// is to reject a typo or a bot filling every field with the same
	// number, not to enforce a program's real age range, which differs
	// between a Pack and a Troop and isn't this form's business.
	//
	// The floor is 0 rather than the youngest a child could join,
	// because a unit meets a family with a one-year-old sibling at a
	// recruiting night and wants to remember them for the year they are
	// old enough. Keeping that is what a prospect list is for; refusing
	// the age only pushed it into the notes, where nothing can sort on
	// it. See migration 0051.
	MinAge = 0
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
	// Source defaults to SourceForm when empty — see sourceOrForm.
	Source string
}

// How an enquiry arrived. See migration 0049.
const (
	// SourceForm: a family filled in the public join form.
	SourceForm = "form"
	// SourceLeader: someone entered it on the admin page, from a list
	// or a conversation. Nobody at this address has asked the unit for
	// anything, which is why nothing is emailed to them automatically.
	SourceLeader = "leader"
)

// Create validates and stores an enquiry.
//
// No audit entry: audit_log.actor_id is a member, and the whole point of
// a prospect is that nobody involved is one yet. The row's own
// created_at is the record that it arrived; everything a leader does to
// it afterwards is audited by UpdateStatus.
// Validate checks an enquiry and normalises its whitespace, returning
// the cleaned-up value.
//
// Split out of Create so the bulk-paste preview can hold a row to
// exactly the rules the save will apply — a preview that promises a row
// the save then refuses is worse than no preview at all.
func Validate(in New) (New, error) {
	in.ParentName = strings.TrimSpace(in.ParentName)
	in.ParentEmail = strings.TrimSpace(in.ParentEmail)
	in.ParentPhone = strings.TrimSpace(in.ParentPhone)
	in.ChildName = strings.TrimSpace(in.ChildName)
	in.ChildGrade = strings.TrimSpace(in.ChildGrade)
	in.ChildSchool = strings.TrimSpace(in.ChildSchool)
	in.Message = strings.TrimSpace(in.Message)

	switch {
	case in.ParentName == "":
		return in, fmt.Errorf("%w: your name is required", ErrInvalid)
	case len(in.ParentName) > MaxName:
		return in, fmt.Errorf("%w: that name is too long", ErrInvalid)
	case in.ParentEmail == "" || !strings.Contains(in.ParentEmail, "@"):
		return in, fmt.Errorf("%w: a valid email address is required so we can reply", ErrInvalid)
	case len(in.ParentEmail) > MaxEmail:
		return in, fmt.Errorf("%w: that email address is too long", ErrInvalid)
	case len(in.ParentPhone) > MaxPhone:
		return in, fmt.Errorf("%w: that phone number is too long", ErrInvalid)
	case in.ChildName == "":
		return in, fmt.Errorf("%w: your child's name is required", ErrInvalid)
	case len(in.ChildName) > MaxName:
		return in, fmt.Errorf("%w: that name is too long", ErrInvalid)
	case in.ChildAge != nil && (*in.ChildAge < MinAge || *in.ChildAge > MaxAge):
		return in, fmt.Errorf("%w: enter an age between %d and %d, or leave it blank", ErrInvalid, MinAge, MaxAge)
	case len(in.ChildGrade) > MaxGrade:
		return in, fmt.Errorf("%w: that grade is too long", ErrInvalid)
	case len(in.ChildSchool) > MaxSchool:
		return in, fmt.Errorf("%w: that school name is too long", ErrInvalid)
	case len(in.Message) > MaxMessage:
		return in, fmt.Errorf("%w: please keep the message under %d characters", ErrInvalid, MaxMessage)
	}
	return in, nil
}

func Create(ctx context.Context, pool *pgxpool.Pool, in New) (Prospect, error) {
	in, err := Validate(in)
	if err != nil {
		return Prospect{}, err
	}

	return scan(pool.QueryRow(ctx, `
		INSERT INTO prospects (unit_id, parent_name, parent_email, parent_phone,
			child_name, child_age, child_grade, child_school, message, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+columns,
		in.UnitID, in.ParentName, in.ParentEmail, in.ParentPhone,
		in.ChildName, in.ChildAge, in.ChildGrade, in.ChildSchool, in.Message,
		sourceOrForm(in.Source)))
}

// sourceOrForm defaults an unset Source to the form, so a caller that
// predates this field — or simply doesn't care — still stores something
// the CHECK accepts and the page can read.
func sourceOrForm(v string) string {
	if v == SourceLeader {
		return SourceLeader
	}
	return SourceForm
}

// AddByLeader records an enquiry someone entered by hand, and audits it
// against them.
//
// Separate from Create for two reasons, both about the difference
// between a family writing in and a name off a list.
//
// It is audited, where Create deliberately is not: a form submission
// has no actor — the whole point of a prospect is that nobody involved
// is a member — but a leader typing someone in is an act by a known
// person, and "who put this family on our list, and when" is the first
// question anyone asks when a name turns out not to want to be there.
//
// And it stores SourceLeader, which is what keeps the automatic reply
// away from them. Nobody at this address asked the unit for anything.
func AddByLeader(ctx context.Context, pool *pgxpool.Pool, in New, actorID string) (Prospect, error) {
	in.Source = SourceLeader
	p, err := Create(ctx, pool, in)
	if err != nil {
		return Prospect{}, err
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "prospect",
		EntityID:   p.ID,
		ActorID:    &actorID,
		Action:     "create",
		After:      p,
	})
	return p, nil
}

const columns = `id::text, unit_id::text, parent_name, parent_email, parent_phone,
	child_name, child_age, child_grade, child_school, message, status, category, source, notes,
	email_opt_out, opt_out_at, created_at, updated_at`

func scan(row pgx.Row) (Prospect, error) {
	var p Prospect
	err := row.Scan(&p.ID, &p.UnitID, &p.ParentName, &p.ParentEmail, &p.ParentPhone,
		&p.ChildName, &p.ChildAge, &p.ChildGrade, &p.ChildSchool, &p.Message,
		&p.Status, &p.Category, &p.Source, &p.Notes, &p.EmailOptOut, &p.OptOutAt, &p.CreatedAt, &p.UpdatedAt)
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
// closedStatusesSQL names this unit's closed statuses, for the queries
// that show only the enquiries still wanting attention — which ones
// those are is the unit's own choice now (prospect_labels.closed),
// where it used to be "joined or declined" for everyone.
//
// A subquery rather than a join so a prospect whose status matches no
// label still counts as open: it stays on the page to be dealt with,
// rather than disappearing from a list nobody would think to widen.
const closedStatusesSQL = `SELECT value FROM prospect_labels
	WHERE unit_id = $1 AND kind = 'status' AND closed`

// Order is how ListForUnit sorts its result.
type Order string

const (
	// OrderNewest is newest enquiry first — the default, and what
	// "who has come in since I last looked" wants.
	OrderNewest Order = "newest"
	// OrderStatus groups by where each family has got to, in the unit's
	// own workflow order rather than alphabetically by label: a leader
	// working through "everyone still at New enquiry" wants them
	// together and wants New before Contacted, which is what the list
	// on the statuses page already means.
	OrderStatus Order = "status"
)

// orderBySQL is the ORDER BY for one Order.
//
// The status ordering reads each row's position out of prospect_labels
// by correlated subquery rather than joining, because a status whose
// label has gone missing must still sort somewhere rather than dropping
// the row: COALESCE puts those last, where an unrecognised status is
// most likely to get looked at.
func orderBySQL(order Order) string {
	if order != OrderStatus {
		return ` ORDER BY created_at DESC`
	}
	return ` ORDER BY COALESCE((
			SELECT l.sort_order FROM prospect_labels l
			WHERE l.unit_id = prospects.unit_id AND l.kind = 'status' AND l.value = prospects.status
		), 2147483647), created_at DESC`
}

func ListForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string, openOnly bool, order Order) ([]Prospect, error) {
	sql := `SELECT ` + columns + ` FROM prospects WHERE unit_id = $1`
	if openOnly {
		sql += ` AND status NOT IN (` + closedStatusesSQL + `)`
	}
	sql += orderBySQL(order)

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
		`SELECT count(*) FROM prospects WHERE unit_id = $1 AND status NOT IN (`+closedStatusesSQL+`)`,
		unitID).Scan(&n)
	return n, err
}

// UpdateStatus records where a prospect has got to and what was said.
// Audited: this is a leader acting on someone's enquiry, and "we called
// them and they're coming Tuesday" is exactly the kind of thing the next
// leader needs to be able to find.
func UpdateStatus(ctx context.Context, pool *pgxpool.Pool, unitID, id, status, category, notes, actorID string) (Prospect, error) {
	// Checked against this unit's own live labels: the permitted set is
	// per-unit now, and both values arrive as form fields.
	known, err := LabelExists(ctx, pool, unitID, KindStatus, status)
	if err != nil {
		return Prospect{}, err
	}
	if !known {
		return Prospect{}, fmt.Errorf("%w: unknown status %q", ErrInvalid, status)
	}
	// "" is always allowed — it is what uncategorised means, and the
	// only way back to it once a category has been set.
	if category != "" {
		known, err := LabelExists(ctx, pool, unitID, KindCategory, category)
		if err != nil {
			return Prospect{}, err
		}
		if !known {
			return Prospect{}, fmt.Errorf("%w: unknown category %q", ErrInvalid, category)
		}
	}
	if len(notes) > MaxNotes {
		return Prospect{}, fmt.Errorf("%w: those notes are too long", ErrInvalid)
	}

	before, err := Get(ctx, pool, unitID, id)
	if err != nil {
		return Prospect{}, err
	}

	after, err := scan(pool.QueryRow(ctx, `
		UPDATE prospects SET status = $1, category = $2, notes = $3, updated_at = now()
		WHERE id = $4 AND unit_id = $5
		RETURNING `+columns, status, category, strings.TrimSpace(notes), id, unitID))
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

// UpdateDetails changes the contact details a leader typed or a family
// submitted — everything except where the enquiry has got to, which
// UpdateStatus owns.
//
// Split from UpdateStatus rather than folded into it because the two
// are different acts done at different moments: moving a family along
// happens constantly and from the list, while fixing a misheard email
// happens once. One form doing both would mean every status change
// re-submitting eight contact fields, and so every status change being
// a chance to blank one by accident.
//
// Audited with the before and after, because this is the one operation
// that can quietly change what a record says about a real person.
func UpdateDetails(ctx context.Context, pool *pgxpool.Pool, unitID, id string, in New, actorID string) (Prospect, error) {
	before, err := Get(ctx, pool, unitID, id)
	if err != nil {
		return Prospect{}, err
	}

	// Validated by the same function Create uses, so a record cannot be
	// edited into a state the form would have refused.
	in.UnitID = before.UnitID
	in, err = Validate(in)
	if err != nil {
		return Prospect{}, err
	}

	after, err := scan(pool.QueryRow(ctx, `
		UPDATE prospects SET parent_name = $1, parent_email = $2, parent_phone = $3,
			child_name = $4, child_age = $5, child_grade = $6, child_school = $7,
			message = $8, updated_at = now()
		WHERE id = $9 AND unit_id = $10
		RETURNING `+columns,
		in.ParentName, in.ParentEmail, in.ParentPhone,
		in.ChildName, in.ChildAge, in.ChildGrade, in.ChildSchool,
		in.Message, id, unitID))
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
