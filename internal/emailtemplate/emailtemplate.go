// Package emailtemplate stores email bodies a unit wrote and wants back
// next time — a recruiting letter it sends every August, a newsletter
// layout it has settled on.
//
// Distinct from newsletter.StarterTemplates, which are code-defined
// starting points shipped with the site and identical for every unit.
// These are the unit's own, and saving one under a name that already
// exists replaces it, because that is what "save this template" means to
// the person clicking it.
//
// Data model and rules only, no HTTP — same separation as every other
// business-logic package here.
package emailtemplate

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

// Kinds. Which composer offers a template — a recruiting letter and a
// pack newsletter are not interchangeable, and one shared list would make
// both harder to use.
const (
	KindProspect   = "prospect"
	KindNewsletter = "newsletter"
)

// MaxName and MaxSubject bound the two short fields. The body is
// deliberately unbounded here: it is HTML a leader composed in the
// editor, and the request-body cap in internal/csrf is what stops it
// being unreasonable.
const (
	MaxName    = 120
	MaxSubject = 200
)

// Template is one saved body.
type Template struct {
	ID        string
	UnitID    string
	Kind      string
	Name      string
	Subject   string
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ErrInvalid is returned for input the person can fix.
var ErrInvalid = errors.New("emailtemplate: invalid template")

// ErrNotFound covers both "no such template" and "not this unit's".
var ErrNotFound = errors.New("emailtemplate: no such template in this unit")

const columns = `id::text, unit_id::text, kind, name, subject, body, created_at, updated_at`

func scan(row interface{ Scan(...any) error }) (Template, error) {
	var t Template
	err := row.Scan(&t.ID, &t.UnitID, &t.Kind, &t.Name, &t.Subject, &t.Body, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func validKind(kind string) bool { return kind == KindProspect || kind == KindNewsletter }

// Save stores a template, replacing any this unit already has under the
// same name and kind.
//
// Upsert rather than "create, and error if it exists" because the button
// says "Save as template" and is clicked from a composer: being told
// "that name is taken" at that moment is an obstacle, not information.
func Save(ctx context.Context, pool *pgxpool.Pool, unitID, kind, name, subject, body, actorID string) (Template, error) {
	if !validKind(kind) {
		return Template{}, fmt.Errorf("%w: unknown kind %q", ErrInvalid, kind)
	}
	name = strings.TrimSpace(name)
	subject = strings.TrimSpace(subject)
	switch {
	case name == "":
		return Template{}, fmt.Errorf("%w: give the template a name so you can find it again", ErrInvalid)
	case len(name) > MaxName:
		return Template{}, fmt.Errorf("%w: that name is too long", ErrInvalid)
	case len(subject) > MaxSubject:
		return Template{}, fmt.Errorf("%w: that subject line is too long", ErrInvalid)
	}

	t, err := scan(pool.QueryRow(ctx, `
		INSERT INTO email_templates (unit_id, kind, name, subject, body, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (unit_id, kind, name)
		DO UPDATE SET subject = EXCLUDED.subject, body = EXCLUDED.body, updated_at = now()
		RETURNING `+columns,
		unitID, kind, name, subject, body, actorID))
	if err != nil {
		return Template{}, err
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "email_template", EntityID: t.ID, ActorID: &actorID, Action: "save",
		After: map[string]any{"kind": t.Kind, "name": t.Name, "subject": t.Subject},
	})
	return t, nil
}

// ListForUnit returns a unit's saved templates of one kind, by name.
func ListForUnit(ctx context.Context, pool *pgxpool.Pool, unitID, kind string) ([]Template, error) {
	if !validKind(kind) {
		return nil, fmt.Errorf("%w: unknown kind %q", ErrInvalid, kind)
	}
	rows, err := pool.Query(ctx,
		`SELECT `+columns+` FROM email_templates WHERE unit_id = $1 AND kind = $2 ORDER BY name`, unitID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Template
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Get loads one, scoped to its unit.
func Get(ctx context.Context, pool *pgxpool.Pool, id, unitID string) (Template, error) {
	t, err := scan(pool.QueryRow(ctx,
		`SELECT `+columns+` FROM email_templates WHERE id = $1 AND unit_id = $2`, id, unitID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, ErrNotFound
	}
	return t, err
}

// Delete removes a saved template. Nothing else references it — a
// campaign or newsletter composed from one holds its own copy of the
// body — so this is safe at any time.
func Delete(ctx context.Context, pool *pgxpool.Pool, id, unitID, actorID string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM email_templates WHERE id = $1 AND unit_id = $2`, id, unitID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "email_template", EntityID: id, ActorID: &actorID, Action: "delete",
	})
	return nil
}
