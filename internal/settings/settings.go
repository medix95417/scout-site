// Package settings is a small, generic store for site-wide on/off
// toggles — configuration that affects the whole install (both units,
// every login), reviewable and changeable from /admin/settings without
// touching code or redeploying. See internal/db/migrations
// /0008_system_settings.sql for the schema rationale.
//
// A toggle not yet in the system_settings table reads as its Default —
// that's what lets a brand-new deployment behave correctly (every
// toggle off, unless its author chose otherwise) without a migration
// having to insert starting rows for every known key.
package settings

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

// ErrUnknownSetting is returned by Set for a key not present in Toggles.
var ErrUnknownSetting = errors.New("settings: unknown setting key")

// Known setting keys. Add a new one here (and to Toggles below) to add a
// new toggle to /admin/settings — no migration needed, since a missing
// row just reads as Default.
const (
	// RequireTwoFactorForAll broadens the two-factor "please set this up"
	// nudge banner (see internal/web's baseData.NeedsTwoFactorSetup) from
	// just Treasurer/super_admin logins to every logged-in user. It does
	// NOT lock anyone out — same non-lockout design as the
	// Treasurer-specific requirement it extends (see
	// PHASE2_TREASURY.md's two-factor section): nobody is ever blocked
	// from logging in for not having enrolled yet, only reminded. Once a
	// login does enroll (voluntarily or via this nudge), a code is
	// required at every subsequent login regardless of this setting —
	// that part isn't a toggle, it's just what "enrolled in two-factor"
	// means.
	RequireTwoFactorForAll = "require_two_factor_for_all"
)

// Toggle describes one setting for the /admin/settings page — the label
// and description a non-technical admin sees, and the value a brand-new
// install (or any key not yet written to system_settings) uses.
type Toggle struct {
	Key         string
	Label       string
	Description string
	Default     bool
}

// Toggles is every setting /admin/settings shows, in display order.
var Toggles = []Toggle{
	{
		Key:         RequireTwoFactorForAll,
		Label:       "Require two-factor authentication for everyone",
		Description: "Shows the two-factor setup reminder banner to every logged-in user, not just Treasurer and Admin logins. Nobody is ever blocked from logging in for not having set it up — this only broadens who gets reminded.",
		Default:     false,
	},
}

// Get reads one setting, returning its Default if no row is stored yet
// (or if key isn't a known Toggle — Default is then simply false, since
// there's nothing to default to).
func Get(ctx context.Context, pool *pgxpool.Pool, key string) (bool, error) {
	var value bool
	err := pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, key).Scan(&value)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return defaultFor(key), nil
		}
		return false, err
	}
	return value, nil
}

