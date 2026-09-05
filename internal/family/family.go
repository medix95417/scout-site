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
// what the roster page actually needs to display. Email/HomePhone/
// CellPhone/Address are already filtered down to only what that member/
// family has actually chosen to release (see migration 0015's release_*
// columns) — same "never show anything nobody opted to share" rule
// internal/roster.DirectoryForUnit already applies for the Family
// Directory page. A member/family that's never released anything simply
// shows blank fields here, same as on that page.
type RosterEntry struct {
	Member
	// SubGroupName is the den/patrol this member belongs to, empty if
	// none. A member in more than one — a Den Leader who also has a Scout
	// in another den — gets them comma-joined, because this is one line
	// on a roster and they are one person.
	SubGroupName string
	// Roles is every role this member holds in the unit, on one entry.
	// These are slugs — the stable keys permission checks compare — so
	// anything shown to a person wants RoleLabels instead.
	Roles []string
	// RoleLabels is Roles as a person reads it. Left nil by the queries
	// in this package: labelling needs the unit's custom roles, and a
	// data-model package has no business loading display strings. The web
	// layer fills it in through units.RoleLabeler — see labelRoster.
	RoleLabels []string
	Email      string // "" if not released
	HomePhone  string // "" if not released
	CellPhone  string // "" if not released
	Address    string // "" if not released — family-level, so shared by every member in the family
}

// RosterForUnit lists every active member with at least one role
// assignment in the given unit — i.e. everyone actually part of that
// unit, not just anyone in the database. This is what leaders see on the
// roster page. Two members are deliberately left out even though they
// technically hold a role_assignment here:
//   - a deactivated member (members.active = false — see SetMemberActive
//     in internal/roster) is "off the rolls" by design, though their
//     record and role assignments are untouched and they show up again in
//     InactiveRosterForUnit below;
//   - a member whose only role in this unit is super_admin — a site-wide
//     configuration/ops grant, not a real membership — never belongs on a
//     family-facing roster. Anyone who holds super_admin alongside a real
//     membership role (e.g. a Scoutmaster who's also been granted
//     super_admin) still appears normally, tagged with their real role.
//
// One entry per member, never one per role. The GROUP BY deliberately
// does NOT include sub_groups.name: it used to, and that split anybody
// holding roles in different sub-groups — or one scoped role and one
// unscoped, which is every Den Leader who is also a parent — into a
// separate line per sub-group, each showing only the subset of their
// roles that belonged to it. A person is one line on a roster, so the
// den/patrol names are aggregated the same way the roles already were.
func RosterForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]RosterEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			members.id, members.family_id, members.first_name, members.last_name, members.member_type::text,
			COALESCE(string_agg(DISTINCT sub_groups.name, ', ' ORDER BY sub_groups.name), '') AS sub_groups,
			array_agg(DISTINCT role_assignments.role::text) AS roles,
			CASE WHEN members.release_email THEN COALESCE(members.email, '') ELSE '' END,
			CASE WHEN members.release_phone THEN COALESCE(members.home_phone, '') ELSE '' END,
			CASE WHEN members.release_phone THEN COALESCE(members.cell_phone, '') ELSE '' END,
			CASE WHEN families.release_address THEN COALESCE(families.address, '') ELSE '' END
		FROM role_assignments
		JOIN members ON members.id = role_assignments.member_id
		JOIN families ON families.id = members.family_id
		LEFT JOIN sub_groups ON sub_groups.id = role_assignments.sub_group_id
		WHERE role_assignments.unit_id = $1 AND members.active
		GROUP BY members.id, members.family_id, members.first_name, members.last_name, members.member_type,
			members.release_email, members.release_phone, members.email, members.home_phone, members.cell_phone,
			families.release_address, families.address
		HAVING NOT bool_and(role_assignments.role::text = 'super_admin')
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
			&e.Email, &e.HomePhone, &e.CellPhone, &e.Address,
		); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// InactiveRosterForUnit is RosterForUnit's mirror image: every deactivated
// member with at least one role assignment in the unit. Since a
// deactivated member no longer shows up in RosterForUnit, this is the only
// way the roster admin page can list them again in order to reactivate one
// (see SetMemberActive in internal/roster) — reactivating needs no repair
// beyond flipping the flag back, since their role assignments were never
// touched.
func InactiveRosterForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]RosterEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			members.id, members.family_id, members.first_name, members.last_name, members.member_type::text,
			COALESCE(string_agg(DISTINCT sub_groups.name, ', ' ORDER BY sub_groups.name), '') AS sub_groups,
			array_agg(DISTINCT role_assignments.role::text) AS roles
		FROM role_assignments
		JOIN members ON members.id = role_assignments.member_id
		LEFT JOIN sub_groups ON sub_groups.id = role_assignments.sub_group_id
		WHERE role_assignments.unit_id = $1 AND NOT members.active
		GROUP BY members.id, members.family_id, members.first_name, members.last_name, members.member_type
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

// RosterForSubGroup is RosterForUnit narrowed to one patrol/den — what a
// sub-group's own members-only page (see internal/web's GroupView) shows
// as its member list. Same active-only filtering as RosterForUnit (see its
// doc comment); no need for the super_admin exclusion here, since a
// super_admin role assignment is always whole-unit (sub_group_id NULL) and
// so never matches this query's sub_group_id filter to begin with.
func RosterForSubGroup(ctx context.Context, pool *pgxpool.Pool, subGroupID string) ([]RosterEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			members.id, members.family_id, members.first_name, members.last_name, members.member_type::text,
			COALESCE(sub_groups.name, ''),
			array_agg(DISTINCT role_assignments.role::text) AS roles
		FROM role_assignments
		JOIN members ON members.id = role_assignments.member_id
		LEFT JOIN sub_groups ON sub_groups.id = role_assignments.sub_group_id
		WHERE role_assignments.sub_group_id = $1 AND members.active
		GROUP BY members.id, members.family_id, members.first_name, members.last_name, members.member_type, sub_groups.name
		ORDER BY (members.member_type = 'youth'), members.last_name, members.first_name
	`, subGroupID)
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
