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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
	"github.com/47-yonkers/scout-site/internal/auth"
)

// --- Dens / patrols ---------------------------------------------------------

type SubGroup struct {
	ID     string
	UnitID string
	Name   string
	Type   string // "den" | "patrol"
}

// SubGroupsForUnit lists every den/patrol in a unit, for populating
// dropdowns. Deliberately unrestricted by scope — a Den Leader should be
// able to see that other dens exist even though they can't manage them;
// the actual write-time restriction happens in Scope.CanManageSubGroup.
func SubGroupsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]SubGroup, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, unit_id, name, sub_group_type::text
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
		if err := rows.Scan(&g.ID, &g.UnitID, &g.Name, &g.Type); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
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

// AllowedRoles returns the roles the acting scope may assign, on this unit.
// super_admin is deliberately never offered here — minting the highest
// privilege tier stays a manual/bootstrap-only operation (see
// internal/bootstrap), not something reachable from a web form. Unit-wide
// leaders get the full set for their unit type; scoped Den Leaders are
// limited to non-leadership roles so they can't promote a family to
// Cubmaster from a roster form.
func AllowedRoles(unitType string, scope Scope) []RoleOption {
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

// IsAllowedRole reports whether role is present in AllowedRoles — the
// server-side check backing the client-side dropdown, since a scoped
// leader could otherwise POST an arbitrary role value directly.
func IsAllowedRole(unitType string, scope Scope, role string) bool {
	for _, opt := range AllowedRoles(unitType, scope) {
		if opt.Value == role {
			return true
		}
	}
	return false
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
		`INSERT INTO users (family_id, email, password_hash) VALUES ($1, $2, $3)`,
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

	tag, err := pool.Exec(ctx, `UPDATE users SET password_hash = $1 WHERE family_id = $2 AND member_id IS NULL`, passwordHash, familyID)
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
		`INSERT INTO users (family_id, member_id, email, password_hash) VALUES ($1, $2, $3, $4)`,
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

	tag, err := pool.Exec(ctx, `UPDATE users SET password_hash = $1 WHERE member_id = $2`, passwordHash, memberID)
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

// MemberDetail is a member plus enough context for the roster edit page.
type MemberDetail struct {
	ID         string
	FamilyID   string
	FamilyName string
	FirstName  string
	LastName   string
	MemberType string
}

// GetMember fetches a member and their family's name, for the edit page.
func GetMember(ctx context.Context, pool *pgxpool.Pool, memberID string) (MemberDetail, bool, error) {
	var m MemberDetail
	err := pool.QueryRow(ctx, `
		SELECT members.id, members.family_id, families.name, members.first_name, members.last_name, members.member_type::text
		FROM members
		JOIN families ON families.id = members.family_id
		WHERE members.id = $1
	`, memberID).Scan(&m.ID, &m.FamilyID, &m.FamilyName, &m.FirstName, &m.LastName, &m.MemberType)
	if err != nil {
		return MemberDetail{}, false, nil //nolint:nilerr // "not found" is a normal, expected outcome
	}
	return m, true, nil
}
