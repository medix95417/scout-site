// Package roster is the leader-facing, self-service replacement for the
// direct-SQL-insert roster management called out as a Phase 1 gap in
// README.md. It lets a leader — scoped to what the permission table in the
// requirements doc actually grants them — add families, add members to
// existing families, assign/remove roles, and manage dens/patrols, all
// through the web UI.
//
// Scope matters here in a way it hasn't for calendar/content editing so
// far: a Den Leader should only manage their own den, not the whole Pack's
// roster, per "Den Leader (their den)" in the original requirements. See
// Scope and ScopeForFamily below.
package roster

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/ledger"
	"github.com/47-yonkers/scout-site/internal/units"
)

// --- Family directory ---------------------------------------------------

// DirectoryMember is one member's row in the family directory (see
// DirectoryForUnit) — Email/HomePhone/CellPhone are already filtered down
// to what that member chose to release; nothing here needs a second
// permission check by the caller.
type DirectoryMember struct {
	ID        string
	FirstName string
	LastName  string
	Email     string // "" if not released
	HomePhone string // "" if not released
	CellPhone string // "" if not released
}

// DirectoryEntry is one family's row in the directory — Address is
// already filtered down to whether that family chose to release it.
type DirectoryEntry struct {
	FamilyID   string
	FamilyName string
	Address    string // "" if not released
	Members    []DirectoryMember
}

// DirectoryForUnit returns every family with at least one member on this
// unit's roster, each decorated with only the contact fields they've
// actually opted to release (see migration 0015's release_* columns) —
// what the members-only "Family Directory" page shows. A family/member
// that's never released anything simply shows blank fields, same as if
// they'd never entered them — this never leaks anything nobody chose to
// share.
//
// Excludes a member whose only role in this unit is super_admin — that
// capability is deliberately narrower and more "site operator" than a
// community leadership role (see units.IsSuperAdmin's own doc comment):
// it's how an ops/bootstrap login (e.g. internal/bootstrap.CreateAdmin,
// or internal/demoseed's "Rivera Family (admin)" persona) gets set up,
// not necessarily a family actually in the program. A member holding
// super_admin alongside a real role (Scoutmaster, Treasurer, parent,
// etc.) still shows normally — only a member with no OTHER role is
// treated as a pure technical account and left out.
func DirectoryForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]DirectoryEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			families.id, families.name,
			CASE WHEN families.release_address THEN COALESCE(families.address, '') ELSE '' END,
			members.id, members.first_name, members.last_name,
			CASE WHEN members.release_email THEN COALESCE(members.email, '') ELSE '' END,
			CASE WHEN members.release_phone THEN COALESCE(members.home_phone, '') ELSE '' END,
			CASE WHEN members.release_phone THEN COALESCE(members.cell_phone, '') ELSE '' END
		FROM families
		JOIN members ON members.family_id = families.id
		WHERE members.active
			AND EXISTS (
				SELECT 1 FROM role_assignments
				WHERE role_assignments.member_id = members.id
					AND role_assignments.unit_id = $1
					AND role_assignments.role <> 'super_admin'
			)
		GROUP BY families.id, families.name, families.release_address, families.address,
			members.id, members.first_name, members.last_name, members.member_type,
			members.release_email, members.release_phone, members.email, members.home_phone, members.cell_phone
		ORDER BY families.name, (members.member_type = 'youth'), members.first_name
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byFamily := make(map[string]*DirectoryEntry)
	var order []string
	for rows.Next() {
		var familyID, familyName, address string
		var m DirectoryMember
		if err := rows.Scan(&familyID, &familyName, &address, &m.ID, &m.FirstName, &m.LastName, &m.Email, &m.HomePhone, &m.CellPhone); err != nil {
			return nil, err
		}
		entry, ok := byFamily[familyID]
		if !ok {
			entry = &DirectoryEntry{FamilyID: familyID, FamilyName: familyName, Address: address}
			byFamily[familyID] = entry
			order = append(order, familyID)
		}
		entry.Members = append(entry.Members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]DirectoryEntry, 0, len(order))
	for _, id := range order {
		out = append(out, *byFamily[id])
	}
	return out, nil
}

