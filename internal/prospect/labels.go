package prospect

// Per-unit prospect statuses and categories.
//
// Two lists in one table, told apart by kind — see migration 0048 for
// the schema, and for why retiring rather than deleting is how a label
// leaves circulation.
//
// A status is where a family has got to, and moves as they progress. A
// category is how the unit files them — which program they asked about,
// how they heard of the unit — and doesn't move. Separate fields
// because they answer different questions: one dropdown carrying both
// would make a leader choose which of the two they wanted to sort by.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

// The two kinds of label. Both are stored in prospect_labels and edited
// through the same handlers; only Closed and the guards below care
// which is which.
const (
	KindStatus   = "status"
	KindCategory = "category"
)

// MaxLabelText is the longest display text a label may have, matching
// the CHECK in migration 0048.
const MaxLabelText = 60

// maxLabelsPerKind caps how many a unit can create. Not a storage
// concern — a dropdown of two hundred statuses is a worse workflow than
// the five it replaced, and the cap is the only thing that says so.
const maxLabelsPerKind = 40

// Label is one entry in a unit's status or category list.
type Label struct {
	ID        string
	UnitID    string
	Kind      string
	Value     string // stable identifier stored on the prospect; never changes
	Label     string // what a leader reads; renameable
	Closed    bool   // status only: a prospect here has been dealt with
	SortOrder int
	RetiredAt *time.Time // non-nil once withdrawn from the pickers
}

// Retired reports whether this label is still offered for new use.
func (l Label) Retired() bool { return l.RetiredAt != nil }

// defaultStatuses is the workflow a unit starts with — the five values
// that were the prospect_status enum before migration 0048. Kept here
// as well as in that migration and seed.sql because EnsureDefaults
// needs them at runtime for a unit that somehow has none, and an empty
// status dropdown is a page a leader cannot use at all.
var defaultStatuses = []Label{
	{Value: "new", Label: "New enquiry", SortOrder: 1},
	{Value: "contacted", Label: "Contacted", SortOrder: 2},
	{Value: "visited", Label: "Visited a meeting", SortOrder: 3},
	{Value: "joined", Label: "Joined", Closed: true, SortOrder: 4},
	{Value: "declined", Label: "Not joining", Closed: true, SortOrder: 5},
}

// DefaultStatuses returns the workflow a unit starts with, as a copy.
//
// Exported because a unit's real list lives in the database and a test
// that only needs "some plausible statuses" shouldn't have to stand one
// up — and because it is the answer to "what will a brand-new unit
// see", which is worth being able to ask in one call.
func DefaultStatuses() []Label {
	out := make([]Label, len(defaultStatuses))
	copy(out, defaultStatuses)
	for i := range out {
		out[i].Kind = KindStatus
	}
	return out
}

// StatusNewValue is the status a new enquiry lands on, and the column
// default in the schema. It is the one label that cannot be retired:
// the public form writes it without asking anyone, so a unit that
// retired it would be filing enquiries under something its own pickers
// no longer offer.
const StatusNewValue = "new"

var (
	// ErrLabelExists is returned when a name would collide with one the
	// unit already has, retired ones included — reusing the slug of a
	// retired label would silently adopt every old row that used it.
	ErrLabelExists = errors.New("prospect: a label with that name already exists")
	// ErrLabelNotFound is the cross-tenant-safe miss: not this unit's,
	// or not there at all, without saying which.
	ErrLabelNotFound = errors.New("prospect: label not found in this unit")
	// ErrProtectedLabel is returned for the two things a unit must not
	// do to itself: retire the status new enquiries land on, or retire
	// the last one it could move a prospect to.
	ErrProtectedLabel = errors.New("prospect: that label cannot be retired")
)

const labelColumns = `id::text, unit_id::text, kind, value, label, closed, sort_order, retired_at`

func scanLabel(row interface{ Scan(...any) error }) (Label, error) {
	var l Label
	err := row.Scan(&l.ID, &l.UnitID, &l.Kind, &l.Value, &l.Label, &l.Closed, &l.SortOrder, &l.RetiredAt)
	return l, err
}

