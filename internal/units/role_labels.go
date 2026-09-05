package units

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Turning a role slug into what a person reads.
//
// `role_assignments.role` holds a slug — "den_leader", "assistant_scoutmaster",
// "committee_chair" — because it has to be a stable key: it is compared in
// permission checks, matched against custom_roles, and written by the CSV
// import. None of that wants a display string that changes when somebody
// renames a role.
//
// The consequence is that every page listing somebody's roles was showing
// the key: a roster reading "den_leader" and "assistant_scoutmaster"
// rather than "Den Leader" and "Assistant Scoutmaster". This is the one
// place that translates, so no page has to know the difference between a
// built-in role (labelled in code, see systemRoleOrder) and a custom one
// (labelled in the database by whoever created it).

// RoleLabeler resolves slugs for one unit. Build it once per page rather
// than per member: it holds that unit's custom-role labels, which would
// otherwise be a query for every row of the roster.
type RoleLabeler struct {
	custom map[string]string
}

// NewRoleLabeler loads a unit's custom-role labels.
//
// A labeler for a unit with no custom roles is still perfectly usable —
// it just resolves built-ins — so an error here is worth reporting but
// never worth failing a page over; see the zero value below.
func NewRoleLabeler(ctx context.Context, pool *pgxpool.Pool, unitID string) (RoleLabeler, error) {
	l := RoleLabeler{custom: map[string]string{}}
	rows, err := pool.Query(ctx, `SELECT slug, label FROM custom_roles WHERE unit_id = $1`, unitID)
	if err != nil {
		return l, err
	}
	defer rows.Close()
	for rows.Next() {
		var slug, label string
		if err := rows.Scan(&slug, &label); err != nil {
			return l, err
		}
		l.custom[slug] = label
	}
	return l, rows.Err()
}

// Label is what to show for one slug.
//
// The fallback matters more than it looks: a role assignment can outlive
// the custom role that defined it (deleting a custom role deliberately
// leaves the assignments in place — see roster.DeleteCustomRole), so a
// slug with no definition anywhere is a real, reachable case. Prettifying
// it beats showing "committee_chair" to a parent, and beats showing
// nothing at all.
//
// Safe on the zero value, so a caller whose labeler failed to load still
// gets built-in roles labelled correctly and custom ones prettified.
func (l RoleLabeler) Label(slug string) string {
	if label, ok := l.custom[slug]; ok {
		return label
	}
	if label := SystemRoleLabel(slug); label != slug {
		return label
	}
	return prettifySlug(slug)
}

// Labels resolves a whole list, preserving order.
func (l RoleLabeler) Labels(slugs []string) []string {
	if len(slugs) == 0 {
		return nil
	}
	out := make([]string, 0, len(slugs))
	for _, s := range slugs {
		out = append(out, l.Label(s))
	}
	return out
}

// prettifySlug turns "committee_chair" into "Committee Chair" — the last
// resort for a slug nothing defines.
func prettifySlug(slug string) string {
	if slug == "" {
		return ""
	}
	words := strings.FieldsFunc(slug, func(r rune) bool { return r == '_' || r == '-' })
	for i, w := range words {
		// Not strings.Title (deprecated, and wrong for anything but
		// ASCII): only the first rune needs raising, and the rest is left
		// exactly as stored.
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
