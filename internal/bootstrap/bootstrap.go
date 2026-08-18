// Package bootstrap creates the very first login so there's a way into the
// system after a fresh `-migrate -seed` — without it you'd have a working
// server and no way to reach any page behind RequireLogin. Meant to be run
// once per deployment (see cmd/server's -bootstrap-admin flag); safe to
// re-run since it checks for an existing user with the same email first.
package bootstrap

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/units"
)

// validRoles mirrors the member_role Postgres enum (internal/db/migrations
// /0001_init.sql). Kept as a Go-side allowlist so GrantRole can reject a
// typo'd role with a clear error instead of a raw Postgres constraint
// violation.
var validRoles = map[string]bool{
	"super_admin":           true,
	"cubmaster":             true,
	"den_leader":            true,
	"scoutmaster":           true,
	"assistant_scoutmaster": true,
	"senior_patrol_leader":  true,
	"patrol_leader":         true,
	"treasurer":             true,
	"parent":                true,
	"scout":                 true,
}

// AdminInput describes the account to create.
type AdminInput struct {
	Email     string
	Password  string
	FirstName string
	LastName  string
}

// CreateAdmin creates a family, a login (users), and a member for the given
// person, then assigns them the super_admin role in every unit currently in
// the database (Troop and Pack, as of Phase 1 — this naturally extends to
// any future unit rows without code changes). Returns the created family ID.
func CreateAdmin(ctx context.Context, pool *pgxpool.Pool, in AdminInput) (familyID string, err error) {
	email := auth.NormalizeEmail(in.Email)

	var existing string
	err = pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&existing)
	if err == nil {
		return "", fmt.Errorf("bootstrap: a user with email %s already exists (id %s) — nothing to do", email, existing)
	}

	passwordHash, err := auth.HashPassword(in.Password)
	if err != nil {
		return "", fmt.Errorf("bootstrap: hashing password: %w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("bootstrap: beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if already committed

	if err := tx.QueryRow(ctx,
		`INSERT INTO families (name) VALUES ($1) RETURNING id`,
		fmt.Sprintf("%s %s (admin)", in.FirstName, in.LastName),
	).Scan(&familyID); err != nil {
		return "", fmt.Errorf("bootstrap: creating family: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO users (family_id, email, password_hash) VALUES ($1, $2, $3)`,
		familyID, email, passwordHash,
	); err != nil {
		return "", fmt.Errorf("bootstrap: creating user: %w", err)
	}

	var memberID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO members (family_id, first_name, last_name, member_type) VALUES ($1, $2, $3, 'adult') RETURNING id`,
		familyID, in.FirstName, in.LastName,
	).Scan(&memberID); err != nil {
		return "", fmt.Errorf("bootstrap: creating member: %w", err)
	}

	rows, err := tx.Query(ctx, `SELECT id FROM units`)
	if err != nil {
		return "", fmt.Errorf("bootstrap: listing units: %w", err)
	}
	var unitIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", fmt.Errorf("bootstrap: scanning unit id: %w", err)
		}
		unitIDs = append(unitIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("bootstrap: iterating units: %w", err)
	}
	if len(unitIDs) == 0 {
		return "", fmt.Errorf("bootstrap: no units found — run `-seed` before `-bootstrap-admin`")
	}

	for _, unitID := range unitIDs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO role_assignments (member_id, unit_id, role) VALUES ($1, $2, 'super_admin')`,
			memberID, unitID,
		); err != nil {
			return "", fmt.Errorf("bootstrap: assigning super_admin role for unit %s: %w", unitID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("bootstrap: committing: %w", err)
	}

	return familyID, nil
}

// GrantRole gives an existing user's family a leadership (or other) role in
// a unit, unit-wide (no den/patrol). This exists for the case
// -bootstrap-admin doesn't cover: an account already has a role in one
// unit (e.g. the Troop) but needs one in another (e.g. the Pack) too —
// most commonly because that second unit didn't exist in the database yet
// when -bootstrap-admin originally ran, so its role-assignment loop never
// saw it. Safe to re-run: the underlying INSERT is ON CONFLICT DO NOTHING,
// since role_assignments has a UNIQUE(member_id, unit_id, sub_group_id,
// role) constraint.
//
// Once an account has any unit-wide leadership role in a unit, day-to-day
// role management for that unit can go back through /admin/roster — this
// is only needed to get the first foothold in a unit.
func GrantRole(ctx context.Context, pool *pgxpool.Pool, email, unitSlug, role string) error {
	if !validRoles[role] {
		return fmt.Errorf("bootstrap: %q is not a valid role (see internal/bootstrap.validRoles for the full list)", role)
	}

	user, found, err := auth.UserByEmail(ctx, pool, email)
	if err != nil {
		return fmt.Errorf("bootstrap: looking up user %s: %w", email, err)
	}
	if !found {
		return fmt.Errorf("bootstrap: no user with email %s — did you mean a different address, or does this account need to be created first (e.g. via /admin/roster on a unit it already has access to)?", email)
	}

	unit, found, err := units.BySlug(ctx, pool, unitSlug)
	if err != nil {
		return fmt.Errorf("bootstrap: looking up unit %s: %w", unitSlug, err)
	}
	if !found {
		return fmt.Errorf("bootstrap: no unit with slug %q — check the slug column in the units table (e.g. \"troop-47\", \"pack-47\")", unitSlug)
	}

	member, err := family.ActingMemberForFamilyInUnit(ctx, pool, user.FamilyID, unit.ID)
	if err != nil {
		return fmt.Errorf("bootstrap: finding a member of %s's family to grant the role to: %w", email, err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO role_assignments (member_id, unit_id, role) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		member.ID, unit.ID, role,
	); err != nil {
		return fmt.Errorf("bootstrap: granting %s the %s role in %s: %w", email, role, unit.Name, err)
	}

	return nil
}
