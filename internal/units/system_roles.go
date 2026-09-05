package units

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

// The nine built-in roles, as something a leader can look at.
//
// systemRoleCapabilities (units.go) is the machine's answer to "what does
// this role grant". This file is the human's: the same information with
// labels, in a stable order, plus the ability for a unit to disagree with
// the default.
//
// Why overriding is allowed at all: the defaults encode one reading of
// how a Troop and a Pack divide responsibility, and real units differ. A
// Pack whose Committee Chair does the spending sign-off, a Troop that
// wants its Assistant Scoutmasters to authorize expenses — both were
// previously only reachable by minting a custom role that duplicated a
// built-in one, which left two roles meaning nearly the same thing and a
// roster where it mattered which you picked.
//
// Why an override is stored as a delta: a unit that never touches a role
// keeps following the code, so improving a default still reaches them.
// See migration 0045.

// SystemRole is one built-in role as the admin page shows it.
type SystemRole struct {
	Slug  string
	Label string
	// UnitTypes is which kinds of unit the role is normally assigned in.
	// Empty means both. Only used to sort the irrelevant ones out of the
	// way on screen — a Pack that has somehow assigned "patrol_leader"
	// still gets its capabilities resolved the same way.
	UnitTypes []string
	// Default is what the code grants, before any override.
	Default []string
	// Capabilities is what this unit actually grants — Default unless
	// overridden.
	Capabilities []string
	// Overridden marks a role this unit has deliberately changed, so the
	// page can show it and offer to put it back.
	Overridden bool
}

// systemRoleOrder lists the built-in roles the way a person thinks about
// them — most authority first, then the two everyone holds. The map in
// units.go is keyed for lookup and has no order; this is the display one.
var systemRoleOrder = []struct {
	slug      string
	label     string
	unitTypes []string
}{
	{"super_admin", "Site Administrator", nil},
	{"scoutmaster", "Scoutmaster", []string{"troop"}},
	{"assistant_scoutmaster", "Assistant Scoutmaster", []string{"troop"}},
	{"cubmaster", "Cubmaster", []string{"pack"}},
	{"den_leader", "Den Leader", []string{"pack"}},
	{"treasurer", "Treasurer", nil},
	{"senior_patrol_leader", "Senior Patrol Leader", []string{"troop"}},
	{"patrol_leader", "Patrol Leader", []string{"troop"}},
	{"parent", "Parent", nil},
	{"scout", "Scout", nil},
}

// SystemRoleLabel is how a built-in slug reads on screen, or the slug
// itself for anything else (a custom role, or a slug from an older
// install that no longer exists in code).
func SystemRoleLabel(slug string) string {
	for _, r := range systemRoleOrder {
		if r.slug == slug {
			return r.label
		}
	}
	return slug
}

// CapabilityLabel describes one capability in a sentence a leader can
// judge, rather than by its internal name.
func CapabilityLabel(capability string) string {
	switch capability {
	case CapEditContent:
		return "Edit content — homepage, news, gallery, calendar, roster (no approval needed)"
	case CapApproveSubmissions:
		return "Approve submissions — decide a pending Scout-submitted event or post"
	case CapSubmitForApproval:
		return "Submit for approval — can propose content and events, but they need approval first"
	case CapManageLedger:
		return "Manage the treasury — ledger, trip funds, fundraisers"
	case CapApproveExpenses:
		return "Authorize spending — sign off on a large expense the Treasurer entered"
	case CapSuperAdmin:
		return "Site settings — the highest tier; also lets this role create and edit other roles"
	}
	return capability
}

// appliesTo reports whether a role is one this kind of unit normally uses.
func (r SystemRole) AppliesTo(unitType string) bool {
	if len(r.UnitTypes) == 0 {
		return true
	}
	for _, t := range r.UnitTypes {
		if t == unitType {
			return true
		}
	}
	return false
}

// DefaultCapabilitiesForRole is what the code grants a built-in role,
// before any override — what "put it back to the default" restores.
func DefaultCapabilitiesForRole(slug string) []string {
	return append([]string(nil), systemRoleCapabilities[slug]...)
}

// SystemRolesForUnit returns every built-in role with the capabilities
// this unit actually grants it — the code's defaults, with any override
// applied.
func SystemRolesForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]SystemRole, error) {
	overrides, err := systemRoleOverrides(ctx, pool, unitID)
	if err != nil {
		return nil, err
	}

	out := make([]SystemRole, 0, len(systemRoleOrder))
	for _, r := range systemRoleOrder {
		def := append([]string(nil), systemRoleCapabilities[r.slug]...)
		sort.Strings(def)

		role := SystemRole{
			Slug:         r.slug,
			Label:        r.label,
			UnitTypes:    r.unitTypes,
			Default:      def,
			Capabilities: def,
		}
		if granted, ok := overrides[r.slug]; ok {
			sort.Strings(granted)
			role.Capabilities = granted
			role.Overridden = true
		}
		out = append(out, role)
	}
	return out, nil
}

