// Package units resolves which "container" (Troop or Pack) a request
// belongs to, and answers role/permission questions. This is the package
// that makes the modular-monolith design real: one running binary, but every
// request is scoped to a single unit from the moment it's resolved here.
package units

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Unit struct {
	ID         string
	Slug       string
	Name       string
	UnitType   string // "troop" | "pack"
	Hostname   string
	ThemeColor string
	LogoURL    string
}

// ByHostname resolves a Unit from the incoming request's Host header.
// Returns (Unit{}, false, nil) if no unit matches — callers should render a
// 404 or a generic landing page rather than treat it as a server error,
// since it just means DNS/Caddy is pointing somewhere the app doesn't know
// about yet (e.g. mid-setup of a new subdomain).
func ByHostname(ctx context.Context, pool *pgxpool.Pool, host string) (Unit, bool, error) {
	// Strip a port if present (e.g. "troop.47-yonkers.org:8080" during local dev).
	if i := strings.IndexByte(host, ':'); i != -1 {
		host = host[:i]
	}

	var u Unit
	err := pool.QueryRow(ctx, `
		SELECT id, slug, name, unit_type::text, hostname, theme_color, COALESCE(logo_url, '')
		FROM units WHERE hostname = $1
	`, host).Scan(&u.ID, &u.Slug, &u.Name, &u.UnitType, &u.Hostname, &u.ThemeColor, &u.LogoURL)

	if err != nil {
		return Unit{}, false, nil //nolint:nilerr // "no unit for this host" is a normal, expected outcome
	}
	return u, true, nil
}

// BySlug resolves a Unit by its slug (e.g. "troop-47", "pack-47") — used by
// admin/ops tooling (see internal/bootstrap.GrantRole) where there's no
// incoming request/Host header to resolve from, unlike ByHostname.
func BySlug(ctx context.Context, pool *pgxpool.Pool, slug string) (Unit, bool, error) {
	var u Unit
	err := pool.QueryRow(ctx, `
		SELECT id, slug, name, unit_type::text, hostname, theme_color, COALESCE(logo_url, '')
		FROM units WHERE slug = $1
	`, slug).Scan(&u.ID, &u.Slug, &u.Name, &u.UnitType, &u.Hostname, &u.ThemeColor, &u.LogoURL)

	if err != nil {
		return Unit{}, false, nil //nolint:nilerr // "no unit with that slug" is a normal, expected outcome
	}
	return u, true, nil
}

type contextKey string

const unitContextKey contextKey = "current_unit"

