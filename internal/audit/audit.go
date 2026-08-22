// Package audit is the system-wide audit log — every write of consequence
// (roster edits, calendar changes, approvals, content edits, and in Phase 2,
// ledger transactions) records here. It's deliberately generic and
// deliberately simple: one table, one Log function, no feature-specific
// logic. That's what lets Phase 2 log ledger_entry writes to the exact same
// place without touching this package. See
// scout-website-architecture-phase1.md Section 5.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Entry struct {
	EntityType        string
	EntityID          string
	ActorID           *string // nil for system-initiated actions (e.g. the Scoutbook CSV import)
	Action            string  // "create", "update", "delete", "approve", "reject", "submit_for_approval", ...
	Before            any     // marshaled to JSON; nil is fine (e.g. on create)
	After             any     // marshaled to JSON; nil is fine (e.g. on delete)
	ApprovalRequestID *string
}

// Log records an audit entry. Failures are logged but deliberately not
// returned as errors to the caller — an audit-log write failing should
// never be the reason a legitimate user action (like approving a calendar
// event) gets rolled back and reported as failed. In production this
// should also alert (e.g. via the process's structured logs feeding into
// whatever monitoring is set up), since a silently-failing audit log
// defeats the point of having one — flagged here rather than solved, since
// alerting infrastructure is an operational decision, not an architecture
// one.
func Log(ctx context.Context, pool *pgxpool.Pool, e Entry) {
	beforeJSON, err := marshalNullable(e.Before)
	if err != nil {
		log.Printf("audit: marshaling before_state for %s/%s: %v", e.EntityType, e.EntityID, err)
		return
	}
	afterJSON, err := marshalNullable(e.After)
	if err != nil {
		log.Printf("audit: marshaling after_state for %s/%s: %v", e.EntityType, e.EntityID, err)
		return
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO audit_log (entity_type, entity_id, actor_id, action, before_state, after_state, approval_request_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, e.EntityType, e.EntityID, e.ActorID, e.Action, beforeJSON, afterJSON, e.ApprovalRequestID)
	if err != nil {
		log.Printf("audit: failed to write entry for %s/%s action=%s: %v", e.EntityType, e.EntityID, e.Action, err)
	}
}

func marshalNullable(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

// LogEntry is what the admin-facing audit view (and CSV export) reads
// back.
type LogEntry struct {
	ID         string
	EntityType string
	EntityID   string
	ActorID    string // "" for system-initiated actions — see ActorName
	ActorName  string // resolved from members, "system" if ActorID was nil
	Action     string
	OccurredAt string // pre-formatted for display; see internal/web
}

// entityScopeSQL is "which audit_log.entity_id values belong to this
// unit" — the answer to "does this unit's activity log cover this
// entity" for every entity_type audit.Log is called with anywhere in the
// codebase (grep for `EntityType: "` if this ever needs updating).
// Shared by ForUnitFiltered, ActorsForUnit, and EntityTypesForUnit so
// there's exactly one place that answers this question — three separate
// copies of this UNION would inevitably drift out of sync with each
// other (and already had: family/member/role_assignment/sub_group were
// being logged by internal/roster since Phase 1 but were never included
// here, so roster/role changes silently never appeared in the activity
// log despite SECURITY_AUDIT.md's assumption that they did — fixed by
// adding them below).
//
// Most entity types have a direct unit_id column and are scoped with a
// plain `WHERE unit_id = $1`. Four don't, each for a different reason:
//   - system_settings has no unit_id at all — it's genuinely global, so a
//     site-wide setting change is relevant to every unit's log, included
//     unconditionally rather than filtered.
//   - members and families have no unit_id column — a member's
//     unit is established by holding a role there (role_assignments),
//     and a family can span both units by design (see
//     scout-website-requirements.md Section 2's single-sign-on account
//     model) — scoped the same way units.RolesForFamilyInUnit and
//     roster.Scope already answer "is this member/family part of this
//     unit."
//   - permission_slip_signatures has no unit_id either — a signature's
//     unit comes from the slip it belongs to.
//
// KEEPING THIS IN SYNC IS LOAD-BEARING, and has silently drifted twice:
// once for family/member/role_assignment/sub_group (see the note above),
// and again for the eight types added in the block below — advancement
// records, custom roles, newsletters, permission slips and their
// signatures, saved treasury reports, per-unit settings, and leader
// profiles were all being written to audit_log but were unreachable from
// every read path, so 48 real rows in the development database had never
// been displayable. Adding an audit.Log call with a new EntityType is
// therefore only half the change: the entity's table must be added here
// too, or the entry is written and then silently never shown. The
// audit_entity_scope_test.go test in this package enforces that by
// failing whenever an EntityType is logged somewhere in the codebase
// without a matching table here.
const entityScopeSQL = `
	SELECT id FROM events WHERE unit_id = $1
	UNION
	SELECT id FROM content_pages WHERE unit_id = $1
	UNION
	SELECT id FROM ledger_transactions WHERE unit_id = $1
	UNION
	SELECT id FROM ledger_accounts WHERE unit_id = $1
	UNION
	SELECT id FROM fundraisers WHERE unit_id = $1
	UNION
	SELECT id FROM fundraiser_allocations WHERE fundraiser_id IN (SELECT id FROM fundraisers WHERE unit_id = $1)
	UNION
	SELECT id FROM system_settings
	UNION
	SELECT id FROM sub_groups WHERE unit_id = $1
	UNION
	SELECT id FROM resources WHERE unit_id = $1
	UNION
	SELECT id FROM role_assignments WHERE unit_id = $1
	UNION
	SELECT member_id FROM role_assignments WHERE unit_id = $1
	UNION
	SELECT members.family_id FROM role_assignments JOIN members ON members.id = role_assignments.member_id WHERE role_assignments.unit_id = $1
	UNION
	SELECT id FROM advancement_records WHERE unit_id = $1
	UNION
	SELECT id FROM custom_roles WHERE unit_id = $1
	UNION
	SELECT id FROM newsletters WHERE unit_id = $1
	UNION
	SELECT id FROM permission_slips WHERE unit_id = $1
	UNION
	SELECT id FROM permission_slip_signatures WHERE permission_slip_id IN (SELECT id FROM permission_slips WHERE unit_id = $1)
	UNION
	SELECT id FROM saved_treasury_reports WHERE unit_id = $1
	UNION
	SELECT id FROM unit_settings WHERE unit_id = $1
	UNION
	SELECT id FROM leaders WHERE unit_id = $1
`

// SystemActor is the Filter.ActorID sentinel for "system-initiated
// entries only" (audit_log.actor_id IS NULL) — distinct from "" (any
// actor, no restriction).
const SystemActor = "system"

// Filter narrows ForUnitFiltered's result set. UnitID is the only
// required field — every other field's zero value means "no restriction
// on this dimension."
type Filter struct {
	UnitID     string
	From       *time.Time // inclusive lower bound on occurred_at
	To         *time.Time // inclusive upper bound on occurred_at
	EntityType string     // "" = any entity type ("function", in the activity log UI)
	ActorID    string     // "" = any actor; SystemActor = system-initiated entries only ("person", in the activity log UI)
	Limit      int        // 0 = unbounded — only intended for CSV export (see internal/web's AuditExport), which applies its own hard cap
}

// ForUnitFiltered returns audit entries touching a unit's entities,
// newest first, narrowed by an optional date range / entity type / actor
// (see Filter — every field but UnitID is optional, so
// `Filter{UnitID: unitID, Limit: 100}` reproduces the old unfiltered
// "recent activity" behavior). Used by both the on-screen filtered
// activity log and the CSV export (internal/web's AuditView /
// AuditExport) so the two can never drift out of sync with each other —
// whatever you're looking at on screen is exactly what you get in the
// export.
func ForUnitFiltered(ctx context.Context, pool *pgxpool.Pool, f Filter) ([]LogEntry, error) {
	args := []any{f.UnitID}
	var conditions []string

	if f.From != nil {
		args = append(args, *f.From)
		conditions = append(conditions, fmt.Sprintf("audit_log.occurred_at >= $%d", len(args)))
	}
	if f.To != nil {
		args = append(args, *f.To)
		conditions = append(conditions, fmt.Sprintf("audit_log.occurred_at <= $%d", len(args)))
	}
	if f.EntityType != "" {
		args = append(args, f.EntityType)
		conditions = append(conditions, fmt.Sprintf("audit_log.entity_type = $%d", len(args)))
	}
	switch f.ActorID {
	case "":
		// no restriction
	case SystemActor:
		conditions = append(conditions, "audit_log.actor_id IS NULL")
	default:
		args = append(args, f.ActorID)
		conditions = append(conditions, fmt.Sprintf("audit_log.actor_id = $%d", len(args)))
	}

	extraWhere := ""
	for _, c := range conditions {
		extraWhere += " AND " + c
	}

	limitClause := ""
	if f.Limit > 0 {
		args = append(args, f.Limit)
		limitClause = fmt.Sprintf(" LIMIT $%d", len(args))
	}

	rows, err := pool.Query(ctx, `
		SELECT
			audit_log.id, audit_log.entity_type, audit_log.entity_id,
			COALESCE(audit_log.actor_id::text, ''),
			COALESCE(members.first_name || ' ' || members.last_name, 'system'),
			audit_log.action,
			to_char(audit_log.occurred_at, 'YYYY-MM-DD HH24:MI')
		FROM audit_log
		LEFT JOIN members ON members.id = audit_log.actor_id
		WHERE audit_log.entity_id IN (`+entityScopeSQL+`)`+extraWhere+`
		ORDER BY audit_log.occurred_at DESC`+limitClause,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []LogEntry
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.ID, &e.EntityType, &e.EntityID, &e.ActorID, &e.ActorName, &e.Action, &e.OccurredAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ActorOption is one entry in the activity log's "person" filter — a real
// member who has at least one audit_log entry visible in this unit's
// log, deduped, alphabetical by name. The synthetic "system" option
// (SystemActor) is added by the caller (internal/web), not here, since
// it isn't a members row.
type ActorOption struct {
	ID   string
	Name string
}

// ActorsForUnit lists everyone who could plausibly be selected in the
// "person" filter — i.e. who actually has at least one entry in this
// unit's activity log, rather than every member/family in the system
// (most of whom never will).
func ActorsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]ActorOption, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT members.id, members.first_name || ' ' || members.last_name
		FROM audit_log
		JOIN members ON members.id = audit_log.actor_id
		WHERE audit_log.entity_id IN (`+entityScopeSQL+`)
		ORDER BY 2
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var actors []ActorOption
	for rows.Next() {
		var a ActorOption
		if err := rows.Scan(&a.ID, &a.Name); err != nil {
			return nil, err
		}
		actors = append(actors, a)
	}
	return actors, rows.Err()
}

// EntityTypesForUnit lists the distinct entity_type values actually
// present in this unit's activity log — what the "function" filter
// dropdown offers, rather than a hardcoded list that could drift from
// what audit.Log is actually called with.
func EntityTypesForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT audit_log.entity_type
		FROM audit_log
		WHERE audit_log.entity_id IN (`+entityScopeSQL+`)
		ORDER BY 1
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var types []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		types = append(types, t)
	}
	return types, rows.Err()
}