// slugify turns a human-entered label ("Committee Chair") into a stable,
// URL/column-safe slug ("committee_chair") — lowercased, non-alphanumeric
// runs collapsed to a single underscore, leading/trailing underscores
// trimmed.
func slugify(label string) string {
	var b strings.Builder
	lastWasUnderscore := false
	for _, r := range strings.ToLower(label) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastWasUnderscore = false
		case !lastWasUnderscore:
			b.WriteByte('_')
			lastWasUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

// --- Dens / patrols ---------------------------------------------------------

type SubGroup struct {
	ID          string
	UnitID      string
	Name        string
	Type        string // "den" | "patrol"
	Description string // shown on the sub-group's own members-only page — see migration 0017
}

// SubGroupsForUnit lists every den/patrol in a unit, for populating
// dropdowns. Deliberately unrestricted by scope — a Den Leader should be
// able to see that other dens exist even though they can't manage them;
// the actual write-time restriction happens in Scope.CanManageSubGroup.
func SubGroupsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]SubGroup, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, unit_id, name, sub_group_type::text, COALESCE(description, '')
		FROM sub_groups
		WHERE unit_id = $1
		ORDER BY name
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []SubGroup
	for rows.Next() {
		var g SubGroup
		if err := rows.Scan(&g.ID, &g.UnitID, &g.Name, &g.Type, &g.Description); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// GetSubGroup looks up a single patrol/den, scoped to a unit — what a
// sub-group's own page (GroupView/AdminGroupEdit, internal/web) loads.
func GetSubGroup(ctx context.Context, pool *pgxpool.Pool, subGroupID, unitID string) (SubGroup, bool, error) {
	var g SubGroup
	err := pool.QueryRow(ctx, `
		SELECT id, unit_id, name, sub_group_type::text, COALESCE(description, '')
		FROM sub_groups WHERE id = $1 AND unit_id = $2
	`, subGroupID, unitID).Scan(&g.ID, &g.UnitID, &g.Name, &g.Type, &g.Description)
	if err != nil {
		return SubGroup{}, false, nil //nolint:nilerr // "no such sub-group in this unit" is a normal, expected outcome
	}
	return g, true, nil
}

// UpdateSubGroupDescription sets a patrol's/den's own-page blurb. Callers
// are responsible for checking Scope.CanManageSubGroup first — a Den
// Leader may edit their own den's page, not another den's.
func UpdateSubGroupDescription(ctx context.Context, pool *pgxpool.Pool, subGroupID, unitID, description, actorID string) error {
	before, found, err := GetSubGroup(ctx, pool, subGroupID, unitID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("roster: sub-group %s not found in unit %s", subGroupID, unitID)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE sub_groups SET description = $1 WHERE id = $2 AND unit_id = $3
	`, description, subGroupID, unitID); err != nil {
		return err
	}

	after := before
	after.Description = description
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "sub_group",
		EntityID:   subGroupID,
		ActorID:    &actorID,
		Action:     "update",
		Before:     before,
		After:      after,
	})
	return nil
}

// CreateSubGroup adds a new den/patrol. Callers must check the acting
// leader is unit-wide (Scope.UnitWide) first — creating a new den/patrol is
// an organizational decision above a single Den Leader's scope.
func CreateSubGroup(ctx context.Context, pool *pgxpool.Pool, unitID, name, subGroupType, actorID string) (SubGroup, error) {
	var g SubGroup
	err := pool.QueryRow(ctx, `
		INSERT INTO sub_groups (unit_id, name, sub_group_type)
		VALUES ($1, $2, $3)
		RETURNING id, unit_id, name, sub_group_type::text
	`, unitID, name, subGroupType).Scan(&g.ID, &g.UnitID, &g.Name, &g.Type)
	if err != nil {
		return SubGroup{}, err
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "sub_group",
		EntityID:   g.ID,
		ActorID:    &actorID,
		Action:     "create",
		After:      g,
	})
	return g, nil
}

// SubGroupUnitID returns which unit a sub_group belongs to, so a handler
// can reject a submitted sub_group_id that exists but belongs to the
// *other* unit (e.g. a Troop leader's form somehow submitting a Pack den's
// id) — Scope.CanManageSubGroup alone doesn't check that, since a
// unit-wide leader is otherwise allowed to manage any sub_group.
func SubGroupUnitID(ctx context.Context, pool *pgxpool.Pool, subGroupID string) (unitID string, ok bool, err error) {
	err = pool.QueryRow(ctx, `SELECT unit_id FROM sub_groups WHERE id = $1`, subGroupID).Scan(&unitID)
	if err != nil {
		return "", false, nil //nolint:nilerr // "not found" is a normal, expected outcome
	}
	return unitID, true, nil
}

// FamilyEmail looks up a family's shared, family-wide login email, for the
// "here's the account we just reset" confirmation screen. Deliberately
// scoped to member_id IS NULL — a family can now also have individual
// member logins (see internal/auth.User.MemberID) with their own,
// different email addresses, and this is specifically "the family-wide
// one", not just any login tied to the family.
func FamilyEmail(ctx context.Context, pool *pgxpool.Pool, familyID string) (string, error) {
	var email string
	err := pool.QueryRow(ctx, `SELECT email FROM users WHERE family_id = $1 AND member_id IS NULL LIMIT 1`, familyID).Scan(&email)
	return email, err
}

// FamilyIDForEmail looks up the family a login email already belongs to,
// if any — used by the Scoutbook CSV import (see internal/web/roster_import.go)
// to decide whether a row's family already exists (add the row's members
// to it) or needs to be created from scratch, the same "email already
// registered" check CreateFamilyWithMember makes, just queryable ahead of
// time instead of only as a creation-time error.
func FamilyIDForEmail(ctx context.Context, pool *pgxpool.Pool, email string) (familyID string, found bool, err error) {
	err = pool.QueryRow(ctx, `SELECT family_id FROM users WHERE email = $1`, auth.NormalizeEmail(email)).Scan(&familyID)
	if err != nil {
		return "", false, nil //nolint:nilerr // "no login with that email yet" is a normal, expected outcome
	}
	return familyID, true, nil
}

// MemberLoginEmail looks up one member's individual login email, if they
// have one — for the "here's the account we just created/reset"
// confirmation screen when acting on an individual Scout login rather than
// the family-wide one.
func MemberLoginEmail(ctx context.Context, pool *pgxpool.Pool, memberID string) (string, bool, error) {
	var email string
	err := pool.QueryRow(ctx, `SELECT email FROM users WHERE member_id = $1`, memberID).Scan(&email)
	if err != nil {
		return "", false, nil //nolint:nilerr // "no individual login yet" is a normal, expected outcome
	}
	return email, true, nil
}

// --- Scope -------------------------------------------------------------

// Scope describes what part of a unit's roster the acting family may
// manage: everything (a Cubmaster, Scoutmaster, Assistant Scoutmaster, or
// super_admin), or only specific dens/patrols (a Den Leader).
type Scope struct {
	UnitWide    bool
	SubGroupIDs map[string]bool
}

func (s Scope) CanManageSubGroup(subGroupID string) bool {
	if s.UnitWide {
		return true
	}
	return subGroupID != "" && s.SubGroupIDs[subGroupID]
}

// ScopeForFamily computes an acting family's roster-management scope in a
// unit from their existing role assignments. Unit-wide adult leader roles
// (cubmaster, scoutmaster, assistant_scoutmaster, super_admin) grant
// UnitWide; den_leader roles grant only the specific sub_group(s) they're
// assigned to. A family with no qualifying role gets an empty Scope —
// callers should have already gated page access with
// units.CanEditUnitContent before this matters.
func ScopeForFamily(ctx context.Context, pool *pgxpool.Pool, familyID, unitID string) (Scope, error) {
	rows, err := pool.Query(ctx, `
		SELECT role_assignments.role::text, COALESCE(role_assignments.sub_group_id::text, '')
		FROM role_assignments
		JOIN members ON members.id = role_assignments.member_id
		WHERE members.family_id = $1 AND role_assignments.unit_id = $2
	`, familyID, unitID)
	if err != nil {
		return Scope{}, err
	}
	defer rows.Close()

	scope := Scope{SubGroupIDs: make(map[string]bool)}
	for rows.Next() {
		var role, subGroupID string
		if err := rows.Scan(&role, &subGroupID); err != nil {
			return Scope{}, err
		}
		switch role {
		case "cubmaster", "scoutmaster", "assistant_scoutmaster", "super_admin":
			scope.UnitWide = true
		case "den_leader":
			if subGroupID != "" {
				scope.SubGroupIDs[subGroupID] = true
			}
		}
	}
	return scope, rows.Err()
}

// ScopeForMember computes one specific member's own roster-management
// scope in a unit from their own role assignments only — the member-
// scoped sibling of ScopeForFamily, used when the acting login is an
// individual member login (see internal/auth.User.MemberID) rather than a
// family-wide one. Deliberately does NOT look at what other members of
// the same family hold: per the "an individual login sees just their own
// stuff" design, a Scout's own login managing the roster (if they hold a
// leadership role themselves, e.g. an Assistant Scoutmaster who also
// happens to be a Scout's sibling with their own login) should be scoped
// to their own roles, not broadened by a parent's or sibling's roles just
// because they share a family_id.
func ScopeForMember(ctx context.Context, pool *pgxpool.Pool, memberID, unitID string) (Scope, error) {
	rows, err := pool.Query(ctx, `
		SELECT role::text, COALESCE(sub_group_id::text, '')
		FROM role_assignments
		WHERE member_id = $1 AND unit_id = $2
	`, memberID, unitID)
	if err != nil {
		return Scope{}, err
	}
	defer rows.Close()

	scope := Scope{SubGroupIDs: make(map[string]bool)}
	for rows.Next() {
		var role, subGroupID string
		if err := rows.Scan(&role, &subGroupID); err != nil {
			return Scope{}, err
		}
		switch role {
		case "cubmaster", "scoutmaster", "assistant_scoutmaster", "super_admin":
			scope.UnitWide = true
		case "den_leader":
			if subGroupID != "" {
				scope.SubGroupIDs[subGroupID] = true
			}
		}
	}
	return scope, rows.Err()
}

// SubGroupIDsForFamily returns every patrol/den any member of a family
// belongs to in a unit — via role_assignments.sub_group_id, regardless of
// whether that role carries any roster-management capability. This is
// deliberately a different question from Scope (which roles a family can
// *manage* the roster for): a plain parent/Scout with no leadership role
// still belongs to their own den/patrol, and needs to see that den's
// scoped calendar events (see calendar.FilterVisibleToViewer) even though
// their Scope is empty.
func SubGroupIDsForFamily(ctx context.Context, pool *pgxpool.Pool, familyID, unitID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT role_assignments.sub_group_id::text
		FROM role_assignments
		JOIN members ON members.id = role_assignments.member_id
		WHERE members.family_id = $1 AND role_assignments.unit_id = $2 AND role_assignments.sub_group_id IS NOT NULL
	`, familyID, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SubGroupIDsForMember is SubGroupIDsForFamily narrowed to one member's own
// role assignments — the individual-login sibling, same "just their own
// stuff" pattern as ScopeForMember.
func SubGroupIDsForMember(ctx context.Context, pool *pgxpool.Pool, memberID, unitID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT sub_group_id::text
		FROM role_assignments
		WHERE member_id = $1 AND unit_id = $2 AND sub_group_id IS NOT NULL
	`, memberID, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CanManageMember reports whether the acting scope covers a member who
// already has role assignments in this unit — true if the scope is
// unit-wide AND the member holds some role in this unit, or if any of the
// member's roles in this unit are scoped to a sub_group the acting leader
// manages.
//
// Security note: the unit-wide case still requires at least one
// role_assignment row for (memberID, unitID) — a unit-wide leader (e.g. a
// Scoutmaster) manages everyone *in their own unit*, not every member in
// the entire database. Every legitimate caller only ever reaches a
// memberID that's already listed in this unit's roster (family.RosterForUnit,
// which itself filters by unit_id), so requiring that same relationship
// here doesn't block any real workflow — it just closes off reaching this
// function directly with an arbitrary/unrelated memberID (e.g. by editing
// the URL), which would otherwise let a leader of one unit view, rename,
// grant roles to, or reset the password of a family with zero relationship
// to their unit.
func (s Scope) CanManageMember(ctx context.Context, pool *pgxpool.Pool, memberID, unitID string) (bool, error) {
	rows, err := pool.Query(ctx, `
		SELECT COALESCE(sub_group_id::text, '') FROM role_assignments
		WHERE member_id = $1 AND unit_id = $2
	`, memberID, unitID)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var subGroupID string
		if err := rows.Scan(&subGroupID); err != nil {
			return false, err
		}
		if s.UnitWide || s.CanManageSubGroup(subGroupID) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ManageableMemberIDs returns the set of member IDs in a unit that fall
// within scope — every member if scope is unit-wide (signaled by a nil
// map, so callers don't have to special-case "unit-wide" separately from
// "everyone happens to be in this map"), or only those with at least one
// role scoped to one of scope's sub_groups otherwise. Used by the roster
// admin list to decide which rows get an "Edit" link in one query, instead
// of one CanManageMember round trip per row.
func ManageableMemberIDs(ctx context.Context, pool *pgxpool.Pool, unitID string, scope Scope) (map[string]bool, error) {
	if scope.UnitWide {
		return nil, nil
	}
	if len(scope.SubGroupIDs) == 0 {
		return map[string]bool{}, nil
	}
	ids := make([]string, 0, len(scope.SubGroupIDs))
	for id := range scope.SubGroupIDs {
		ids = append(ids, id)
	}
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT member_id FROM role_assignments
		WHERE unit_id = $1 AND sub_group_id = ANY($2::uuid[])
	`, unitID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	manageable := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		manageable[id] = true
	}
	return manageable, rows.Err()
}

// RoleOption is one assignable role, filtered to what makes sense for the
// current unit type and the acting leader's scope.
type RoleOption struct {
	Value string
	Label string
}

// fixedRoleOptions returns the roles the acting scope may assign, on this
// unit, from the fixed code-defined set — everything AllowedRoles returned
// before custom roles existed. super_admin is deliberately never offered
// here — minting the highest privilege tier stays a manual/bootstrap-only
// operation (see internal/bootstrap), not something reachable from a web
// form. Unit-wide leaders get the full set for their unit type; scoped Den
// Leaders are limited to non-leadership roles so they can't promote a
// family to Cubmaster from a roster form.
func fixedRoleOptions(unitType string, scope Scope) []RoleOption {
	if !scope.UnitWide {
		return []RoleOption{
			{"parent", "Parent"},
			{"scout", "Scout"},
		}
	}
	if unitType == "troop" {
		return []RoleOption{
			{"scoutmaster", "Scoutmaster"},
			{"assistant_scoutmaster", "Assistant Scoutmaster"},
			{"senior_patrol_leader", "Senior Patrol Leader"},
			{"patrol_leader", "Patrol Leader"},
			{"treasurer", "Treasurer"},
			{"parent", "Parent"},
			{"scout", "Scout"},
		}
	}
	return []RoleOption{
		{"cubmaster", "Cubmaster"},
		{"den_leader", "Den Leader"},
		{"treasurer", "Treasurer"},
		{"parent", "Parent"},
		{"scout", "Scout"},
	}
}

// AllowedRoles returns every role the acting scope may assign on this
// unit — the fixed code-defined set (see fixedRoleOptions) plus any
// custom roles a super_admin has created for this unit (see
// CreateCustomRole). Custom roles are only offered to unit-wide leaders,
// same restriction as the leadership tier of the fixed set: a scoped Den
// Leader can't grant a custom role that might carry real capabilities
// (edit_content, manage_ledger, etc.) any more than they could promote
// someone straight to Cubmaster.
func AllowedRoles(ctx context.Context, pool *pgxpool.Pool, unitType, unitID string, scope Scope) ([]RoleOption, error) {
	opts := fixedRoleOptions(unitType, scope)
	if !scope.UnitWide {
		return opts, nil
	}
	custom, err := ListCustomRoles(ctx, pool, unitID)
	if err != nil {
		return nil, err
	}
	for _, cr := range custom {
		opts = append(opts, RoleOption{Value: cr.Slug, Label: cr.Label})
	}
	return opts, nil
}

// IsAllowedRole reports whether role is present in AllowedRoles — the
// server-side check backing the client-side dropdown, since a scoped
// leader could otherwise POST an arbitrary role value directly.
func IsAllowedRole(ctx context.Context, pool *pgxpool.Pool, unitType, unitID string, scope Scope, role string) (bool, error) {
	opts, err := AllowedRoles(ctx, pool, unitType, unitID, scope)
	if err != nil {
		return false, err
	}
	for _, opt := range opts {
		if opt.Value == role {
			return true, nil
		}
	}
	return false, nil
}

// --- Custom roles -----------------------------------------------------

// CustomRole is a per-unit role a super_admin created on the fly, with
// whichever capabilities they chose to grant it (see internal/units'
// capability constants).
type CustomRole struct {
	ID           string
	UnitID       string
	Slug         string
	Label        string
	Capabilities []string
	CreatedAt    string
}

// ErrReservedRoleSlug is returned by CreateCustomRole when the requested
// slug collides with one of the fixed system role slugs — allowing that
// would make role_assignments.role ambiguous about which meaning applies.
var ErrReservedRoleSlug = fmt.Errorf("roster: that role name is reserved")

// CreateCustomRole adds a new role for a unit. label is slugified
// (lowercased, spaces to underscores, stripped of anything not
// alphanumeric/underscore) to produce a stable slug — the same
// human-friendly-label-in, stable-slug-out pattern content sections
// already use (see internal/content). capabilities is filtered to only
// ever contain names from units.AllCapabilities; anything else is
// silently dropped rather than erroring, since the admin form's checkbox
// list is the only caller and can't produce an invalid name to begin with.
func CreateCustomRole(ctx context.Context, pool *pgxpool.Pool, unitID, label string, capabilities []string, actorID string) (CustomRole, error) {
	slug := slugify(label)
	if slug == "" {
		return CustomRole{}, fmt.Errorf("roster: role name %q doesn't produce a usable slug", label)
	}
	if units.ReservedRoleSlugs[slug] {
		return CustomRole{}, ErrReservedRoleSlug
	}

	var granted []string
	valid := make(map[string]bool, len(units.AllCapabilities))
	for _, c := range units.AllCapabilities {
		valid[c] = true
	}
	for _, c := range capabilities {
		if valid[c] {
			granted = append(granted, c)
		}
	}

	var cr CustomRole
	err := pool.QueryRow(ctx, `
		INSERT INTO custom_roles (unit_id, slug, label, capabilities, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, unit_id, slug, label, capabilities, created_at::text
	`, unitID, slug, label, granted, actorID).Scan(&cr.ID, &cr.UnitID, &cr.Slug, &cr.Label, &cr.Capabilities, &cr.CreatedAt)
	if err != nil {
		return CustomRole{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "custom_role",
		EntityID:   cr.ID,
		ActorID:    &actorID,
		Action:     "create",
		After:      cr,
	})
	return cr, nil
}

// ListCustomRoles returns every custom role defined for a unit, most
// recently created first.
func ListCustomRoles(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]CustomRole, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, unit_id, slug, label, capabilities, created_at::text
		FROM custom_roles WHERE unit_id = $1 ORDER BY created_at DESC
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CustomRole
	for rows.Next() {
		var cr CustomRole
		if err := rows.Scan(&cr.ID, &cr.UnitID, &cr.Slug, &cr.Label, &cr.Capabilities, &cr.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	return out, rows.Err()
}

// DeleteCustomRole removes a custom role definition, scoped to unitID so
// one unit can't delete another's. Existing role_assignments rows using
// this slug are left in place (harmless — CapabilitiesForRoles simply
// finds nothing for a slug with no matching custom_roles row, so anyone
// still holding the deleted role reverts to having no extra capabilities
// from it, same as if they'd never had a role assigned); a leader who
// wants those members' role reassigned handles that separately.
func DeleteCustomRole(ctx context.Context, pool *pgxpool.Pool, roleID, unitID, actorID string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM custom_roles WHERE id = $1 AND unit_id = $2`, roleID, unitID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "custom_role",
		EntityID:   roleID,
		ActorID:    &actorID,
		Action:     "delete",
	})
	return nil
}

// --- Families & members -----------------------------------------------

// FamilyOption is a minimal family reference for the "add to existing
// family" picker. Families aren't unit-scoped in the schema (one account
// can span both the Troop and Pack, by design — see
// scout-website-architecture-phase1.md Section 2), so this lists every
// family system-wide rather than filtering by the current unit.
type FamilyOption struct {
	ID   string
	Name string
}

func AllFamilies(ctx context.Context, pool *pgxpool.Pool) ([]FamilyOption, error) {
	rows, err := pool.Query(ctx, `SELECT id, name FROM families ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var families []FamilyOption
	for rows.Next() {
		var f FamilyOption
		if err := rows.Scan(&f.ID, &f.Name); err != nil {
			return nil, err
		}
		families = append(families, f)
	}
	return families, rows.Err()
}

// MemberOption is a minimal, cross-unit member reference for the "add an
// existing person to this unit" picker (see MembersNotInUnit) — a family's
// name alongside the member so two same-first-named Scouts in different
// families are still distinguishable in a flat dropdown.
type MemberOption struct {
	ID         string
	FirstName  string
	LastName   string
	FamilyName string
}

// MembersNotInUnit lists every member system-wide who does not already
// hold at least one role_assignment in the given unit — i.e. exactly the
// people a leader can't currently find on their own unit's roster page,
// because family.RosterForUnit (deliberately) only lists members already
// part of the unit. This is what lets a leader give an existing person
// (most commonly: a Scout or parent already registered under the other
// unit, e.g. a Pack Scout crossing over to a Troop position) their first
// role here, without creating a duplicate member row for someone who
// already exists in the system.
func MembersNotInUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]MemberOption, error) {
	rows, err := pool.Query(ctx, `
		SELECT members.id, members.first_name, members.last_name, families.name
		FROM members
		JOIN families ON families.id = members.family_id
		WHERE members.id NOT IN (
			SELECT member_id FROM role_assignments WHERE unit_id = $1
		)
		ORDER BY families.name, (members.member_type = 'youth'), members.first_name
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MemberOption
	for rows.Next() {
		var m MemberOption
		if err := rows.Scan(&m.ID, &m.FirstName, &m.LastName, &m.FamilyName); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// NewFamilyInput is what creating a brand-new family login needs.
type NewFamilyInput struct {
	FamilyName string
	Email      string
	FirstName  string // first adult member's name
	LastName   string
}

// CreateFamilyWithMember creates a family, its login, and its first adult
// member in one transaction, generating a temporary password (returned in
// plaintext exactly once — there's nowhere else to see it again, since
// only the bcrypt hash is stored). Mirrors internal/bootstrap.CreateAdmin's
// transaction shape, minus the automatic super_admin role — the caller
// assigns whatever role actually fits via AssignRole.
func CreateFamilyWithMember(ctx context.Context, pool *pgxpool.Pool, in NewFamilyInput, actorID string) (familyID, memberID, tempPassword string, err error) {
	email := auth.NormalizeEmail(in.Email)

	var existing string
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&existing); err == nil {
		return "", "", "", fmt.Errorf("a family with email %s is already registered", email)
	}

	tempPassword, err = auth.GenerateTemporaryPassword()
	if err != nil {
		return "", "", "", fmt.Errorf("generating temporary password: %w", err)
	}
	passwordHash, err := auth.HashPassword(tempPassword)
	if err != nil {
		return "", "", "", fmt.Errorf("hashing password: %w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", "", "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if already committed

	if err := tx.QueryRow(ctx,
		`INSERT INTO families (name) VALUES ($1) RETURNING id`, in.FamilyName,
	).Scan(&familyID); err != nil {
		return "", "", "", fmt.Errorf("creating family: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO users (family_id, email, password_hash, must_change_password) VALUES ($1, $2, $3, true)`,
		familyID, email, passwordHash,
	); err != nil {
		return "", "", "", fmt.Errorf("creating login: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO members (family_id, first_name, last_name, member_type) VALUES ($1, $2, $3, 'adult') RETURNING id`,
		familyID, in.FirstName, in.LastName,
	).Scan(&memberID); err != nil {
		return "", "", "", fmt.Errorf("creating member: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", "", "", err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "family",
		EntityID:   familyID,
		ActorID:    &actorID,
		Action:     "create",
		After:      map[string]string{"name": in.FamilyName, "email": email},
	})
	return familyID, memberID, tempPassword, nil
}

// ResetFamilyPassword generates and stores a new temporary password for a
// family's shared, family-wide login, returned in plaintext once. The only
// recovery path for a locked-out family that doesn't have (or doesn't
// remember) email access — see internal/web/password_reset.go for the
// self-service "forgot password" email flow most families should use
// day-to-day. Deliberately scoped to the family-wide login (member_id IS
// NULL) — see ResetMemberLoginPassword for resetting one specific member's
// own individual login instead.
func ResetFamilyPassword(ctx context.Context, pool *pgxpool.Pool, familyID, actorID string) (tempPassword string, err error) {
	tempPassword, err = auth.GenerateTemporaryPassword()
	if err != nil {
		return "", err
	}
	passwordHash, err := auth.HashPassword(tempPassword)
	if err != nil {
		return "", err
	}

	tag, err := pool.Exec(ctx,
		`UPDATE users SET password_hash = $1, must_change_password = true WHERE family_id = $2 AND member_id IS NULL`,
		passwordHash, familyID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", fmt.Errorf("no family-wide login found for family %s", familyID)
	}

	// Sign out every device this family was already logged into — same
	// reasoning as the self-service reset flow (internal/auth.ConsumeResetToken):
	// a session shouldn't survive the very password reset meant to shut it
	// out. Best-effort: a failure here shouldn't stop the leader from
	// getting the new temporary password to hand off, so it's logged, not
	// returned as an error.
	if err := auth.DestroySessionsForFamily(ctx, pool, familyID); err != nil {
		log.Printf("roster: invalidating sessions after password reset for family %s: %v", familyID, err)
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "family",
		EntityID:   familyID,
		ActorID:    &actorID,
		Action:     "reset_password",
	})
	return tempPassword, nil
}

// MemberHasLogin reports whether a member already has their own individual
// login (as opposed to only being reachable through their family's shared,
// family-wide login) — used to decide whether the roster admin UI offers
// "create an individual login" or "reset individual login password" for a
// given member.
func MemberHasLogin(ctx context.Context, pool *pgxpool.Pool, memberID string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE member_id = $1)`, memberID).Scan(&exists)
	return exists, err
}

// CreateMemberLogin creates a brand-new individual login for one specific
// member — e.g. a Scout who should be able to log in as themselves rather
// than only through their family's shared login (see internal/auth.User.
// MemberID). Generates a temporary password, returned in plaintext exactly
// once, same as CreateFamilyWithMember. The email must not already be in
// use by any login (family-wide or individual) system-wide, and the member
// must not already have an individual login of their own — callers should
// check MemberHasLogin first for a friendlier error, but this also
// re-checks atomically since the UNIQUE constraint on users.member_id
// would otherwise just surface as a generic database error.
func CreateMemberLogin(ctx context.Context, pool *pgxpool.Pool, memberID, familyID, email, actorID string) (tempPassword string, err error) {
	normalized := auth.NormalizeEmail(email)

	var existing string
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, normalized).Scan(&existing); err == nil {
		return "", fmt.Errorf("a login with email %s already exists", normalized)
	}
	hasLogin, err := MemberHasLogin(ctx, pool, memberID)
	if err != nil {
		return "", err
	}
	if hasLogin {
		return "", fmt.Errorf("this member already has an individual login")
	}

	tempPassword, err = auth.GenerateTemporaryPassword()
	if err != nil {
		return "", err
	}
	passwordHash, err := auth.HashPassword(tempPassword)
	if err != nil {
		return "", err
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO users (family_id, member_id, email, password_hash, must_change_password) VALUES ($1, $2, $3, $4, true)`,
		familyID, memberID, normalized, passwordHash,
	); err != nil {
		return "", fmt.Errorf("creating individual login: %w", err)
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "member",
		EntityID:   memberID,
		ActorID:    &actorID,
		Action:     "create_individual_login",
		After:      map[string]string{"email": normalized},
	})
	return tempPassword, nil
}

