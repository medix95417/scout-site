// Package units resolves which "container" (Troop or Pack) a request
// belongs to, and answers role/permission questions. This is the package
// that makes the modular-monolith design real: one running binary, but every
// request is scoped to a single unit from the moment it's resolved here.
package units

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Unit struct {
	ID          string
	Slug        string
	Name        string
	UnitType    string // "troop" | "pack"
	Hostname    string
	ThemeColor  string // primary/structural color — header, hero bands, calendar highlights
	AccentColor string // action color — buttons and other calls-to-action
	LogoURL     string
}

// AccentTextColor returns the text color a button using AccentColor as its
// background should use, so every accent-colored button clears WCAG AA's
// contrast minimum regardless of which program color a unit's accent
// happens to be. Scouting Red (#CE1126) contrasts fine with white — but
// Cub Scouts' mandated accent, Cub Scout Yellow (#FDC116), is a light
// color: white text on it fails badly (~1.6:1, nowhere near the 3:1 a
// button's text needs). Computed from AccentColor's actual relative
// luminance (WCAG's own formula) rather than hardcoded per unit type, so
// this keeps working correctly if a unit's accent color ever changes.
func (u Unit) AccentTextColor() string {
	const darkText = "#111827" // Tailwind gray-900 — this site's standard body text color
	const lightText = "#ffffff"

	r, g, b, ok := parseHexColor(u.AccentColor)
	if !ok {
		return lightText // unparseable color: fall back to the old, previously-universal behavior
	}

	// WCAG 2.x relative luminance and contrast ratio — see
	// https://www.w3.org/TR/WCAG21/#dfn-relative-luminance.
	linearize := func(c float64) float64 {
		c /= 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	luminance := 0.2126*linearize(r) + 0.7152*linearize(g) + 0.0722*linearize(b)
	contrastWithWhite := 1.05 / (luminance + 0.05)
	contrastWithDark := (luminance + 0.05) / 0.05

	if contrastWithWhite >= contrastWithDark {
		return lightText
	}
	return darkText
}

// parseHexColor parses a "#rrggbb" string into 0-255 float components.
// Returns ok=false for anything else (missing "#", wrong length, non-hex
// digits) — every AccentColor in this codebase is set by an admin/
// migration, never by a site visitor, so a malformed one is a
// configuration bug, not something to render an error page over.
func parseHexColor(hex string) (r, g, b float64, ok bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, 0, 0, false
	}
	rv, err1 := strconv.ParseUint(hex[1:3], 16, 8)
	gv, err2 := strconv.ParseUint(hex[3:5], 16, 8)
	bv, err3 := strconv.ParseUint(hex[5:7], 16, 8)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return float64(rv), float64(gv), float64(bv), true
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
		SELECT id, slug, name, unit_type::text, hostname, theme_color, accent_color, COALESCE(logo_url, '')
		FROM units WHERE hostname = $1
	`, host).Scan(&u.ID, &u.Slug, &u.Name, &u.UnitType, &u.Hostname, &u.ThemeColor, &u.AccentColor, &u.LogoURL)

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
		SELECT id, slug, name, unit_type::text, hostname, theme_color, accent_color, COALESCE(logo_url, '')
		FROM units WHERE slug = $1
	`, slug).Scan(&u.ID, &u.Slug, &u.Name, &u.UnitType, &u.Hostname, &u.ThemeColor, &u.AccentColor, &u.LogoURL)

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

// Capability model -----------------------------------------------------
//
// A role — one of the 9 fixed, code-defined slugs below, or a per-unit
// custom_roles row a super_admin created on the fly — grants zero or more
// of these five capabilities. Permission checks (CanEditUnitContent,
// CanManageLedger, etc.) all operate on a resolved capability set, not
// role names directly, so a custom role sharing a capability with a fixed
// role (e.g. a "Committee Chair" custom role granted CapManageLedger) is
// indistinguishable from a Treasurer to every permission check in the
// codebase — same as the requirements doc's permission table intends.
const (
	CapEditContent        = "edit_content"        // create/edit content & calendar without needing approval
	CapApproveSubmissions = "approve_submissions" // decide a pending SPL/Patrol-Leader submission
	CapSubmitForApproval  = "submit_for_approval" // can submit content/events, but they need approval first
	CapManageLedger       = "manage_ledger"       // fund accounting — ledger, trip funds, fundraisers
	CapApproveExpenses    = "approve_expenses"    // authorize a large expense a Treasurer entered — see CanApproveExpenses
	CapSuperAdmin         = "super_admin"         // site-wide settings; minting/granting other capabilities
)

// AllCapabilities lists every capability a custom role can be granted —
// the checkbox list on the "create a custom role" admin form iterates
// this, and custom_roles' CHECK constraint mirrors it.
var AllCapabilities = []string{CapEditContent, CapApproveSubmissions, CapSubmitForApproval, CapManageLedger, CapApproveExpenses, CapSuperAdmin}

