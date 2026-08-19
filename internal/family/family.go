// Package family manages households, the members within them (both adults
// and youth), and how they relate to units.
package family

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Member struct {
	ID         string
	FamilyID   string
	FirstName  string
	LastName   string
	MemberType string // "adult" | "youth"
}

// MembersForFamily lists everyone (adults and youth) in a family, ordered
// adults-first then by first name — a small kindness for the roster UI.
func MembersForFamily(ctx context.Context, pool *pgxpool.Pool, familyID string) ([]Member, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, family_id, first_name, last_name, member_type::text
		FROM members
		WHERE family_id = $1
		ORDER BY (member_type = 'youth'), first_name
	`, familyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.FamilyID, &m.FirstName, &m.LastName, &m.MemberType); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// GetMember fetches a single member by ID — the building block for
// resolving an individual member login's own identity (see
// internal/auth.User.MemberID and internal/web's actingMember helper),
// where the acting member is already known by ID rather than needing to be
// derived from a family via ActingMemberForFamilyInUnit's heuristic.
func GetMember(ctx context.Context, pool *pgxpool.Pool, memberID string) (Member, bool, error) {
	var m Member
	err := pool.QueryRow(ctx, `
		SELECT id, family_id, first_name, last_name, member_type::text
		FROM members WHERE id = $1
	`, memberID).Scan(&m.ID, &m.FamilyID, &m.FirstName, &m.LastName, &m.MemberType)
	if err != nil {
		return Member{}, false, nil //nolint:nilerr // "not found" is a normal, expected outcome
	}
	return m, true, nil
}

// MemberBelongsToFamily reports whether memberID is a member of familyID —
// used by Phase 2's treasury views to let a family see/manage their own
// Scout's individual ledger account (and request trip-fund transfers for
// them) without needing the Treasurer role, while still blocking access
// to any other family's Scout's account.
func MemberBelongsToFamily(ctx context.Context, pool *pgxpool.Pool, memberID, familyID string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM members WHERE id = $1 AND family_id = $2)`,
		memberID, familyID,
	).Scan(&exists)
	return exists, err
}

// ActingMemberForFamilyInUnit picks which of a family's members should be
// treated as "the actor" for writes (event creation, RSVPs, etc.) within a
// unit — the member who actually holds a role there, preferring adults.
// Falls back to any member of the family if none hold a role yet (e.g. a
// brand-new family account that hasn't been assigned a role by a leader).
//
// This is a Phase 1 simplification: role assignments belong to individual
// members, but login is at the family level, so someone has to be chosen to
// attribute the action to. Worth revisiting if a family ever needs two
// members acting independently in the same unit at the same time (e.g. an
// SPL and their ASM parent in the same troop) — Phase 1 assumes that's rare
// enough to not block on.
func ActingMemberForFamilyInUnit(ctx context.Context, pool *pgxpool.Pool, familyID, unitID string) (Member, error) {
	var m Member
	err := pool.QueryRow(ctx, `
		SELECT members.id, members.family_id, members.first_name, members.last_name, members.member_type::text
		FROM members
		LEFT JOIN role_assignments ON role_assignments.member_id = members.id AND role_assignments.unit_id = $2
		WHERE members.family_id = $1
		ORDER BY (role_assignments.id IS NULL), (members.member_type = 'youth'), members.first_name
		LIMIT 1
	`, familyID, unitID).Scan(&m.ID, &m.FamilyID, &m.FirstName, &m.LastName, &m.MemberType)
	return m, err
}

// RosterEntry is a member plus the roles they hold in the current unit —
// what the roster page actually needs to display.
type RosterEntry struct {
	Member
	SubGroupName string // den/patrol name, if assigned; empty if unit-wide
	Roles        []string
}

// RosterForUnit lists every member with at least one role assignment in the
// given unit — i.e. everyone actually part of that unit, not just anyone in
// the database. This is what leaders see on the roster page.
func RosterForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]RosterEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			members.id, members.family_id, members.first_name, members.last_name, members.member_type::text,
			COALESCE(sub_groups.name, ''),
			array_agg(DISTINCT role_assignments.role::text) AS roles
		FROM role_assignments
		JOIN members ON members.id = role_assignments.member_id
		LEFT JOIN sub_groups ON sub_groups.id = role_assignments.sub_group_id
		WHERE role_assignments.unit_id = $1
		GROUP BY members.id, members.family_id, members.first_name, members.last_name, members.member_type, sub_groups.name
		ORDER BY (members.member_type = 'youth'), members.last_name, members.first_name
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []RosterEntry
	for rows.Next() {
		var e RosterEntry
		if err := rows.Scan(
			&e.ID, &e.FamilyID, &e.FirstName, &e.LastName, &e.MemberType,
			&e.SubGroupName, &e.Roles,
		); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