// ResetMemberLoginPassword generates and stores a new temporary password
// for one specific member's individual login — the member-scoped sibling
// of ResetFamilyPassword, for a Scout who logs in with their own account
// rather than their family's shared one.
func ResetMemberLoginPassword(ctx context.Context, pool *pgxpool.Pool, memberID, actorID string) (tempPassword string, err error) {
	tempPassword, err = auth.GenerateTemporaryPassword()
	if err != nil {
		return "", err
	}
	passwordHash, err := auth.HashPassword(tempPassword)
	if err != nil {
		return "", err
	}

	tag, err := pool.Exec(ctx,
		`UPDATE users SET password_hash = $1, must_change_password = true WHERE member_id = $2`,
		passwordHash, memberID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", fmt.Errorf("no individual login found for member %s", memberID)
	}

	if err := auth.DestroySessionsForMember(ctx, pool, memberID); err != nil {
		log.Printf("roster: invalidating sessions after password reset for member %s: %v", memberID, err)
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "member",
		EntityID:   memberID,
		ActorID:    &actorID,
		Action:     "reset_individual_login_password",
	})
	return tempPassword, nil
}

// AddMember adds a new member (adult or youth) to an existing family.
func AddMember(ctx context.Context, pool *pgxpool.Pool, familyID, firstName, lastName, memberType, actorID string) (memberID string, err error) {
	err = pool.QueryRow(ctx, `
		INSERT INTO members (family_id, first_name, last_name, member_type)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, familyID, firstName, lastName, memberType).Scan(&memberID)
	if err != nil {
		return "", err
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "member",
		EntityID:   memberID,
		ActorID:    &actorID,
		Action:     "create",
		After:      map[string]string{"first_name": firstName, "last_name": lastName, "member_type": memberType},
	})
	return memberID, nil
}

// UpdateMember edits a member's name/type.
func UpdateMember(ctx context.Context, pool *pgxpool.Pool, memberID, firstName, lastName, memberType, actorID string) error {
	var before struct{ FirstName, LastName, MemberType string }
	_ = pool.QueryRow(ctx, `SELECT first_name, last_name, member_type::text FROM members WHERE id = $1`, memberID).
		Scan(&before.FirstName, &before.LastName, &before.MemberType)

	tag, err := pool.Exec(ctx, `
		UPDATE members SET first_name = $1, last_name = $2, member_type = $3 WHERE id = $4
	`, firstName, lastName, memberType, memberID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("member %s not found", memberID)
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "member",
		EntityID:   memberID,
		ActorID:    &actorID,
		Action:     "update",
		Before:     before,
		After:      map[string]string{"first_name": firstName, "last_name": lastName, "member_type": memberType},
	})
	return nil
}

// SetContactInfo updates a member's own email/phone numbers and whether
// each is released to the rest of the unit (see migration 0015 and
// EmailsReleasedInUnit/PhonesReleasedInUnit below). Deliberately has no
// roster.Scope check of its own — a member's family should be able to
// call this for their own contact info without needing CanEditUnitContent
// (see internal/web/my_family.go's self-service page), while a roster
// admin editing it through /admin/roster is already gated by
// requireRosterEditor before reaching here. The caller decides who's
// allowed to invoke this for a given memberID; this function just writes
// what it's given.
func SetContactInfo(ctx context.Context, pool *pgxpool.Pool, memberID, email, homePhone, cellPhone string, releaseEmail, releasePhone bool, actorID string) error {
	var before struct {
		Email, HomePhone, CellPhone string
		ReleaseEmail, ReleasePhone  bool
	}
	_ = pool.QueryRow(ctx, `
		SELECT COALESCE(email, ''), COALESCE(home_phone, ''), COALESCE(cell_phone, ''), release_email, release_phone
		FROM members WHERE id = $1
	`, memberID).Scan(&before.Email, &before.HomePhone, &before.CellPhone, &before.ReleaseEmail, &before.ReleasePhone)

	tag, err := pool.Exec(ctx, `
		UPDATE members SET email = NULLIF($1, ''), home_phone = NULLIF($2, ''), cell_phone = NULLIF($3, ''),
			release_email = $4, release_phone = $5
		WHERE id = $6
	`, email, homePhone, cellPhone, releaseEmail, releasePhone, memberID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("member %s not found", memberID)
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "member_contact_info",
		EntityID:   memberID,
		ActorID:    &actorID,
		Action:     "update",
		Before:     before,
		After: map[string]any{
			"email": email, "home_phone": homePhone, "cell_phone": cellPhone,
			"release_email": releaseEmail, "release_phone": releasePhone,
		},
	})
	return nil
}

// SetFamilyAddress updates a family's household address and whether it's
// released to the rest of the unit — the family-level sibling of
// SetContactInfo, same "caller decides who's allowed to call this"
// design.
func SetFamilyAddress(ctx context.Context, pool *pgxpool.Pool, familyID, address string, releaseAddress bool, actorID string) error {
	var before struct {
		Address        string
		ReleaseAddress bool
	}
	_ = pool.QueryRow(ctx, `SELECT COALESCE(address, ''), release_address FROM families WHERE id = $1`, familyID).
		Scan(&before.Address, &before.ReleaseAddress)

	tag, err := pool.Exec(ctx, `
		UPDATE families SET address = NULLIF($1, ''), release_address = $2 WHERE id = $3
	`, address, releaseAddress, familyID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("family %s not found", familyID)
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "family_address",
		EntityID:   familyID,
		ActorID:    &actorID,
		Action:     "update",
		Before:     before,
		After:      map[string]any{"address": address, "release_address": releaseAddress},
	})
	return nil
}

// --- Role assignments ---------------------------------------------------

// AssignRole grants a role to a member in a unit, optionally scoped to a
// den/patrol. Silently does nothing if the exact (member, unit, sub_group,
// role) combination is already assigned — role_assignments has a unique
// constraint on that tuple, and re-clicking "assign" shouldn't error.
func AssignRole(ctx context.Context, pool *pgxpool.Pool, memberID, unitID string, subGroupID *string, role, actorID string) error {
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO role_assignments (member_id, unit_id, sub_group_id, role)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (member_id, unit_id, sub_group_id, role) DO NOTHING
		RETURNING id
	`, memberID, unitID, subGroupID, role).Scan(&id)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil // already assigned — not an error
		}
		return err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "role_assignment",
		EntityID:   id,
		ActorID:    &actorID,
		Action:     "create",
		After:      map[string]string{"member_id": memberID, "unit_id": unitID, "role": role},
	})
	return nil
}