// systemRoleCapabilities maps each of the 9 fixed, code-defined role
// slugs (see migration 0001_init.sql's original member_role enum — the
// column is now plain text, but these specific slugs' meaning is
// unchanged) to the capabilities they grant. This is the single source of
// truth for what used to be three separate AdultLeaderRoles/ApproverRoles/
// TreasuryRoles maps — consolidated so there's one place to look up "what
// can this role do," matching how a custom role's capabilities are looked
// up (see custom_roles.capabilities).
var systemRoleCapabilities = map[string][]string{
	"cubmaster":   {CapEditContent, CapApproveExpenses},
	"den_leader":  {CapEditContent},
	"scoutmaster": {CapEditContent, CapApproveSubmissions, CapApproveExpenses},
	// Deliberately NOT CapApproveExpenses: the unit's top leader signs off
	// on spending. A unit that wants an ASM to cover can grant the
	// capability through a custom role (see custom_roles.capabilities).
	"assistant_scoutmaster": {CapEditContent, CapApproveSubmissions},
	"senior_patrol_leader":  {CapSubmitForApproval},
	"patrol_leader":         {CapSubmitForApproval},
	"treasurer":             {CapManageLedger},
	"super_admin":           {CapEditContent, CapApproveSubmissions, CapManageLedger, CapApproveExpenses, CapSuperAdmin},
	// "parent" and "scout" intentionally grant nothing extra.
}

// ReservedRoleSlugs are the fixed system role slugs — a custom role can't
// reuse one of these (see roster.CreateCustomRole), since doing so would
// make role_assignments.role ambiguous between "the fixed role with this
// meaning" and "this unit's custom role with a possibly different one."
var ReservedRoleSlugs = func() map[string]bool {
	m := make(map[string]bool, len(systemRoleCapabilities))
	for slug := range systemRoleCapabilities {
		m[slug] = true
	}
	return m
}()

// SystemRolesWithCapability returns the fixed role slugs that grant
// capability, in sorted order. The inverse of the systemRoleCapabilities
// map above, for callers asking "who can do this" rather than "what can
// this person do" — see roster.MembersWithCapability.
func SystemRolesWithCapability(capability string) []string {
	var slugs []string
	for slug, granted := range systemRoleCapabilities {
		for _, c := range granted {
			if c == capability {
				slugs = append(slugs, slug)
				break
			}
		}
	}
	sort.Strings(slugs)
	return slugs
}

// Capabilities is a resolved set of capabilities a member/family holds in
// a unit — what CanEditUnitContent and friends check against.
type Capabilities map[string]bool

func (c Capabilities) has(capability string) bool { return c[capability] }

// Has is the exported form, for callers that hold a capability as a
// string rather than calling one of the named predicates below —
// internal/help gates each help topic on a units.Cap* value carried in
// the topic itself, so it can't use CanEditUnitContent and friends.
// Prefer the named predicates where one fits; they document intent at
// the call site in a way a bare string can't.
func (c Capabilities) Has(capability string) bool { return c.has(capability) }

// CapabilitiesForRoles resolves a set of role slugs (from
// RolesForMemberInUnit/RolesForFamilyInUnit) into the capabilities they
// grant — system roles via the fixed map above, anything else via a
// custom_roles lookup scoped to unitID.
func CapabilitiesForRoles(ctx context.Context, pool *pgxpool.Pool, unitID string, roles []string) (Capabilities, error) {
	caps := make(Capabilities)
	var customSlugs []string
	for _, role := range roles {
		if granted, ok := systemRoleCapabilities[role]; ok {
			for _, c := range granted {
				caps[c] = true
			}
			continue
		}
		customSlugs = append(customSlugs, role)
	}
	if len(customSlugs) == 0 {
		return caps, nil
	}

	rows, err := pool.Query(ctx, `
		SELECT capabilities FROM custom_roles WHERE unit_id = $1 AND slug = ANY($2)
	`, unitID, customSlugs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var granted []string
		if err := rows.Scan(&granted); err != nil {
			return nil, err
		}
		for _, c := range granted {
			caps[c] = true
		}
	}
	return caps, rows.Err()
}