// ListLabels returns one kind of label for a unit in display order.
// Retired ones come back only when asked for: the pickers want the
// live list, while the management page and anything rendering a
// historical value want all of them.
func ListLabels(ctx context.Context, pool *pgxpool.Pool, unitID, kind string, includeRetired bool) ([]Label, error) {
	sql := `SELECT ` + labelColumns + ` FROM prospect_labels WHERE unit_id = $1 AND kind = $2`
	if !includeRetired {
		sql += ` AND retired_at IS NULL`
	}
	sql += ` ORDER BY sort_order, label`

	rows, err := pool.Query(ctx, sql, unitID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Label{}
	for rows.Next() {
		l, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LabelText maps value to display text for one kind, retired included.
// Used wherever a stored value has to be rendered — a prospect's
// current status, a campaign's audience — since those may name a label
// that has since been withdrawn.
func LabelText(ctx context.Context, pool *pgxpool.Pool, unitID, kind string) (map[string]string, error) {
	all, err := ListLabels(ctx, pool, unitID, kind, true)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(all))
	for _, l := range all {
		m[l.Value] = l.Label
	}
	return m, nil
}

// EnsureDefaults gives a unit the default statuses if it has none at
// all. Migration 0048 and seed.sql both do this already; this is the
// belt to their braces, because the failure it prevents — a status
// dropdown with nothing in it — makes the prospects page unusable and
// would be a puzzling thing to debug from a screenshot.
func EnsureDefaults(ctx context.Context, pool *pgxpool.Pool, unitID string) error {
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM prospect_labels WHERE unit_id = $1 AND kind = $2`,
		unitID, KindStatus).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	for _, d := range defaultStatuses {
		if _, err := pool.Exec(ctx, `
			INSERT INTO prospect_labels (unit_id, kind, value, label, closed, sort_order)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (unit_id, kind, value) DO NOTHING`,
			unitID, KindStatus, d.Value, d.Label, d.Closed, d.SortOrder); err != nil {
			return err
		}
	}
	return nil
}

// LabelExists reports whether a value is one this unit may assign right
// now. Retired labels are deliberately excluded: an existing prospect
// keeps a retired status, but nothing new may be moved to one.
//
// This replaced a check against a hardcoded list. It has to hit the
// database because the permitted set is now per-unit, and because the
// value arrives as a form field — which can say anything.
func LabelExists(ctx context.Context, pool *pgxpool.Pool, unitID, kind, value string) (bool, error) {
	var ok bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM prospect_labels
			WHERE unit_id = $1 AND kind = $2 AND value = $3 AND retired_at IS NULL
		)`, unitID, kind, value).Scan(&ok)
	return ok, err
}

// CreateLabel adds one to the end of a unit's list.
//
// The value is derived from the text once, here, and never changes
// again — renaming has to leave every prospect and campaign that
// already refers to it pointing at the same row. That is also why a
// collision with a retired label is refused rather than reusing it:
// taking over a withdrawn label's value would silently adopt all its
// old rows.
func CreateLabel(ctx context.Context, pool *pgxpool.Pool, unitID, kind, text string, closed bool, actorID string) (Label, error) {
	if kind != KindStatus && kind != KindCategory {
		return Label{}, fmt.Errorf("%w: unknown kind %q", ErrInvalid, kind)
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return Label{}, fmt.Errorf("%w: a name is required", ErrInvalid)
	}
	if len(text) > MaxLabelText {
		return Label{}, fmt.Errorf("%w: that name is too long", ErrInvalid)
	}
	// A category is never "closed" — that is a status's idea of having
	// been dealt with, and storing true here would be meaningless state
	// waiting to be read by mistake.
	if kind == KindCategory {
		closed = false
	}

	existing, err := ListLabels(ctx, pool, unitID, kind, true)
	if err != nil {
		return Label{}, err
	}
	if len(existing) >= maxLabelsPerKind {
		return Label{}, fmt.Errorf("%w: a unit can have at most %d of these", ErrInvalid, maxLabelsPerKind)
	}
	taken := make(map[string]bool, len(existing))
	next := 1
	for _, l := range existing {
		taken[l.Value] = true
		if strings.EqualFold(l.Label, text) {
			return Label{}, ErrLabelExists
		}
		if l.SortOrder >= next {
			next = l.SortOrder + 1
		}
	}
	value := uniqueSlug(text, taken)
	if value == "" {
		return Label{}, fmt.Errorf("%w: that name has no letters or digits in it", ErrInvalid)
	}

	l, err := scanLabel(pool.QueryRow(ctx, `
		INSERT INTO prospect_labels (unit_id, kind, value, label, closed, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+labelColumns,
		unitID, kind, value, text, closed, next))
	if err != nil {
		return Label{}, err
	}
	logLabel(ctx, pool, actorID, "create", Label{}, l)
	return l, nil
}

// RenameLabel changes the display text, and only that. The value stays
// put, so everything already filed under it follows the new name.
func RenameLabel(ctx context.Context, pool *pgxpool.Pool, unitID, id, text, actorID string) (Label, error) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return Label{}, fmt.Errorf("%w: a name is required", ErrInvalid)
	}
	if len(text) > MaxLabelText {
		return Label{}, fmt.Errorf("%w: that name is too long", ErrInvalid)
	}

	before, err := GetLabel(ctx, pool, unitID, id)
	if err != nil {
		return Label{}, err
	}
	siblings, err := ListLabels(ctx, pool, unitID, before.Kind, true)
	if err != nil {
		return Label{}, err
	}
	for _, l := range siblings {
		if l.ID != id && strings.EqualFold(l.Label, text) {
			return Label{}, ErrLabelExists
		}
	}

	after, err := scanLabel(pool.QueryRow(ctx, `
		UPDATE prospect_labels SET label = $1 WHERE id = $2 AND unit_id = $3
		RETURNING `+labelColumns, text, id, unitID))
	if err != nil {
		return Label{}, err
	}
	logLabel(ctx, pool, actorID, "update", before, after)
	return after, nil
}

// SetLabelClosed sets whether prospects at this status count as dealt
// with, and so drop out of the default list. Meaningless for a
// category, which is why it refuses one rather than storing a flag
// nothing reads.
func SetLabelClosed(ctx context.Context, pool *pgxpool.Pool, unitID, id string, closed bool, actorID string) (Label, error) {
	before, err := GetLabel(ctx, pool, unitID, id)
	if err != nil {
		return Label{}, err
	}
	if before.Kind != KindStatus {
		return Label{}, fmt.Errorf("%w: only a status can be open or closed", ErrInvalid)
	}
	after, err := scanLabel(pool.QueryRow(ctx, `
		UPDATE prospect_labels SET closed = $1 WHERE id = $2 AND unit_id = $3
		RETURNING `+labelColumns, closed, id, unitID))
	if err != nil {
		return Label{}, err
	}
	logLabel(ctx, pool, actorID, "update", before, after)
	return after, nil
}

// SetLabelRetired withdraws a label from the pickers, or puts it back.
//
// Retiring is the only way a label leaves, because deleting one would
// strand the prospects filed under it and the campaigns that named it
// in their audience — both store the value, and a row that no longer
// exists reads as a bare slug or as nothing at all. A retired label
// keeps its text and stops being offered.
func SetLabelRetired(ctx context.Context, pool *pgxpool.Pool, unitID, id string, retired bool, actorID string) (Label, error) {
	before, err := GetLabel(ctx, pool, unitID, id)
	if err != nil {
		return Label{}, err
	}
	if retired && before.Kind == KindStatus {
		if before.Value == StatusNewValue {
			return Label{}, fmt.Errorf("%w: new enquiries arrive at %q, so it has to stay", ErrProtectedLabel, before.Label)
		}
		live, err := ListLabels(ctx, pool, unitID, KindStatus, false)
		if err != nil {
			return Label{}, err
		}
		if len(live) <= 1 {
			return Label{}, fmt.Errorf("%w: a unit needs at least one status to put a prospect at", ErrProtectedLabel)
		}
	}

	var at *time.Time
	if retired {
		now := time.Now()
		at = &now
	}
	after, err := scanLabel(pool.QueryRow(ctx, `
		UPDATE prospect_labels SET retired_at = $1 WHERE id = $2 AND unit_id = $3
		RETURNING `+labelColumns, at, id, unitID))
	if err != nil {
		return Label{}, err
	}
	logLabel(ctx, pool, actorID, "update", before, after)
	return after, nil
}

// MoveLabel shifts one place up or down its list, swapping places with
// its neighbour. Up/down rather than drag-and-drop because this site
// has no JavaScript build step, and a pair of buttons works on a phone
// and with a keyboard without one.
func MoveLabel(ctx context.Context, pool *pgxpool.Pool, unitID, id string, up bool, actorID string) error {
	target, err := GetLabel(ctx, pool, unitID, id)
	if err != nil {
		return err
	}
	// Ordered over the whole kind, retired ones included, so moving
	// through a retired neighbour doesn't skip a position and leave the
	// two lists disagreeing about the order.
	all, err := ListLabels(ctx, pool, unitID, target.Kind, true)
	if err != nil {
		return err
	}
	at := -1
	for i, l := range all {
		if l.ID == id {
			at = i
			break
		}
	}
	if at < 0 {
		return ErrLabelNotFound
	}
	swap := at + 1
	if up {
		swap = at - 1
	}
	if swap < 0 || swap >= len(all) {
		return nil // already at the end; not an error, just nothing to do
	}

	// Rewritten from the list's positions rather than by swapping the
	// two stored numbers, because rows seeded at the same sort_order
	// would otherwise swap to no visible effect.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	all[at], all[swap] = all[swap], all[at]
	for i, l := range all {
		if _, err := tx.Exec(ctx,
			`UPDATE prospect_labels SET sort_order = $1 WHERE id = $2 AND unit_id = $3`,
			i+1, l.ID, unitID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	logLabel(ctx, pool, actorID, "update", target, all[swap])
	return nil
}

// GetLabel loads one, scoped to its unit.
func GetLabel(ctx context.Context, pool *pgxpool.Pool, unitID, id string) (Label, error) {
	l, err := scanLabel(pool.QueryRow(ctx,
		`SELECT `+labelColumns+` FROM prospect_labels WHERE id = $1 AND unit_id = $2`, id, unitID))
	if err != nil {
		return Label{}, ErrLabelNotFound
	}
	return l, nil
}

func logLabel(ctx context.Context, pool *pgxpool.Pool, actorID, action string, before, after Label) {
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "prospect_label",
		EntityID:   after.ID,
		ActorID:    &actorID,
		Action:     action,
		Before:     before,
		After:      after,
	})
}

// uniqueSlug turns display text into the stable identifier stored on
// the prospect, avoiding anything the unit already holds.
//
// Lowercase letters, digits and dashes only — the same shape the
// column's CHECK enforces, so a name written in another script, or in
// nothing but punctuation, is refused by the caller rather than
// producing a row Postgres will reject.
func uniqueSlug(text string, taken map[string]bool) string {
	base := slugify(text)
	if base == "" {
		return ""
	}
	if !taken[base] {
		return base
	}
	for n := 2; n < 100; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if len(candidate) > 40 {
			candidate = fmt.Sprintf("%s-%d", base[:40-len(fmt.Sprint(n))-1], n)
		}
		if !taken[candidate] {
			return candidate
		}
	}
	return ""
}

func slugify(text string) string {
	var b strings.Builder
	lastDash := true // leading dashes are not allowed by the CHECK
	for _, r := range strings.ToLower(text) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteRune('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	return out
}

// CountByLabel counts prospects per status and per category value, so
// the management page can say what retiring one would affect before a
// leader does it. Keyed by value; a category of "" is counted under
// "none", which is also how the list page names that filter.
func CountByLabel(ctx context.Context, pool *pgxpool.Pool, unitID string) (map[string]int, error) {
	out := map[string]int{}
	rows, err := pool.Query(ctx, `
		SELECT status, CASE WHEN category = '' THEN 'none' ELSE category END, count(*)
		FROM prospects WHERE unit_id = $1 GROUP BY 1, 2`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var status, category string
		var n int
		if err := rows.Scan(&status, &category, &n); err != nil {
			return nil, err
		}
		out[status] += n
		out[category] += n
	}
	return out, rows.Err()
}