// Middleware resolves the unit for every request and attaches it to the
// context. Mount this near the top of the middleware chain — most other
// packages assume UnitFromContext will succeed.
func Middleware(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			unit, ok, err := ByHostname(r.Context(), pool, r.Host)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if !ok {
				http.Error(w, fmt.Sprintf("no site configured for host %q", r.Host), http.StatusNotFound)
				return
			}
			ctx := contextWithUnit(r.Context(), unit)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func contextWithUnit(ctx context.Context, u Unit) context.Context {
	return context.WithValue(ctx, unitContextKey, u)
}

// UnitFromContext returns the unit resolved for this request.
func UnitFromContext(ctx context.Context) (Unit, bool) {
	u, ok := ctx.Value(unitContextKey).(Unit)
	return u, ok
}

// Role-checking helpers -----------------------------------------------------

// AdultLeaderRoles are the roles that can create/edit content and approve
// youth submissions without needing approval themselves, per the
// requirements doc's permission table.
var AdultLeaderRoles = map[string]bool{
	"cubmaster":             true,
	"den_leader":            true,
	"scoutmaster":           true,
	"assistant_scoutmaster": true,
	"super_admin":           true,
}

// ApproverRoles are the roles allowed to approve a pending SPL/Patrol
// Leader submission — any Scoutmaster or Assistant Scoutmaster, per the
// requirements doc.
var ApproverRoles = map[string]bool{
	"scoutmaster":           true,
	"assistant_scoutmaster": true,
	"super_admin":           true,
}

// TreasuryRoles are the roles allowed to manage a unit's ledger — general
// fund, individual Scout accounts, trip funds, and fundraisers. Phase 2
// (scout-website-requirements.md Section 3.4) is explicit that the
// Treasurer role is what carries "access to that unit's full accounting
// records" — deliberately narrower than AdultLeaderRoles, since a
// Scoutmaster or Den Leader having content-edit rights doesn't imply
// money access.
var TreasuryRoles = map[string]bool{
	"treasurer":   true,
	"super_admin": true,
}

// RolesForMemberInUnit returns every role a member holds within a unit
// (across all sub-groups) — a member can hold more than one (e.g. a parent
// who is also a Den Leader).
func RolesForMemberInUnit(ctx context.Context, pool *pgxpool.Pool, memberID, unitID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT role::text FROM role_assignments WHERE member_id = $1 AND unit_id = $2
	`, memberID, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

// CanEditUnitContent reports whether any of the given roles grants
// unrestricted (no-approval-needed) content/calendar edit access.
func CanEditUnitContent(roles []string) bool {
	for _, r := range roles {
		if AdultLeaderRoles[r] {
			return true
		}
	}
	return false
}

// CanSubmitForApproval reports whether any of the given roles grants
// restricted (approval-required) submission access — SPL and Patrol Leader.
func CanSubmitForApproval(roles []string) bool {
	for _, r := range roles {
		if r == "senior_patrol_leader" || r == "patrol_leader" {
			return true
		}
	}
	return false
}

// CanApprove reports whether any of the given roles can approve a pending
// submission (any SM/ASM, per the requirements doc).
func CanApprove(roles []string) bool {
	for _, r := range roles {
		if ApproverRoles[r] {
			return true
		}
	}
	return false
}

// CanManageLedger reports whether any of the given roles grants access to
// a unit's ledger — viewing balances, entering transactions, deciding
// pending trip-fund transfers, and managing fundraisers.
func CanManageLedger(roles []string) bool {
	for _, r := range roles {
		if TreasuryRoles[r] {
			return true
		}
	}
	return false
}

// IsSuperAdmin reports whether any of the given roles is super_admin —
// the gate for the site-wide settings page (internal/web/settings_admin.go),
// which is deliberately narrower than CanManageLedger/CanEditUnitContent:
// those cover one unit's content or books, this covers configuration that
// affects the whole install (both units, every login).
func IsSuperAdmin(roles []string) bool {
	for _, r := range roles {
		if r == "super_admin" {
			return true
		}
	}
	return false
}

// FamilyHasAnyTreasuryRole reports whether any member of a family holds a
// TreasuryRoles role (Treasurer or super_admin) in ANY unit — used to
// decide whether a login needs two-factor authentication (see
// internal/twofactor). Checked per-login rather than per-unit, since this
// site's single sign-on means one login session already spans both
// subdomains regardless of which one a Treasurer happens to hold the role
// in.
func FamilyHasAnyTreasuryRole(ctx context.Context, pool *pgxpool.Pool, familyID string) (bool, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT role_assignments.role::text
		FROM role_assignments
		JOIN members ON members.id = role_assignments.member_id
		WHERE members.family_id = $1
	`, familyID)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return false, err
		}
		if TreasuryRoles[role] {
			return true, nil
		}
	}
	return false, rows.Err()
}

// MemberHasAnyTreasuryRole reports whether one specific member holds a
// TreasuryRoles role (Treasurer or super_admin) in ANY unit — the
// member-scoped sibling of FamilyHasAnyTreasuryRole, used for the
// two-factor nudge banner when the current login is an individual member
// login (see internal/auth.User.MemberID) rather than a family-wide one:
// an individual Scout login should be nudged based on roles that member
// personally holds, not roles anyone else in their family holds.
func MemberHasAnyTreasuryRole(ctx context.Context, pool *pgxpool.Pool, memberID string) (bool, error) {
	rows, err := pool.Query(ctx, `SELECT DISTINCT role::text FROM role_assignments WHERE member_id = $1`, memberID)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return false, err
		}
		if TreasuryRoles[role] {
			return true, nil
		}
	}
	return false, rows.Err()
}

// RolesForFamilyInUnit returns the union of roles held by any member of a
// family within a unit. Phase 1 logs in at the family level (one account
// per family, per the requirements doc), so a family's effective
// permissions in a unit are the roles held by whichever of its members —
// almost always an adult leader — carries a role there. A family with no
// members holding leadership roles simply gets the implicit "parent" view.
func RolesForFamilyInUnit(ctx context.Context, pool *pgxpool.Pool, familyID, unitID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT role_assignments.role::text
		FROM role_assignments
		JOIN members ON members.id = role_assignments.member_id
		WHERE members.family_id = $1 AND role_assignments.unit_id = $2
	`, familyID, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}