// RoleAssignment is one role a member holds, with enough context for the
// edit page to display and for a caller to authorize removal.
type RoleAssignment struct {
	ID           string
	MemberID     string
	UnitID       string
	Role         string
	SubGroupID   string // "" = whole-unit scope
	SubGroupName string
}

// GetRoleAssignment fetches one role assignment — used to authorize a
// removal (check unit + scope) *before* RemoveRole deletes it.
func GetRoleAssignment(ctx context.Context, pool *pgxpool.Pool, roleAssignmentID string) (RoleAssignment, bool, error) {
	var ra RoleAssignment
	err := pool.QueryRow(ctx, `
		SELECT role_assignments.id, role_assignments.member_id, role_assignments.unit_id,
			role_assignments.role::text, COALESCE(role_assignments.sub_group_id::text, ''),
			COALESCE(sub_groups.name, '')
		FROM role_assignments
		LEFT JOIN sub_groups ON sub_groups.id = role_assignments.sub_group_id
		WHERE role_assignments.id = $1
	`, roleAssignmentID).Scan(&ra.ID, &ra.MemberID, &ra.UnitID, &ra.Role, &ra.SubGroupID, &ra.SubGroupName)
	if err != nil {
		return RoleAssignment{}, false, nil //nolint:nilerr // "not found" is a normal, expected outcome
	}
	return ra, true, nil
}