// RolesForMemberInUnit returns every role a member holds within a unit
// (across all sub-groups) — a member can hold more than one (e.g. a parent
// who is also a Den Leader).
func RolesForMemberInUnit(ctx context.Context, pool *pgxpool.Pool, memberID, unitID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT role_assignments.role::text
		FROM role_assignments
		JOIN members ON members.id = role_assignments.member_id
		WHERE role_assignments.member_id = $1 AND role_assignments.unit_id = $2
		  AND members.active
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

// CanEditUnitContent reports whether caps grants unrestricted
// (no-approval-needed) content/calendar edit access.
func CanEditUnitContent(caps Capabilities) bool { return caps.has(CapEditContent) }

// CanSubmitForApproval reports whether caps grants restricted
// (approval-required) submission access — SPL and Patrol Leader, or a
// custom role granted the same capability.
func CanSubmitForApproval(caps Capabilities) bool { return caps.has(CapSubmitForApproval) }

// CanApprove reports whether caps can approve a pending submission (any
// SM/ASM, per the requirements doc, or an equivalent custom role).
func CanApprove(caps Capabilities) bool { return caps.has(CapApproveSubmissions) }

// CanManageLedger reports whether caps grants access to a unit's ledger —
// viewing balances, entering transactions, deciding pending trip-fund
// transfers, and managing fundraisers.
func CanManageLedger(caps Capabilities) bool { return caps.has(CapManageLedger) }

// CanApproveExpenses reports whether caps can authorize a large expense
// a Treasurer has entered (see settings.ExpenseApprovalThresholdCents).
//
// Deliberately separate from CapManageLedger, and never held alongside it
// by the same system role: the whole point of the control is that the
// person spending the money isn't the person approving it. Granted to the
// unit's top leader — Cubmaster in a Pack, Scoutmaster in a Troop — plus
// super_admin, which needs to be able to unblock a unit whose leader is
// unavailable.
func CanApproveExpenses(caps Capabilities) bool { return caps.has(CapApproveExpenses) }

// IsSuperAdmin reports whether caps includes the super_admin capability —
// the gate for the site-wide settings page (internal/web/settings_admin.go)
// and for minting/granting capabilities via a new custom role, deliberately
// narrower than CanManageLedger/CanEditUnitContent: those cover one unit's
// content or books, this covers configuration that affects the whole
// install (both units, every login) or hands out more access.
func IsSuperAdmin(caps Capabilities) bool { return caps.has(CapSuperAdmin) }

// FamilyHasAnyTreasuryRole reports whether any member of a family holds a
// role with the CapManageLedger capability (Treasurer, super_admin, or an
// equivalent custom role) in ANY unit — used to decide whether a login
// needs two-factor authentication (see internal/twofactor). Checked
// per-login rather than per-unit, since this site's single sign-on means
// one login session already spans both subdomains regardless of which one
// a Treasurer happens to hold the role in.
func FamilyHasAnyTreasuryRole(ctx context.Context, pool *pgxpool.Pool, familyID string) (bool, error) {
	rolesByUnit, err := rolesByUnitFor(ctx, pool, `SELECT role_assignments.unit_id, role_assignments.role
		FROM role_assignments JOIN members ON members.id = role_assignments.member_id
		WHERE members.family_id = $1`, familyID)
	if err != nil {
		return false, err
	}
	return anyUnitHasCapability(ctx, pool, rolesByUnit, CapManageLedger)
}

// MemberHasAnyTreasuryRole reports whether one specific member holds a
// role with the CapManageLedger capability in ANY unit — the
// member-scoped sibling of FamilyHasAnyTreasuryRole, used for the
// two-factor nudge banner when the current login is an individual member
// login (see internal/auth.User.MemberID) rather than a family-wide one:
// an individual Scout login should be nudged based on roles that member
// personally holds, not roles anyone else in their family holds.
func MemberHasAnyTreasuryRole(ctx context.Context, pool *pgxpool.Pool, memberID string) (bool, error) {
	rolesByUnit, err := rolesByUnitFor(ctx, pool, `SELECT unit_id, role FROM role_assignments WHERE member_id = $1`, memberID)
	if err != nil {
		return false, err
	}
	return anyUnitHasCapability(ctx, pool, rolesByUnit, CapManageLedger)
}

// rolesByUnitFor runs a query returning (unit_id, role) pairs and groups
// the roles by unit — the shared plumbing behind FamilyHasAnyTreasuryRole
// and MemberHasAnyTreasuryRole, which both need to resolve capabilities
// per-unit (a custom role's capabilities are scoped to the unit that
// defined it) across however many units the family/member holds roles in.
func rolesByUnitFor(ctx context.Context, pool *pgxpool.Pool, query string, arg string) (map[string][]string, error) {
	rows, err := pool.Query(ctx, query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byUnit := make(map[string][]string)
	for rows.Next() {
		var unitID, role string
		if err := rows.Scan(&unitID, &role); err != nil {
			return nil, err
		}
		byUnit[unitID] = append(byUnit[unitID], role)
	}
	return byUnit, rows.Err()
}

// anyUnitHasCapability reports whether any unit's resolved capability set
// includes capability.
func anyUnitHasCapability(ctx context.Context, pool *pgxpool.Pool, rolesByUnit map[string][]string, capability string) (bool, error) {
	for unitID, roles := range rolesByUnit {
		caps, err := CapabilitiesForRoles(ctx, pool, unitID, roles)
		if err != nil {
			return false, err
		}
		if caps.has(capability) {
			return true, nil
		}
	}
	return false, nil
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
		  AND members.active
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
