package web

// This file is the Activity Log (/audit) and its CSV export
// (/audit/export.csv). Both share exactly the same filtering — date
// range, "function" (entity type), and "person" (actor) — via
// audit.Filter / audit.ForUnitFiltered (see internal/audit/audit.go), so
// a leader can narrow the on-screen table and then export precisely what
// they're looking at, not some separately-filtered dataset. The filter
// controls submit as a plain GET (see audit.html) so the resulting URL is
// bookmarkable/shareable and the "Export CSV" link can just carry the
// same query string over to /audit/export.csv.

import (
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/47-yonkers/scout-site/internal/audit"
	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/units"
)

// maxAuditExportRows caps a CSV export so an unbounded date range (or no
// filter at all, on a long-running install) can't turn one request into
// an unbounded query and response. 10,000 rows is generous for what this
// is for — a council audit or a committee reviewing a specific period,
// not a full data dump — revisit if a real unit's activity genuinely
// outgrows it. The on-screen view uses a separate, smaller cap
// (auditViewRows) since nobody scrolls through 10,000 table rows.
const (
	maxAuditExportRows = 10000
	auditViewRows      = 200
)

// entityTypeLabels maps the raw entity_type values audit.Log is called
// with across the codebase (grep for `EntityType: "` if this ever needs
// updating) to a leader-friendly label for the activity log table and
// the "function" filter dropdown. Falls back to the raw value for
// anything unlisted, so a future entity_type introduced without updating
// this map still shows up here — just less prettily — instead of
// silently vanishing.
var entityTypeLabels = map[string]string{
	"content_page":          "Content (homepage, news, galleries)",
	"event":                 "Calendar events",
	"ledger_transaction":    "Ledger transactions",
	"ledger_account":        "Ledger accounts",
	"fundraiser":            "Fundraisers",
	"fundraiser_allocation": "Fundraiser allocations",
	"system_setting":        "Site settings",
	"family":                "Family logins",
	"member":                "Roster members",
	"role_assignment":       "Roles",
	"sub_group":             "Dens/patrols",
}

func entityTypeLabel(entityType string) string {
	if label, ok := entityTypeLabels[entityType]; ok {
		return label
	}
	return entityType
}

// auditRow adds the friendly label to audit.LogEntry for the table.
type auditRow struct {
	audit.LogEntry
	EntityLabel string
}

// entityTypeOption is one entry in the "function" filter dropdown.
type entityTypeOption struct {
	Value string
	Label string
}

// requireAuditViewer resolves the current unit/login and checks
// permission to view the activity log — leaders (CanEditUnitContent) or
// the Treasurer (CanManageLedger, since Phase 2's ledger writes to this
// same log and a Treasurer needs to see those entries even without
// general content-edit rights). Shared by AuditView and AuditExport so
// the export can't be reached with looser permissions than the view
// itself. Per SECURITY_AUDIT.md, this used to be gated on "logged in"
// only — the activity log surfaces things like who reset whose password
// and every role change, which isn't something every logged-in Scout/
// Parent account needs visibility into.
func (h *Handlers) requireAuditViewer(w http.ResponseWriter, r *http.Request) (unit units.Unit, ok bool) {
	unit, _ = units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return units.Unit{}, false
	}
	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || (!units.CanEditUnitContent(caps) && !units.CanManageLedger(caps)) {
		http.Error(w, "you don't have permission to view the activity log", http.StatusForbidden)
		return units.Unit{}, false
	}
	return unit, true
}

// parseAuditFilter reads from/to/entity_type/actor_id query params into
// an audit.Filter. from/to are YYYY-MM-DD (an HTML <input type="date">'s
// native format); a missing or unparseable value just leaves that bound
// unset rather than erroring — a leader mistyping or clearing part of the
// URL should see an unfiltered log, not a broken page.
func parseAuditFilter(r *http.Request, unitID string) audit.Filter {
	q := r.URL.Query()
	f := audit.Filter{UnitID: unitID, EntityType: q.Get("entity_type"), ActorID: q.Get("actor_id")}
	if from, err := time.ParseInLocation("2006-01-02", q.Get("from"), time.Local); err == nil {
		f.From = &from
	}
	if to, err := time.ParseInLocation("2006-01-02", q.Get("to"), time.Local); err == nil {
		// Inclusive whole-day upper bound — picking a "to" date means
		// "through the end of that day," not midnight at its start.
		endOfDay := to.Add(24*time.Hour - time.Second)
		f.To = &endOfDay
	}
	return f
}

func (h *Handlers) AuditView(w http.ResponseWriter, r *http.Request) {
	unit, ok := h.requireAuditViewer(w, r)
	if !ok {
		return
	}

	filter := parseAuditFilter(r, unit.ID)
	filter.Limit = auditViewRows
	entries, err := audit.ForUnitFiltered(r.Context(), h.Pool, filter)
	if err != nil {
		log.Printf("web: loading audit log: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rows := make([]auditRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, auditRow{LogEntry: e, EntityLabel: entityTypeLabel(e.EntityType)})
	}

	actors, err := audit.ActorsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading activity log actors for filter: %v", err)
	}
	rawTypes, err := audit.EntityTypesForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading activity log entity types for filter: %v", err)
	}
	entityTypes := make([]entityTypeOption, 0, len(rawTypes))
	for _, t := range rawTypes {
		entityTypes = append(entityTypes, entityTypeOption{Value: t, Label: entityTypeLabel(t)})
	}

	data := struct {
		baseData
		Entries           []auditRow
		Actors            []audit.ActorOption
		EntityTypes       []entityTypeOption
		From, To          string
		EntityType        string
		ActorID           string
		ExportQueryString string // carries the current filter over to the "Export CSV" link
	}{
		baseData:          h.base(r, "Activity Log"),
		Entries:           rows,
		Actors:            actors,
		EntityTypes:       entityTypes,
		From:              r.URL.Query().Get("from"),
		To:                r.URL.Query().Get("to"),
		EntityType:        filter.EntityType,
		ActorID:           filter.ActorID,
		ExportQueryString: r.URL.RawQuery,
	}
	h.render(w, h.audit, data)
}

// AuditExport streams the same filtered result set AuditView shows as a
// CSV download — see maxAuditExportRows for the row cap.
func (h *Handlers) AuditExport(w http.ResponseWriter, r *http.Request) {
	unit, ok := h.requireAuditViewer(w, r)
	if !ok {
		return
	}

	filter := parseAuditFilter(r, unit.ID)
	filter.Limit = maxAuditExportRows
	entries, err := audit.ForUnitFiltered(r.Context(), h.Pool, filter)
	if err != nil {
		log.Printf("web: exporting audit log: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	filename := fmt.Sprintf("activity-log-%s-%s.csv", unit.Slug, time.Now().In(time.Local).Format("2006-01-02"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"When", "Who", "Action", "Function", "Entity ID"})
	for _, e := range entries {
		_ = cw.Write([]string{e.OccurredAt, e.ActorName, e.Action, entityTypeLabel(e.EntityType), e.EntityID})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		// Headers (and possibly some rows) are already on the wire at this
		// point — nothing left to do but log it, same as any streaming
		// response that fails partway through.
		log.Printf("web: writing audit export CSV: %v", err)
	}
}