// RemoveRole deletes a role assignment after re-fetching it for the audit
// trail (callers should have already authorized the removal via
// GetRoleAssignment + a scope check — this re-fetch is for the audit
// before-state, not authorization). Returns (RoleAssignment{}, false, nil)
// if it's already gone — treated as a no-op, not an error.
func RemoveRole(ctx context.Context, pool *pgxpool.Pool, roleAssignmentID, actorID string) (RoleAssignment, bool, error) {
	ra, ok, err := GetRoleAssignment(ctx, pool, roleAssignmentID)
	if err != nil || !ok {
		return RoleAssignment{}, false, err
	}

	if _, err := pool.Exec(ctx, `DELETE FROM role_assignments WHERE id = $1`, roleAssignmentID); err != nil {
		return RoleAssignment{}, false, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "role_assignment",
		EntityID:   ra.ID,
		ActorID:    &actorID,
		Action:     "delete",
		Before:     ra,
	})
	return ra, true, nil
}

// RolesForMemberInUnit lists a member's role assignments in a unit, for the
// member edit page.
func RolesForMemberInUnit(ctx context.Context, pool *pgxpool.Pool, memberID, unitID string) ([]RoleAssignment, error) {
	rows, err := pool.Query(ctx, `
		SELECT role_assignments.id, role_assignments.member_id, role_assignments.unit_id,
			role_assignments.role::text, COALESCE(role_assignments.sub_group_id::text, ''),
			COALESCE(sub_groups.name, '')
		FROM role_assignments
		LEFT JOIN sub_groups ON sub_groups.id = role_assignments.sub_group_id
		WHERE role_assignments.member_id = $1 AND role_assignments.unit_id = $2
		ORDER BY role_assignments.role
	`, memberID, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []RoleAssignment
	for rows.Next() {
		var ra RoleAssignment
		if err := rows.Scan(&ra.ID, &ra.MemberID, &ra.UnitID, &ra.Role, &ra.SubGroupID, &ra.SubGroupName); err != nil {
			return nil, err
		}
		roles = append(roles, ra)
	}
	return roles, rows.Err()
}

// OtherUnitRoles is one other unit a member holds role(s) in, for the
// read-only "also holds roles in" note on the member edit page.
type OtherUnitRoles struct {
	UnitID   string
	UnitName string
	Roles    []string
}

// RolesForMemberOtherUnits lists which OTHER units (not currentUnitID) a
// member holds any role in, and what those roles are — a member/family can
// hold roles in both the Troop and Pack simultaneously (see
// RolesForFamilyInUnit's doc comment), but the member edit page only ever
// queried the current unit, so a Troop leader had no visibility that the
// same person is, say, also a Den Leader in the Pack. This is read-only:
// it doesn't grant the viewing leader any ability to manage those other
// roles, just to see that they exist.
func RolesForMemberOtherUnits(ctx context.Context, pool *pgxpool.Pool, memberID, currentUnitID string) ([]OtherUnitRoles, error) {
	rows, err := pool.Query(ctx, `
		SELECT units.id, units.name, array_agg(role_assignments.role::text ORDER BY role_assignments.role)
		FROM role_assignments
		JOIN units ON units.id = role_assignments.unit_id
		WHERE role_assignments.member_id = $1 AND role_assignments.unit_id != $2
		GROUP BY units.id, units.name
		ORDER BY units.name
	`, memberID, currentUnitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OtherUnitRoles
	for rows.Next() {
		var o OtherUnitRoles
		if err := rows.Scan(&o.UnitID, &o.UnitName, &o.Roles); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// MemberDetail is a member plus enough context for the roster edit page.
type MemberDetail struct {
	ID             string
	FamilyID       string
	FamilyName     string
	FirstName      string
	LastName       string
	MemberType     string
	Active         bool
	Email          string
	HomePhone      string
	CellPhone      string
	ReleaseEmail   bool
	ReleasePhone   bool
	FamilyAddress  string
	ReleaseAddress bool
}

// GetMember fetches a member, their family's name, and both the member's
// own contact info and their family's address (see migration 0015), for
// the edit page.
func GetMember(ctx context.Context, pool *pgxpool.Pool, memberID string) (MemberDetail, bool, error) {
	var m MemberDetail
	err := pool.QueryRow(ctx, `
		SELECT members.id, members.family_id, families.name, members.first_name, members.last_name, members.member_type::text,
			members.active,
			COALESCE(members.email, ''), COALESCE(members.home_phone, ''), COALESCE(members.cell_phone, ''),
			members.release_email, members.release_phone,
			COALESCE(families.address, ''), families.release_address
		FROM members
		JOIN families ON families.id = members.family_id
		WHERE members.id = $1
	`, memberID).Scan(&m.ID, &m.FamilyID, &m.FamilyName, &m.FirstName, &m.LastName, &m.MemberType,
		&m.Active,
		&m.Email, &m.HomePhone, &m.CellPhone, &m.ReleaseEmail, &m.ReleasePhone,
		&m.FamilyAddress, &m.ReleaseAddress)
	if err != nil {
		return MemberDetail{}, false, nil //nolint:nilerr // "not found" is a normal, expected outcome
	}
	return m, true, nil
}

// --- Active status (deactivate/reactivate) -----------------------------

// NonZeroBalanceError is returned by SetMemberActive when deactivating a
// member whose Scout account(s) still carry a nonzero balance. Balances
// holds only the nonzero ones, across every unit the member has an
// individual account in (see ledger.ScoutAccountBalancesForMember) — the
// caller (internal/web/admin_roster.go) uses it to tell the admin exactly
// which unit(s)/amount(s) need to be resolved (spent down, refunded, or
// transferred out) before this member can come off the rolls.
type NonZeroBalanceError struct {
	Balances []ledger.ScoutAccountBalance
}

func (e NonZeroBalanceError) Error() string {
	return "roster: member still has a nonzero Scout account balance"
}

// SetMemberActive deactivates or reactivates a member — the "remove from
// the rolls, but keep their info" operation. Deactivating only flips
// members.active to false (see migration 0022): every role_assignment,
// advancement record, and past ledger transaction stays exactly as it
// was, which is what lets reactivating (flipping it back to true) restore
// a member to the roster with zero repair work. This is deliberately a
// member-wide flag, not scoped to one unit — a member who's off the rolls
// is off every unit's roster at once (see family.RosterForUnit's doc
// comment); the much more common case of a Scout crossing over from Pack
// to Troop is handled by assigning them a new role in the other unit, not
// by deactivating.
//
// Deactivating is refused (NonZeroBalanceError) if the member holds a
// Scout account anywhere with a nonzero balance — the requirements this
// codebase already treats as non-negotiable for a real financial ledger
// (see internal/ledger's package doc) extend to this: money doesn't just
// disappear from view because the person it belongs to came off the
// roster.
func SetMemberActive(ctx context.Context, pool *pgxpool.Pool, memberID string, active bool, actorID string) error {
	if !active {
		balances, err := ledger.ScoutAccountBalancesForMember(ctx, pool, memberID)
		if err != nil {
			return err
		}
		var nonzero []ledger.ScoutAccountBalance
		for _, b := range balances {
			if b.BalanceCents != 0 {
				nonzero = append(nonzero, b)
			}
		}
		if len(nonzero) > 0 {
			return NonZeroBalanceError{Balances: nonzero}
		}
	}

	var before struct{ Active bool }
	_ = pool.QueryRow(ctx, `SELECT active FROM members WHERE id = $1`, memberID).Scan(&before.Active)

	tag, err := pool.Exec(ctx, `UPDATE members SET active = $1 WHERE id = $2`, active, memberID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("member %s not found", memberID)
	}

	action := "deactivate"
	if active {
		action = "reactivate"
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "member",
		EntityID:   memberID,
		ActorID:    &actorID,
		Action:     action,
		Before:     before,
		After:      map[string]bool{"active": active},
	})
	return nil
}
