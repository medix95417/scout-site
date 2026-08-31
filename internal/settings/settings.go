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
	"regexp"
	"strconv"
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

	// PasswordResetEnabled controls whether the self-service "Forgot your
	// password?" flow (/forgot-password) sends a reset email at all.
	// Defaults to true — existing installs keep working exactly as before
	// until an admin explicitly turns this off, e.g. because a unit wants
	// every reset to go through a leader (see /admin/roster's own
	// leader-triggered reset, which is unaffected either way — that's a
	// separate code path). Site-wide, not per-unit: both Troop and Pack
	// share one login system, so a token issued before this was turned off
	// still works — this only stops new reset emails from going out.
	PasswordResetEnabled = "password_reset_enabled"
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
	{
		Key:         PasswordResetEnabled,
		Label:       "Allow self-service email password reset",
		Description: "On (default): the \"Forgot your password?\" link on the login page emails a reset link. Off: that link explains resets are turned off and points to a leader instead — a leader can still reset anyone's password directly from /admin/roster either way. A reset link already sent before this is turned off keeps working.",
		Default:     true,
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

	// value IS NOT NULL excludes a text-setting row (see TextSettings
	// below) — since migration 0009 relaxed value to nullable so a
	// text-setting row doesn't need a meaningless boolean placeholder,
	// this table can hold both kinds of row, and this query only wants
	// the boolean ones.
	rows, err := pool.Query(ctx, `SELECT key, value FROM system_settings WHERE value IS NOT NULL`)
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

// ScoutAccountSelfService controls whether a family/individual login can
// view its own Scout ledger account page (the self-service side of
// /treasury/accounts/{id}). Defaults to true (families can see their own
// balances). When off, only a Treasurer/super_admin can open an account
// page — the account still exists and the treasury side is unchanged, the
// individual/family self-service view is just shut off (and the
// "My Accounts" nav link hidden). Lets a unit that doesn't want families
// browsing balances turn that off without dropping the treasury feature.
const ScoutAccountSelfService = "scout_account_self_service"

// ProspectFormEnabled controls whether the public "interested in
// joining" form at /join is served. Defaults to true.
//
// Worth having as a switch rather than always-on because it is the only
// route besides the fundraiser storefront where a member of the public
// can write to this database, and a unit that isn't recruiting — or that
// is being flooded with spam — needs a way to close it that doesn't
// involve a deploy. Turning it off hides the form and 404s the route;
// enquiries already received stay on /admin/prospects.
const ProspectFormEnabled = "prospect_form_enabled"

// PermissionSlipsEnabled is the feature's master switch, distinct from
// PermissionSlipEnforcement below (which only narrows *which* events show
// a slip while the feature is on). Defaults to true, so a unit already
// using slips sees no change.
//
// Off means off for everyone, with no leader escape hatch: no link on any
// event on /calendar for a family, a logged-out visitor, or a leader, and
// every /calendar/{id}/permission-slip route returns 404 — including for
// an event a leader flagged as requiring one, or already attached a real
// slip to. That last part is the point of the setting rather than an edge
// case: a unit that collects slips on paper doesn't want a half-finished
// digital slip reachable by URL, and it especially doesn't want one
// listed on the calendar a prospective family is reading.
//
// Nothing underneath is deleted — slips and the signatures already
// collected stay in the database exactly as they are, and turning the
// feature back on restores them. Same treatment as TreasuryEnabled.
const PermissionSlipsEnabled = "permission_slips_enabled"

// PermissionSlipEnforcement controls whether the "Permission slip" link on
// /calendar only shows for events a leader has explicitly marked as
// requiring one (see calendar.Event.RequiresPermissionSlip) — a weekly
// meeting doesn't need it cluttering every single event. Defaults to
// false: every event keeps showing the link, same as before this existed,
// so nothing changes until a unit opts in. A leader (CanEditUnitContent)
// can always reach an event's permission-slip page regardless of this
// setting or the event's own flag — there's no way to edit an event after
// creation yet, so a leader who forgot to check the box still needs an
// escape hatch to attach a slip after the fact.
const PermissionSlipEnforcement = "permission_slip_enforcement"

// TreasuryEnabled controls whether the Treasury area (/treasury and its
// sub-pages, including the fundraiser storefront's admin management) and
// the family self-service "My Accounts" view (/accounts) are reachable
// for a unit. Defaults to true (fund accounting is part of the normal
// feature set) so existing units see no behavior change until an admin
// explicitly turns it off — e.g. a unit that doesn't handle its own
// finances through this site. Turning it off doesn't touch any ledger
// data underneath (accounts, balances, transaction history, fundraisers,
// items, orders all stay exactly as they are); it just closes every entry
// point to that data, the same non-destructive shape as
// AdvancementEnabled below — including the public fundraiser storefront
// page and homepage button, since those exist to feed the same ledger
// this toggle gates. A Treasurer/super_admin gets the same "not
// available" message as anyone else while this is off; it isn't a
// per-role exception like ScoutAccountSelfService below, since disabling
// the whole function has to actually mean off.
const TreasuryEnabled = "treasury_enabled"

// NewsletterEnabled controls whether /admin/newsletters (and sending) is
// reachable for a unit. Defaults to true (newsletters are part of the
// normal feature set) so existing units see no behavior change until an
// admin explicitly turns it off — e.g. because a unit sends its
// newsletter through some other system and doesn't want this one
// duplicating it. Already-sent newsletters and their audit history aren't
// deleted, just the admin UI for managing/sending new ones is hidden —
// same non-destructive shape as AdvancementEnabled above.
const NewsletterEnabled = "newsletter_enabled"

// Payments toggles — whether this unit accepts online payments through
// each processor. Off by default: a unit shouldn't start accepting real
// payments just because it upgraded to a version of the app that added
// this feature, and (per PaymentsStripeEnabled/PaymentsPayPalEnabled's
// own gate) enabling one is only meaningful once its credentials are
// actually filled in below.
const (
	PaymentsStripeEnabled = "payments_stripe_enabled"
	PaymentsPayPalEnabled = "payments_paypal_enabled"
	// SocialFacebookEnabled/SocialInstagramEnabled/SocialTikTokEnabled gate
	// whether each social link (see SocialFacebookURL etc. below) actually
	// shows in the footer and on the homepage. Default true so a unit that
	// already had one of these URLs set (via the old /admin/home editor,
	// before this moved here — see content.LegacySocialURL) keeps showing
	// it with no extra action; turning one off hides it without clearing
	// the URL underneath.
	SocialFacebookEnabled  = "social_facebook_enabled"
	SocialInstagramEnabled = "social_instagram_enabled"
	SocialTikTokEnabled    = "social_tiktok_enabled"
	// PayPalLiveMode picks which of PayPal's two separate API environments
	// PayPalClientID/PayPalClientSecret are checked against — sandbox
	// (default; PayPal's test environment, no real money moves) or live.
	// PayPal, unlike Stripe, uses entirely separate credentials per
	// environment rather than a key prefix that implies it, so the app
	// needs this told to it explicitly to know which API base URL to call.
	PayPalLiveMode = "payments_paypal_live_mode"
)

// UnitToggle is a per-unit sibling of Toggle. Section groups related
// toggles under their own heading on /admin/settings — "" renders under
// the generic "This Unit's Settings" heading; "payments" renders under
// the dedicated "Payments" heading alongside the credential fields in
// UnitTextSettings below.
type UnitToggle struct {
	Key         string
	Label       string
	Description string
	Default     bool
	Section     string
}

// UnitToggles is every per-unit setting the "This Unit's Settings" and
// "Payments" sections of /admin/settings show, in display order.
var UnitToggles = []UnitToggle{
	{
		Key:         AdvancementEnabled,
		Label:       "Rank/badge advancement tracking",
		Description: "Shows /advancement and /admin/advancement for this unit. Turn off if you're tracking advancement elsewhere (e.g. Scoutbook) and don't need it duplicated here — the data isn't deleted, just hidden, so turning it back on picks up right where it left off.",
		Default:     true,
	},
	{
		Key:         TreasuryEnabled,
		Label:       "Treasury (fund accounting)",
		Description: "Shows /treasury and \"My Accounts\" for this unit, including the fundraiser storefront's admin pages and public order page/homepage button. Turn off if this unit doesn't handle its own finances through this site — no ledger data is deleted, every entry point (Treasurer and family self-service alike) is just closed until this is turned back on.",
		Default:     true,
	},
	{
		Key:         NewsletterEnabled,
		Label:       "Newsletters",
		Description: "Shows /admin/newsletters for this unit, including sending new ones. Turn off if this unit sends its newsletter some other way — already-sent newsletters and their history aren't deleted, just the admin UI for managing new ones is hidden.",
		Default:     true,
	},
	{
		Key:         ScoutAccountSelfService,
		Label:       "My Accounts (family access to Scout account balances)",
		Description: "Shows the \"My Accounts\" nav link and page, letting a family (or a Scout's own login) view their own Scout account balance and history. Turn off to keep account balances treasurer-only — the Treasury area is unchanged, this just shuts off the family-facing self-service view.",
		Default:     true,
	},
	{
		Key:   ProspectFormEnabled,
		Label: "\"Interested in joining\" form",
		Description: "Serves the public enquiry form at /join, where a prospective family leaves their details. " +
			"On by default. Turning it off closes the form to the public — anyone with the link gets \"not found\" — " +
			"but keeps every enquiry already received on the Prospects page, so nothing is lost by pausing it.",
		Default: true,
	},
	{
		Key:         PermissionSlipsEnabled,
		Label:       "Permission slips",
		Description: "On (default): families can view and sign permission slips for events from the calendar. Turn off for a unit that handles slips on paper — the link disappears from every event for everyone, leaders included, and the slip pages return \"not found\" even for an event already marked as needing one. Slips and signatures already recorded are kept, and come back if you turn this on again.",
		Default:     true,
	},
	{
		Key:         PermissionSlipEnforcement,
		Label:       "Only show permission slips on events that need one",
		Description: "Off (default): every event shows a \"Permission slip\" link, whether or not it needs one. On: only events a leader marks \"Requires a permission slip\" when creating them show that link — a weekly meeting won't display it at all. Leaders can always reach an event's permission slip page either way.",
		Default:     false,
	},
	{
		Key:         PaymentsStripeEnabled,
		Label:       "Accept payments via Stripe",
		Description: "Turns Stripe on for this unit once its keys are filled in below. Troop and Pack each connect their own Stripe account — this only affects this unit's own payments.",
		Default:     false,
		Section:     "payments",
	},
	{
		Key:         PaymentsPayPalEnabled,
		Label:       "Accept payments via PayPal",
		Description: "Turns PayPal on for this unit once its credentials are filled in below. Troop and Pack each connect their own PayPal account — this only affects this unit's own payments.",
		Default:     false,
		Section:     "payments",
	},
	{
		Key:         PayPalLiveMode,
		Label:       "PayPal live mode",
		Description: "Off (default) checks the sandbox credentials below — PayPal's test environment, no real money moves. Turn on only once you've entered real, live PayPal credentials and are ready to accept actual payments.",
		Default:     false,
		Section:     "payments",
	},
	{
		Key:         SocialFacebookEnabled,
		Label:       "Show Facebook link",
		Description: "Shows a Facebook icon/link in the site footer and on the homepage once a page URL is set below. Turn off to hide it without losing the URL you've entered.",
		Default:     true,
		Section:     "social",
	},
	{
		Key:         SocialInstagramEnabled,
		Label:       "Show Instagram link",
		Description: "Shows an Instagram icon/link in the site footer and on the homepage once a profile URL is set below. Turn off to hide it without losing the URL you've entered.",
		Default:     true,
		Section:     "social",
	},
	{
		Key:         SocialTikTokEnabled,
		Label:       "Show TikTok link",
		Description: "Shows a TikTok icon/link in the site footer and on the homepage once a profile URL is set below. Turn off to hide it without losing the URL you've entered.",
		Default:     true,
		Section:     "social",
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

	// value IS NOT NULL excludes a per-unit text-setting row (e.g. the
	// payment credential fields below) — see All's identical filter for
	// why this table can hold both kinds of row.
	rows, err := pool.Query(ctx, `SELECT key, value FROM unit_settings WHERE unit_id = $1 AND value IS NOT NULL`, unitID)
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

// --- Per-unit text settings (incl. payment credentials) -----------------
//
// A per-unit sibling to TextSettings above — for a setting that needs an
// actual value rather than on/off AND that Troop and Pack should answer
// independently, the same per-unit rationale as UnitToggle. So far this
// is exclusively the Stripe/PayPal credentials backing
// PaymentsStripeEnabled/PaymentsPayPalEnabled above.
//
// Some of these values (Secret: true) are real financial credentials —
// unlike the SMTP host/port/username above, "leave it in an environment
// variable instead" isn't practical here (Troop and Pack would each need
// their own separate container/environment just to have separate keys,
// defeating the point of one shared install), so this package does store
// them in the database — the same trust boundary internal/auth's TOTP
// secrets already sit inside. What IS different from every other setting
// in this package: a Secret value is never read back for display (see
// UnitTextSettingIsSet, not GetUnitText, for what the admin page shows),
// and a blank submission on the Payments form leaves a Secret field's
// stored value alone rather than clearing it (see SetUnitText) — so
// resubmitting the whole form to change one field never accidentally
// wipes another credential the admin just didn't feel like retyping.

// Known per-unit text setting keys — see migration 0023's value_text
// column.
const (
	StripePublishableKey       = "stripe_publishable_key"
	StripeSecretKey            = "stripe_secret_key"
	StripeWebhookSigningSecret = "stripe_webhook_signing_secret"

	PayPalClientID     = "paypal_client_id"
	PayPalClientSecret = "paypal_client_secret"

	SocialFacebookURL  = "social_facebook_url"
	SocialInstagramURL = "social_instagram_url"
	SocialTikTokURL    = "social_tiktok_url"

	// ProspectNotifyEmails is who gets told when someone fills in the
	// public "interested in joining" form — a list of addresses, one per
	// line or comma-separated. Blank means nobody is emailed; the enquiry
	// is still recorded and still shows on /admin/prospects, so it is
	// never lost, just not pushed to anyone.
	ProspectNotifyEmails = "prospect_notify_emails"

	WelcomeEmailSubject = "welcome_email_subject"
	WelcomeEmailBody    = "welcome_email_body"

	ExpenseApprovalThreshold = "expense_approval_threshold"
)

// DefaultExpenseApprovalThresholdCents is what an expense has to exceed
// before it needs a second person's authorization, when a unit hasn't set
// its own figure. $100 — high enough that routine spending (a campsite
// deposit, a few boxes of supplies) isn't slowed down, low enough that
// anything a committee would want to know about is covered.
const DefaultExpenseApprovalThresholdCents int64 = 10000

// ErrInvalidThreshold is returned by SetUnitText for an approval
// threshold that isn't a whole number of dollars. Its message is written
// to be shown straight to the admin who typed it.
var ErrInvalidThreshold = errors.New(
	"enter the approval threshold as a whole number of dollars, like 100 — no cents, no dollar sign")

// ExpenseApprovalThresholdCents reads a unit's threshold, falling back to
// DefaultExpenseApprovalThresholdCents when it's unset or unparseable.
// Stored as whole dollars (see ExpenseApprovalThreshold) rather than
// cents: nobody sets a $100.50 threshold, and keeping it to integers means
// no decimal parsing anywhere near a money value.
func ExpenseApprovalThresholdCents(ctx context.Context, pool *pgxpool.Pool, unitID string) (int64, error) {
	raw, err := GetUnitText(ctx, pool, unitID, ExpenseApprovalThreshold)
	if err != nil {
		return DefaultExpenseApprovalThresholdCents, err
	}
	dollars, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32)
	if err != nil || dollars < 0 {
		return DefaultExpenseApprovalThresholdCents, nil
	}
	return dollars * 100, nil
}

// UnitTextSetting describes one per-unit text setting for /admin/settings.
// Secret marks a real credential: the admin page never re-displays its
// actual value once saved, and leaving the field blank on submit leaves
// the stored value unchanged instead of clearing it (see SetUnitText).
// Section groups related settings under their own heading and their own
// save form, the same way UnitToggle.Section does — "payments" for the
// Stripe/PayPal credentials, "social" for the social-profile URLs, each
// saved independently so submitting one section's form can never blank
// out a field that belongs to the other.
type UnitTextSetting struct {
	Key         string
	Label       string
	Description string
	Placeholder string
	Secret      bool
	Multiline   bool // renders as a <textarea> instead of a single-line <input> — see admin-settings.html
	Section     string
}

// PaymentsIntegrationLive records whether this codebase actually has a
// checkout flow wired up. It does not: there is no Stripe or PayPal API
// call, and no webhook handler, anywhere in this repository — the
// credential fields below exist so a unit can get set up ahead of that
// work landing.
//
// Which makes a live secret key stored here pure downside: nothing reads
// it, and it sits in the database and in every backup taken from here on.
// While this is false, validateUnitTextValue refuses to store a live
// Stripe secret key and points the admin at a test key instead. Flip it
// to true in the same change that adds the checkout integration.
const PaymentsIntegrationLive = false

// ErrLiveKeyNotAccepted is returned by SetUnitText for a live payment
// credential entered while PaymentsIntegrationLive is false. Its message
// is written to be shown straight to the admin who typed it.
var ErrLiveKeyNotAccepted = errors.New(
	"online payments aren't switched on in this version of the site yet — nothing here can charge a card, " +
		"so a live secret key would just sit in the database and in every backup. Use your test key " +
		"(sk_test_...) for now; enter the live one when checkout is actually turned on.")

// validateUnitTextValue rejects values that shouldn't be stored yet. Kept
// in SetUnitText rather than in the handler so every path that writes a
// setting gets the same check.
// MaxEmailTemplateBytes bounds an admin-editable email template.
//
// The global POST limit is 500 MB, sized so a phone's camera roll can be
// uploaded in one go (see internal/csrf). That's the wrong bound for
// something that ends up as an email body, so this is a far tighter one —
// but deliberately roomy, because a template with images embedded as
// base64 data URIs is the normal reason a legitimate one gets big, and
// refusing that would be refusing the actual use case.
//
// Worth knowing when picking a template rather than a number: most mail
// clients cope with a large body, but Gmail clips a message past about
// 102 KB behind a "View entire message" link. A template comfortably
// under this ceiling can still be truncated in the reader's inbox — see
// the note on the newsletter form.
const MaxEmailTemplateBytes = 5 << 20 // 5 MB

// welcomePasswordPlaceholder matches the {{password}} placeholder with
// any whitespace inside the braces, mirroring what the sender accepts
// (see internal/web's canonicalizePlaceholders).
var welcomePasswordPlaceholder = regexp.MustCompile(`\{\{\s*password\s*\}\}`)

// ErrWelcomeEmailNeedsPassword is returned when a welcome-email body
// wouldn't actually tell anyone their password.
//
// A hard refusal rather than a warning, because the failure is silent and
// lands on a real family: the leader ticks "email login details", the
// email goes out looking perfectly fine, and the family has no password
// and no idea why they can't log in. The leader doesn't find out either —
// nothing errors. Cheaper to refuse the save, when the fix is one line.
var ErrWelcomeEmailNeedsPassword = errors.New(
	"this welcome email has no {{password}} placeholder, so it wouldn't tell anyone their password. " +
		"Add a line like:  <p>Temporary password: {{password}}</p>  " +
		"(if a template built elsewhere looks like it has one, check the braces haven't been split by " +
		"formatting or turned into &#123; — either breaks the match)")

// ErrTemplateTooLarge is returned for an email template past that bound.
var ErrTemplateTooLarge = errors.New(
	"that email template is over 5 MB — check you picked the right file, since anything that large is likely to be rejected by a mail server before it reaches anyone")

func validateUnitTextValue(key, trimmed string) error {
	if trimmed == "" {
		return nil
	}
	if key == WelcomeEmailBody {
		if len(trimmed) > MaxEmailTemplateBytes {
			return ErrTemplateTooLarge
		}
		if !welcomePasswordPlaceholder.MatchString(trimmed) {
			return ErrWelcomeEmailNeedsPassword
		}
	}
	if key == ExpenseApprovalThreshold {
		if dollars, err := strconv.ParseInt(trimmed, 10, 32); err != nil || dollars < 0 {
			return ErrInvalidThreshold
		}
		return nil
	}
	if PaymentsIntegrationLive {
		return nil
	}
	// Only the secret half matters. A publishable key (pk_live_) is meant
	// to be visible in a browser and is harmless at rest, so it's still
	// accepted — a unit can stage that side of the config now.
	if key == StripeSecretKey && strings.HasPrefix(trimmed, "sk_live_") {
		return ErrLiveKeyNotAccepted
	}
	return nil
}

// UnitTextSettings is every per-unit text/credential setting the
// "Payments" section of /admin/settings shows, in display order.
var UnitTextSettings = []UnitTextSetting{
	{
		Key:         StripePublishableKey,
		Label:       "Stripe publishable key",
		Description: "Starts with pk_test_ (sandbox) or pk_live_ — safe to expose in a browser, this is what a Stripe Checkout page needs client-side.",
		Placeholder: "pk_live_...",
		Section:     "payments",
	},
	{
		Key:         StripeSecretKey,
		Label:       "Stripe secret key",
		Description: "Starts with sk_test_ — grants real account access; never share it outside this form. Live keys (sk_live_) aren't accepted yet: no checkout is wired up in this version, so a live key would sit unused in the database and in every backup.",
		Placeholder: "sk_test_...",
		Secret:      true,
		Section:     "payments",
	},
	{
		Key:         StripeWebhookSigningSecret,
		Label:       "Stripe webhook signing secret",
		Description: "Starts with whsec_ — from the webhook endpoint's settings in the Stripe Dashboard. Nothing reads this yet; it's here so the configuration is ready when checkout is turned on.",
		Placeholder: "whsec_...",
		Secret:      true,
		Section:     "payments",
	},
	{
		Key:         PayPalClientID,
		Label:       "PayPal client ID",
		Description: "From your PayPal app's credentials — the sandbox or live one, matching the PayPal live mode toggle above.",
		Placeholder: "",
		Section:     "payments",
	},
	{
		Key:         PayPalClientSecret,
		Label:       "PayPal client secret",
		Description: "From the same PayPal app — grants real account access; never share it outside this form.",
		Placeholder: "",
		Secret:      true,
		Section:     "payments",
	},
	{
		Key:   ExpenseApprovalThreshold,
		Label: "Expense approval threshold (dollars)",
		Description: "An expense above this amount isn't recorded straight away — it waits for the " +
			"Cubmaster or Scoutmaster to authorize it, so the person spending the money isn't the only " +
			"person who signed off on it. Whole dollars, no cents. Leave blank for the default of $100. " +
			"Set it very high to effectively turn the requirement off.",
		Placeholder: "100",
		Section:     "treasury_controls",
	},
	{
		Key:         SocialFacebookURL,
		Label:       "Facebook page URL",
		Description: "Shows a Facebook icon/link in the footer and on the homepage, once the toggle above is on.",
		Placeholder: "https://facebook.com/yourtroop",
		Section:     "social",
	},
	{
		Key:         SocialInstagramURL,
		Label:       "Instagram profile URL",
		Description: "Shows an Instagram icon/link in the footer and on the homepage, once the toggle above is on.",
		Placeholder: "https://instagram.com/yourtroop",
		Section:     "social",
	},
	{
		Key:         SocialTikTokURL,
		Label:       "TikTok profile URL",
		Description: "Shows a TikTok icon/link in the footer and on the homepage, once the toggle above is on.",
		Placeholder: "https://tiktok.com/@yourtroop",
		Section:     "social",
	},
	{
		Key:   ProspectNotifyEmails,
		Label: "Who to email about a new enquiry",
		Description: "Email addresses to notify when someone fills in the \"interested in joining\" form — " +
			"one per line, or separated by commas. Usually the Cubmaster or Scoutmaster and whoever handles new " +
			"families. Leave blank and nobody is emailed; the enquiry still appears on the Prospects page.",
		Placeholder: "cubmaster@example.com\nmembership@example.com",
		Multiline:   true,
		Section:     "prospects",
	},
	{
		Key:         WelcomeEmailSubject,
		Label:       "Welcome email subject",
		Description: "Placeholders: {{name}}, {{email}}, {{password}}, {{login_url}}, {{unit_name}}. Left blank, a sensible default is used.",
		Placeholder: "Welcome to {{unit_name}}!",
		Section:     "welcome_email",
	},
	{
		Key:         WelcomeEmailBody,
		Label:       "Welcome email body",
		Description: "Same placeholders as the subject. HTML is allowed — write tags and they're sent as formatting; a template with no tags is sent as plain paragraphs exactly as before. Values substituted for the placeholders are always escaped, so a name or password containing < or & can't break the layout. Sent only when a leader checks \"Email login details\" while creating a family or individual login — never automatically. A family account's email always gets one extra, non-editable paragraph appended noting they can see every child linked to their account across both the Pack and Troop sites, since that's real behavior of this login, not just wording.",
		Placeholder: "",
		Multiline:   true,
		Section:     "welcome_email",
	},
}

func isKnownUnitText(key string) bool {
	for _, t := range UnitTextSettings {
		if t.Key == key {
			return true
		}
	}
	return false
}

func unitTextSettingIsSecret(key string) bool {
	for _, t := range UnitTextSettings {
		if t.Key == key {
			return t.Secret
		}
	}
	return false
}

// GetUnitText reads one per-unit text setting's actual stored value —
// meant for server-side use (e.g. a future checkout integration actually
// calling Stripe/PayPal), not for rendering back into an HTML form. See
// UnitTextSettingIsSet for what the admin page should show instead for a
// Secret setting.
func GetUnitText(ctx context.Context, pool *pgxpool.Pool, unitID, key string) (string, error) {
	var value *string
	err := pool.QueryRow(ctx, `SELECT value_text FROM unit_settings WHERE unit_id = $1 AND key = $2`, unitID, key).Scan(&value)
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

// AllUnitText returns every known UnitTextSetting's current stored value
// for a unit ("" for anything unset) — the raw values, including
// secrets. Callers building an admin page must not render a Secret one's
// value back into a form; see UnitTextSettingIsSet.
func AllUnitText(ctx context.Context, pool *pgxpool.Pool, unitID string) (map[string]string, error) {
	values := make(map[string]string, len(UnitTextSettings))
	for _, t := range UnitTextSettings {
		values[t.Key] = ""
	}

	rows, err := pool.Query(ctx, `SELECT key, value_text FROM unit_settings WHERE unit_id = $1 AND value_text IS NOT NULL`, unitID)
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

// UnitTextSettingIsSet reports whether a per-unit text setting has any
// stored value — what the admin page shows in place of a Secret
// setting's actual value ("already set" vs. "not set"), so a credential
// never round-trips back into rendered HTML once saved.
func UnitTextSettingIsSet(ctx context.Context, pool *pgxpool.Pool, unitID, key string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM unit_settings WHERE unit_id = $1 AND key = $2 AND value_text IS NOT NULL)
	`, unitID, key).Scan(&exists)
	return exists, err
}

// SetUnitText writes a per-unit text setting's value, audit-logged the
// same way SetText does — except a Secret setting's actual value is
// never written to the audit log (just whether it changed), and a blank
// submission for a Secret setting is a no-op (leaves whatever's already
// stored alone) rather than clearing it, so resubmitting the whole
// Payments form to change one field never silently wipes another
// credential that just happened to be left blank because the admin
// didn't want to retype it.
func SetUnitText(ctx context.Context, pool *pgxpool.Pool, unitID, key, value, actorID string) error {
	if !isKnownUnitText(key) {
		return ErrUnknownSetting
	}
	trimmed := strings.TrimSpace(value)
	secret := unitTextSettingIsSecret(key)

	if secret && trimmed == "" {
		return nil // leave the existing credential untouched
	}
	if err := validateUnitTextValue(key, trimmed); err != nil {
		return err
	}

	var existing *string
	err := pool.QueryRow(ctx, `SELECT value_text FROM unit_settings WHERE unit_id = $1 AND key = $2`, unitID, key).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	hadValue := existing != nil

	var textArg any
	if trimmed != "" {
		textArg = trimmed
	}

	var id string
	err = pool.QueryRow(ctx, `
		INSERT INTO unit_settings (unit_id, key, value_text, updated_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (unit_id, key) DO UPDATE SET value_text = EXCLUDED.value_text, updated_at = now(), updated_by = EXCLUDED.updated_by
		RETURNING id
	`, unitID, key, textArg, actorID).Scan(&id)
	if err != nil {
		return err
	}

	before := map[string]any{"key": key}
	after := map[string]any{"key": key}
	if secret {
		// Never write a credential's actual value to the audit log — just
		// enough to show whether something changed.
		before["value"] = redactedIfSet(hadValue)
		after["value"] = redactedIfSet(trimmed != "")
	} else {
		if hadValue {
			before["value"] = *existing
		}
		if trimmed != "" {
			after["value"] = trimmed
		}
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "unit_setting",
		EntityID:   id,
		ActorID:    &actorID,
		Action:     "update_text",
		Before:     before,
		After:      after,
	})
	return nil
}

// redactedIfSet renders a Secret setting's presence for the audit log
// without ever including its actual value.
func redactedIfSet(set bool) string {
	if set {
		return "(set)"
	}
	return "(not set)"
}
