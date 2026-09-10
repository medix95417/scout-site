package roster

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/units"
)

// The privilege ceiling: what a roster editor may hand out, and whose
// login they may touch.
//
// Roster editing was gated on two questions — does this leader hold the
// content-editing capability, and is this member inside their scope —
// and neither one asks how much access the other person has. A unit-wide
// leader's scope covers everyone in the unit, the Admin included, so the
// same permission that lets an Assistant Scoutmaster fix a Scout's
// surname let them reset the Admin's password, mint a fresh login for
// the Admin's member record under their own email address (which meets
// no two-factor prompt, because the enrollment belongs to the Admin's
// own login), or simply assign themselves Treasurer. Each of those is one
// ordinary form on the roster page.
//
// The ceiling is one rule applied in both directions: a leader may grant
// only capabilities they hold, and may administer only a login that
// holds nothing they do not. units.Capabilities.Covers is the rule; the
// functions here gather the capability sets it compares.

// RoleCapabilities resolves what one role grants in a unit — a built-in
// role with the unit's overrides applied, or a custom role's stored list.
// Unknown roles grant nothing, which the ceiling then covers trivially;
// whether such a role may be assigned at all is AllowedRoles' question.
func RoleCapabilities(ctx context.Context, pool *pgxpool.Pool, unitID, role string) (units.Capabilities, error) {
	return units.CapabilitiesForRoles(ctx, pool, unitID, []string{role})
}

// MemberCapabilitiesAcrossUnits is the union of everything one member
// holds in every unit — the ceiling for actions on that member's own
// record or individual login.
//
// Across units, not in the current one, because the things this guards
// are not per-unit. A member's individual login signs in to both
// subdomains at once; deactivating a member row silences their roles
// everywhere. A Troop leader who could not touch the Troop's Admin must
// not be able to touch the Pack's Admin either, just because that Admin
// happens to hold only a parent role on the Troop side.
//
// Inactive assignments count. This is a ceiling, and a member who is
// deactivated today can be reactivated tomorrow; measuring what they
// would hold errs towards refusing, which is the right way to be wrong.
func MemberCapabilitiesAcrossUnits(ctx context.Context, pool *pgxpool.Pool, memberID string) (units.Capabilities, error) {
	return capabilitiesAcrossUnits(ctx, pool, `
		SELECT unit_id, role::text FROM role_assignments WHERE member_id = $1
	`, memberID)
}

// FamilyCapabilitiesAcrossUnits is the union of everything any member of
// a family holds in every unit — the ceiling for resetting the family's
// shared login, which carries all of it.
func FamilyCapabilitiesAcrossUnits(ctx context.Context, pool *pgxpool.Pool, familyID string) (units.Capabilities, error) {
	return capabilitiesAcrossUnits(ctx, pool, `
		SELECT role_assignments.unit_id, role_assignments.role::text
		FROM role_assignments
		JOIN members ON members.id = role_assignments.member_id
		WHERE members.family_id = $1
	`, familyID)
}

// capabilitiesAcrossUnits runs a (unit_id, role) query and resolves each
// unit's roles separately — a custom role means whatever the unit that
// defined it says, so roles cannot be pooled before resolving.
func capabilitiesAcrossUnits(ctx context.Context, pool *pgxpool.Pool, query, arg string) (units.Capabilities, error) {
	rows, err := pool.Query(ctx, query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byUnit := map[string][]string{}
	for rows.Next() {
		var unitID, role string
		if err := rows.Scan(&unitID, &role); err != nil {
			return nil, err
		}
		byUnit[unitID] = append(byUnit[unitID], role)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	held := units.Capabilities{}
	for unitID, roles := range byUnit {
		caps, err := units.CapabilitiesForRoles(ctx, pool, unitID, roles)
		if err != nil {
			return nil, err
		}
		for c, ok := range caps {
			if ok {
				held[c] = true
			}
		}
	}
	return held, nil
}