// All returns every known Toggle's current value (stored override, or
// its Default if unset) — what the /admin/settings page renders.
func All(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	values := make(map[string]bool, len(Toggles))
	for _, t := range Toggles {
		values[t.Key] = t.Default
	}

	rows, err := pool.Query(ctx, `SELECT key, value FROM system_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var value bool
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if _, known := values[key]; known {
			values[key] = value
		}
	}
	return values, rows.Err()
}

// Set writes a setting's value, audit-logged the same way every other
// admin change in this codebase is. Rejects unknown keys — the
// /admin/settings form only ever posts a key from Toggles, so an
// unrecognized one here means either a stale form or someone probing the
// endpoint directly, neither of which should silently create a row.
func Set(ctx context.Context, pool *pgxpool.Pool, key string, value bool, actorID string) error {
	if !isKnown(key) {
		return ErrUnknownSetting
	}

	var before any // nil if this key has never been set before
	var existingValue bool
	err := pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, key).Scan(&existingValue)
	if err == nil {
		before = existingValue
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	var id string
	err = pool.QueryRow(ctx, `
		INSERT INTO system_settings (key, value, updated_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now(), updated_by = EXCLUDED.updated_by
		RETURNING id
	`, key, value, actorID).Scan(&id)
	if err != nil {
		return err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "system_setting",
		EntityID:   id,
		ActorID:    &actorID,
		Action:     "update",
		Before:     map[string]any{"key": key, "value": before},
		After:      map[string]any{"key": key, "value": value},
	})
	return nil
}

func defaultFor(key string) bool {
	for _, t := range Toggles {
		if t.Key == key {
			return t.Default
		}
	}
	return false
}

func isKnown(key string) bool {
	for _, t := range Toggles {
		if t.Key == key {
			return true
		}
	}
	return false
}

// --- Per-unit toggles ---------------------------------------------------
//
// A sibling to the Toggle/Toggles above, but scoped to one unit (see
// migration 0014's unit_settings table) — for a setting Troop and Pack
// might reasonably answer differently, unlike a Toggle, which by design
// affects the whole install. Same shape and Get/All/Set pattern otherwise.

// AdvancementEnabled controls whether /advancement and /admin/advancement
// are reachable for a unit. Defaults to true (advancement tracking is
// part of the normal feature set) so existing units see no behavior
// change until an admin explicitly turns it off — e.g. because BSA
// national's own Scoutbook changes made this unit's own tracking
// redundant, while keeping the ability to flip it back on quickly if
// that changes again.
const AdvancementEnabled = "advancement_enabled"

// UnitToggle is a per-unit sibling of Toggle.
type UnitToggle struct {
	Key         string
	Label       string
	Description string
	Default     bool
}

// UnitToggles is every per-unit setting the "This Unit's Settings"
// section of /admin/settings shows, in display order.
var UnitToggles = []UnitToggle{
	{
		Key:         AdvancementEnabled,
		Label:       "Rank/badge advancement tracking",
		Description: "Shows /advancement and /admin/advancement for this unit. Turn off if you're tracking advancement elsewhere (e.g. Scoutbook) and don't need it duplicated here — the data isn't deleted, just hidden, so turning it back on picks up right where it left off.",
		Default:     true,
	},
}

// GetForUnit reads one per-unit setting, returning its Default if no row
// is stored yet for this unit.
func GetForUnit(ctx context.Context, pool *pgxpool.Pool, unitID, key string) (bool, error) {
	var value bool
	err := pool.QueryRow(ctx, `SELECT value FROM unit_settings WHERE unit_id = $1 AND key = $2`, unitID, key).Scan(&value)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return defaultForUnit(key), nil
		}
		return false, err
	}
	return value, nil
}

// AllForUnit returns every known UnitToggle's current value for a unit —
// what /admin/settings renders in its "This Unit's Settings" section.
func AllForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) (map[string]bool, error) {
	values := make(map[string]bool, len(UnitToggles))
	for _, t := range UnitToggles {
		values[t.Key] = t.Default
	}

	rows, err := pool.Query(ctx, `SELECT key, value FROM unit_settings WHERE unit_id = $1`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var value bool
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if _, known := values[key]; known {
			values[key] = value
		}
	}
	return values, rows.Err()
}

// SetForUnit writes a per-unit setting's value, audit-logged the same way
// Set does. Rejects unknown keys for the same reason Set does.
func SetForUnit(ctx context.Context, pool *pgxpool.Pool, unitID, key string, value bool, actorID string) error {
	if !isKnownUnitToggle(key) {
		return ErrUnknownSetting
	}

	var before any
	var existingValue bool
	err := pool.QueryRow(ctx, `SELECT value FROM unit_settings WHERE unit_id = $1 AND key = $2`, unitID, key).Scan(&existingValue)
	if err == nil {
		before = existingValue
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	var id string
	err = pool.QueryRow(ctx, `
		INSERT INTO unit_settings (unit_id, key, value, updated_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (unit_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now(), updated_by = EXCLUDED.updated_by
		RETURNING id
	`, unitID, key, value, actorID).Scan(&id)
	if err != nil {
		return err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "unit_setting",
		EntityID:   id,
		ActorID:    &actorID,
		Action:     "update",
		Before:     map[string]any{"key": key, "value": before},
		After:      map[string]any{"key": key, "value": value},
	})
	return nil
}

func defaultForUnit(key string) bool {
	for _, t := range UnitToggles {
		if t.Key == key {
			return t.Default
		}
	}
	return false
}

func isKnownUnitToggle(key string) bool {
	for _, t := range UnitToggles {
		if t.Key == key {
			return true
		}
	}
	return false
}

// --- Text settings -----------------------------------------------------
//
// A second, parallel key/value concept alongside the boolean Toggles
// above, for settings that need an actual value rather than an on/off
// switch — so far, just the non-secret half of SMTP configuration (host,
// port, username, from address), added here specifically so a
// non-technical admin can set those from /admin/settings instead of
// editing environment variables and restarting (see
// internal/mailer.Mailer, which resolves these at send time). Kept as a
// separate set of functions/types rather than generalizing Toggle to hold
// either kind of value — a bool and a string mean different things to a
// template ("is this on" vs. "what's this set to"), and forcing both
// through one shape would make both harder to read.
//
// A text setting's absence (no row, or a row with value_text NULL) reads
// as "", which callers treat as "not overridden — fall back to whatever
// the environment/default provides" — see internal/mailer's effective
// config resolution. Unlike a Toggle, there's no meaningful "default"
// value baked into TextSetting itself; the fallback is context-specific
// (an env var, for SMTP) and lives with the caller, not here.

// Known text setting keys — see internal/db/migrations
// /0009_member_logins_and_text_settings.sql for the value_text column
// these are stored in, and internal/config for the SMTP_* environment
// variables that still take priority for SMTP_PASSWORD only (see
// README.md — the SMTP password deliberately never gets a DB-editable
// counterpart, so there's no SMTPPassword key here).
const (
	SMTPHost     = "smtp_host"
	SMTPPort     = "smtp_port"
	SMTPUsername = "smtp_username"
	SMTPFrom     = "smtp_from"
)

// TextSetting describes one string-valued setting for the /admin/settings
// page.
type TextSetting struct {
	Key         string
	Label       string
	Description string
	Placeholder string // shown in the empty input — hints what happens if left blank
}

// TextSettings is every text setting /admin/settings shows, in display
// order — currently just SMTP host/port/username/from. Deliberately
// excludes the SMTP password: see the package comment above and
// README.md/SECURITY_AUDIT.md for why that one stays environment-variable
// only rather than gaining a database-backed counterpart.
var TextSettings = []TextSetting{
	{
		Key:         SMTPHost,
		Label:       "SMTP server host",
		Description: "e.g. smtp.mailgun.org or smtp.gmail.com. Leave blank to fall back to the SMTP_HOST environment variable (or to leave email disabled, if that's blank too).",
		Placeholder: "smtp.example.com",
	},
	{
		Key:         SMTPPort,
		Label:       "SMTP server port",
		Description: "587 for STARTTLS (the common case) or 465 for implicit TLS. Leave blank to fall back to SMTP_PORT (default 587).",
		Placeholder: "587",
	},
	{
		Key:         SMTPUsername,
		Label:       "SMTP username",
		Description: "Leave blank to fall back to SMTP_USERNAME (or to send without authentication, if your provider allows it).",
		Placeholder: "",
	},
	{
		Key:         SMTPFrom,
		Label:       "From address",
		Description: `The address (and optional display name) outgoing mail is sent from — e.g. "Troop 47 <noreply@47-yonkers.org>". Leave blank to fall back to SMTP_FROM.`,
		Placeholder: "Troop 47 <noreply@example.org>",
	},
}

// isKnownText reports whether key is one of TextSettings.
func isKnownText(key string) bool {
	for _, t := range TextSettings {
		if t.Key == key {
			return true
		}
	}
	return false
}

// GetText reads one text setting's stored override, returning "" if it's
// never been set (no row, or a row with value_text NULL) — the caller's
// signal to fall back to its own default (see internal/mailer).
func GetText(ctx context.Context, pool *pgxpool.Pool, key string) (string, error) {
	var value *string
	err := pool.QueryRow(ctx, `SELECT value_text FROM system_settings WHERE key = $1`, key).Scan(&value)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	if value == nil {
		return "", nil
	}
	return *value, nil
}

// AllText returns every known TextSetting's current stored override (""
// for anything unset) — what the /admin/settings page renders.
func AllText(ctx context.Context, pool *pgxpool.Pool) (map[string]string, error) {
	values := make(map[string]string, len(TextSettings))
	for _, t := range TextSettings {
		values[t.Key] = ""
	}

	rows, err := pool.Query(ctx, `SELECT key, value_text FROM system_settings WHERE value_text IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if _, known := values[key]; known {
			values[key] = value
		}
	}
	return values, rows.Err()
}

// SetText writes a text setting's value, audit-logged the same way Set
// does for a boolean toggle. Rejects unknown keys, same reasoning as Set.
// An all-whitespace/empty value is stored as NULL (equivalent to "never
// set" — GetText returns "" either way) rather than an empty string, so
// clearing the field in the admin form actually clears the override
// instead of leaving a "" row that's indistinguishable from unset but
// technically present.
func SetText(ctx context.Context, pool *pgxpool.Pool, key, value, actorID string) error {
	if !isKnownText(key) {
		return ErrUnknownSetting
	}
	trimmed := strings.TrimSpace(value)

	var before any
	var existing *string
	err := pool.QueryRow(ctx, `SELECT value_text FROM system_settings WHERE key = $1`, key).Scan(&existing)
	if err == nil {
		if existing != nil {
			before = *existing
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	var textArg any
	if trimmed != "" {
		textArg = trimmed
	}

	var id string
	err = pool.QueryRow(ctx, `
		INSERT INTO system_settings (key, value_text, updated_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value_text = EXCLUDED.value_text, updated_at = now(), updated_by = EXCLUDED.updated_by
		RETURNING id
	`, key, textArg, actorID).Scan(&id)
	if err != nil {
		return err
	}

	var after any
	if trimmed != "" {
		after = trimmed
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "system_setting",
		EntityID:   id,
		ActorID:    &actorID,
		Action:     "update_text",
		Before:     map[string]any{"key": key, "value": before},
		After:      map[string]any{"key": key, "value": after},
	})
	return nil
}