// systemRoleOverrides loads this unit's deltas, keyed by slug. A role with
// no row is absent from the map — distinct from a role overridden to grant
// nothing, which is present and empty.
func systemRoleOverrides(ctx context.Context, pool *pgxpool.Pool, unitID string) (map[string][]string, error) {
	rows, err := pool.Query(ctx,
		`SELECT role_slug, capabilities FROM role_capability_overrides WHERE unit_id = $1`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]string{}
	for rows.Next() {
		var slug string
		var granted []string
		if err := rows.Scan(&slug, &granted); err != nil {
			return nil, err
		}
		if granted == nil {
			granted = []string{}
		}
		out[slug] = granted
	}
	return out, rows.Err()
}

// ErrCannotDisarmSuperAdmin refuses the one edit that cannot be undone
// from inside the site.
var ErrCannotDisarmSuperAdmin = errStr("units: the Site Administrator role must keep the site-settings capability — " +
	"removing it would leave nobody able to put it back")

type errStr string

func (e errStr) Error() string { return string(e) }

// SetSystemRoleCapabilities overrides what a built-in role grants in this
// unit, or clears the override when the requested set matches the code's
// default — so a leader who experiments and changes their mind ends up
// back on the default rather than on a frozen copy of it.
//
// The super_admin guard is the one hard rule. Every other capability can
// be granted and revoked freely because a Site Administrator can always
// grant it back; take site-settings away from the only role that has it
// and there is no longer anyone who can reach this page, which is a
// lockout no amount of clicking recovers from.
func SetSystemRoleCapabilities(ctx context.Context, pool *pgxpool.Pool, unitID, slug string, capabilities []string, actorID string) error {
	if _, ok := systemRoleCapabilities[slug]; !ok && !isKnownSystemRole(slug) {
		return errStr("units: not a built-in role")
	}

	granted := filterCapabilities(capabilities)
	if slug == "super_admin" && !containsCap(granted, CapSuperAdmin) {
		return ErrCannotDisarmSuperAdmin
	}

	if sameCapabilitySet(granted, systemRoleCapabilities[slug]) {
		_, err := pool.Exec(ctx,
			`DELETE FROM role_capability_overrides WHERE unit_id = $1 AND role_slug = $2`, unitID, slug)
		if err != nil {
			return err
		}
		logRoleChange(ctx, pool, unitID, slug, actorID, "reset", systemRoleCapabilities[slug], granted)
		return nil
	}

	_, err := pool.Exec(ctx, `
		INSERT INTO role_capability_overrides (unit_id, role_slug, capabilities, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (unit_id, role_slug)
		DO UPDATE SET capabilities = EXCLUDED.capabilities, updated_by = EXCLUDED.updated_by, updated_at = now()
	`, unitID, slug, granted, actorID)
	if err != nil {
		return err
	}
	logRoleChange(ctx, pool, unitID, slug, actorID, "update", systemRoleCapabilities[slug], granted)
	return nil
}

// logRoleChange records a built-in role override.
//
// The entity is the unit, not the role: audit_log.entity_id is a uuid
// column, and a built-in role has no id of its own — it is a slug in Go
// code, and its override row is keyed by (unit_id, role_slug) and deleted
// outright on a reset. Logging the unit is both what fits the column and
// what is true, since changing what Den Leader means here is a change to
// this unit's configuration. Which role, and what changed, are in the
// before/after payload where the activity log shows them.
func logRoleChange(ctx context.Context, pool *pgxpool.Pool, unitID, slug, actorID, action string, before, after []string) {
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "system_role",
		EntityID:   unitID,
		ActorID:    &actorID,
		Action:     action,
		Before:     map[string]any{"role": slug, "capabilities": filterCapabilities(before)},
		After:      map[string]any{"role": slug, "capabilities": after},
	})
}

func isKnownSystemRole(slug string) bool {
	for _, r := range systemRoleOrder {
		if r.slug == slug {
			return true
		}
	}
	return false
}

// filterCapabilities drops anything that isn't a capability name, so a
// hand-crafted form post can't write a value the CHECK constraint would
// reject (or, worse, one it wouldn't).
func filterCapabilities(in []string) []string {
	valid := make(map[string]bool, len(AllCapabilities))
	for _, c := range AllCapabilities {
		valid[c] = true
	}
	seen := map[string]bool{}
	out := []string{}
	for _, c := range in {
		if valid[c] && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

func containsCap(set []string, want string) bool {
	for _, c := range set {
		if c == want {
			return true
		}
	}
	return false
}

func sameCapabilitySet(a, b []string) bool {
	x, y := filterCapabilities(a), filterCapabilities(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
