package web

// This file is rank/badge advancement tracking — scout-website-requirements.md's
// Phase 3 "advancement tracking" candidate (see README.md's "Not in
// Phase 1" section and 0001_init.sql's advancement_records table
// comment). Two ways records get in: a leader recording one earned
// rank/badge directly (source = "manual"), or pasting many rows at once
// in the same header-driven paste-box shape the Scoutbook roster import
// (roster_import.go) and fundraiser bulk import (treasury.go) already
// established — see internal/advancement's package comment for why both
// exist rather than only the Scoutbook-shaped bulk path the schema
// comment originally anticipated.
//
// GET /advancement is members-only (any logged-in family), read-only,
// and shows every youth in the unit's history — advancement isn't
// financial or PII-sensitive the way ledger/roster data is, and a Scout
// showing off an Eagle rank to the rest of the troop is normal. Adding,
// editing, and bulk-importing records is leader-only
// (units.CanEditUnitContent) under /admin/advancement.

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/47-yonkers/scout-site/internal/advancement"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/roster"
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

// requireAdvancementEnabled reports whether advancement tracking is on
// for unitID (see settings.AdvancementEnabled), writing a clear 403 if
// not — hiding the nav link (see base.html) isn't itself a security
// boundary, so every advancement route checks this directly too, the same
// way a disabled feature should behave for anyone who still has the URL
// bookmarked or types it directly.
func (h *Handlers) requireAdvancementEnabled(w http.ResponseWriter, r *http.Request, unitID string) bool {
	enabled, err := settings.GetForUnit(r.Context(), h.Pool, unitID, settings.AdvancementEnabled)
	if err != nil {
		log.Printf("web: checking advancement-enabled setting: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if !enabled {
		http.Error(w, "advancement tracking is turned off for this unit — an admin can re-enable it from /admin/settings", http.StatusForbidden)
		return false
	}
	return true
}

// advancementImportRow is one line of a pasted bulk-advancement import
// and its outcome — the advancement-specific sibling of roster_import.go's
// importOutcome and treasury.go's bulkImportRow; each bulk import has its
// own shape of "what happened," so these aren't shared types.
type advancementImportRow struct {
	Line    int
	Input   string
	Applied bool
	Detail  string // what happened, on success
	Reason  string // why it was skipped, on failure
}

type advancementImportResult struct {
	Rows      []advancementImportRow
	Succeeded int
	Skipped   int
}

// advancementView decorates a Record with its Scout's name and a
// display-ready date, for both the members-only list and the leader
// management view.
type advancementView struct {
	advancement.Record
	MemberName string
	EarnedOn   string // "" if EarnedDate is nil
}

func decorateAdvancement(records []advancement.Record, names map[string]string) []advancementView {
	views := make([]advancementView, 0, len(records))
	for _, r := range records {
		v := advancementView{Record: r, MemberName: names[r.MemberID]}
		if r.EarnedDate != nil {
			v.EarnedOn = r.EarnedDate.Format("Jan 2, 2006")
		}
		views = append(views, v)
	}
	return views
}

func (h *Handlers) Advancement(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	if _, _, ok := h.requireUnitMember(w, r, "/advancement"); !ok {
		return
	}
	if !h.requireAdvancementEnabled(w, r, unit.ID) {
		return
	}

	records, err := advancement.ListForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing advancement records: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	names, err := h.memberNames(r.Context(), memberIDsOf(records))
	if err != nil {
		log.Printf("web: loading member names for advancement: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := struct {
		baseData
		Records []advancementView
	}{baseData: h.base(r, "Advancement"), Records: decorateAdvancement(records, names)}
	h.render(w, h.advancement, data)
}

func memberIDsOf(records []advancement.Record) []string {
	ids := make([]string, 0, len(records))
	seen := map[string]bool{}
	for _, r := range records {
		if !seen[r.MemberID] {
			ids = append(ids, r.MemberID)
			seen[r.MemberID] = true
		}
	}
	return ids
}

func (h *Handlers) AdminAdvancementList(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireContentEditor(w, r, "/admin/advancement")
	if !ok {
		return
	}
	if !h.requireAdvancementEnabled(w, r, unit.ID) {
		return
	}
	h.renderAdvancementAdmin(w, r, unit.ID, "", nil)
}

// renderAdvancementAdmin loads the leader management view — every
// record plus the unit's youth roster for the "add one" form's Scout
// picker — optionally with a bulk-import result summary attached (see
// AdminAdvancementBulkImport), same shared-render shape as
// renderFundraiser in treasury.go.
func (h *Handlers) renderAdvancementAdmin(w http.ResponseWriter, r *http.Request, unitID, flash string, bulkResult *advancementImportResult) {
	records, err := advancement.ListForUnit(r.Context(), h.Pool, unitID)
	if err != nil {
		log.Printf("web: listing advancement records: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	names, err := h.memberNames(r.Context(), memberIDsOf(records))
	if err != nil {
		log.Printf("web: loading member names for advancement: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rosterEntries, err := family.RosterForUnit(r.Context(), h.Pool, unitID)
	if err != nil {
		log.Printf("web: loading roster for advancement admin: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var scouts []family.RosterEntry
	for _, m := range rosterEntries {
		if m.MemberType == "youth" {
			scouts = append(scouts, m)
		}
	}

	data := struct {
		baseData
		Records    []advancementView
		Scouts     []family.RosterEntry
		BulkResult *advancementImportResult
	}{
		baseData:   h.base(r, "Manage Advancement"),
		Records:    decorateAdvancement(records, names),
		Scouts:     scouts,
		BulkResult: bulkResult,
	}
	data.Flash = flash
	h.render(w, h.advancementAdmin, data)
}

// parseEarnedDate parses an optional "YYYY-MM-DD" date field — "" is
// valid (a record with no known date, e.g. an old badge nobody wrote the
// date down for).
func parseEarnedDate(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, fmt.Errorf("%q isn't a valid date (expected YYYY-MM-DD)", raw)
	}
	return &t, nil
}

func (h *Handlers) AdminAdvancementCreate(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/advancement")
	if !ok {
		return
	}
	if !h.requireAdvancementEnabled(w, r, unit.ID) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	memberID := r.PostFormValue("member_id")
	recordType := r.PostFormValue("record_type")
	name := strings.TrimSpace(r.PostFormValue("name"))
	if memberID == "" || name == "" {
		http.Error(w, "choose a Scout and enter a name", http.StatusBadRequest)
		return
	}
	earnedDate, err := parseEarnedDate(r.PostFormValue("earned_date"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The target member must actually belong to this unit's roster —
	// otherwise a leader could (accidentally, via a stale form, or on
	// purpose) record advancement against a member of a completely
	// unrelated family/unit by guessing an ID.
	if m, found, err := family.GetMember(r.Context(), h.Pool, memberID); err != nil || !found || m.MemberType != "youth" {
		http.Error(w, "choose a valid Scout", http.StatusBadRequest)
		return
	}
	roles, err := roster.RolesForMemberInUnit(r.Context(), h.Pool, memberID, unit.ID)
	if err != nil || len(roles) == 0 {
		http.Error(w, "that Scout isn't on this unit's roster", http.StatusBadRequest)
		return
	}

	if _, err := advancement.CreateRecord(r.Context(), h.Pool, memberID, unit.ID, recordType, name, earnedDate, "manual", actor.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/admin/advancement", http.StatusSeeOther)
}

func (h *Handlers) AdminAdvancementDelete(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/advancement")
	if !ok {
		return
	}
	if !h.requireAdvancementEnabled(w, r, unit.ID) {
		return
	}
	id := r.PathValue("id")
	if err := advancement.DeleteRecord(r.Context(), h.Pool, id, unit.ID, actor.ID); err != nil {
		if err == advancement.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		log.Printf("web: deleting advancement record: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/advancement", http.StatusSeeOther)
}

// AdminAdvancementBulkImport parses pasted rows (First Name, Last Name,
// Type, Name, Earned Date) and applies each — same per-row
// skip-and-report shape as the fundraiser and Scoutbook roster bulk
// imports: a row that doesn't match exactly one Scout, or has an invalid
// type/date, is skipped and reported rather than blocking the rows that
// are fine.
func (h *Handlers) AdminAdvancementBulkImport(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/advancement")
	if !ok {
		return
	}
	if !h.requireAdvancementEnabled(w, r, unit.ID) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	rosterEntries, err := family.RosterForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading roster for advancement bulk import: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	byName := map[string][]family.RosterEntry{}
	for _, m := range rosterEntries {
		if m.MemberType != "youth" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(m.FirstName) + " " + strings.TrimSpace(m.LastName))
		byName[key] = append(byName[key], m)
	}

	result := &advancementImportResult{}
	sawFirstLine := false
	lineNum := 0
	for _, rawLine := range strings.Split(r.FormValue("bulk_rows"), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" {
			continue
		}
		fields := splitBulkRow(line)

		if !sawFirstLine {
			sawFirstLine = true
			// A header row's 3rd column ("type") won't be "rank"/"badge"
			// — use that to detect and silently skip it, same heuristic
			// the fundraiser bulk import uses on its amount column.
			var typeField string
			if len(fields) > 2 {
				typeField = strings.ToLower(strings.TrimSpace(fields[2]))
			}
			if typeField != "rank" && typeField != "badge" {
				continue
			}
		}
		lineNum++

		var firstName, lastName, recordType, name, earnedRaw string
		if len(fields) > 0 {
			firstName = strings.TrimSpace(fields[0])
		}
		if len(fields) > 1 {
			lastName = strings.TrimSpace(fields[1])
		}
		if len(fields) > 2 {
			recordType = strings.ToLower(strings.TrimSpace(fields[2]))
		}
		if len(fields) > 3 {
			name = strings.TrimSpace(fields[3])
		}
		if len(fields) > 4 {
			earnedRaw = strings.TrimSpace(fields[4])
		}

		skip := func(reason string) {
			result.Rows = append(result.Rows, advancementImportRow{Line: lineNum, Input: line, Reason: reason})
			result.Skipped++
		}

		if firstName == "" || lastName == "" || name == "" {
			skip("first name, last name, and name are required")
			continue
		}
		if recordType != "rank" && recordType != "badge" {
			skip(`type must be "rank" or "badge"`)
			continue
		}
		earnedDate, err := parseEarnedDate(earnedRaw)
		if err != nil {
			skip(err.Error())
			continue
		}

		matches := byName[strings.ToLower(firstName+" "+lastName)]
		if len(matches) == 0 {
			skip(fmt.Sprintf("no Scout on the roster named %q %q", firstName, lastName))
			continue
		}
		if len(matches) > 1 {
			skip(fmt.Sprintf("%d Scouts on the roster are named %q %q — enter this one manually above", len(matches), firstName, lastName))
			continue
		}

		rec, err := advancement.CreateRecord(r.Context(), h.Pool, matches[0].ID, unit.ID, recordType, name, earnedDate, "scoutbook_csv", actor.ID)
		if err != nil {
			skip(err.Error())
			continue
		}
		detail := fmt.Sprintf("recorded %s %s for %s %s", recordType, name, firstName, lastName)
		if rec.EarnedDate != nil {
			detail += " (earned " + rec.EarnedDate.Format("Jan 2, 2006") + ")"
		}
		result.Rows = append(result.Rows, advancementImportRow{Line: lineNum, Input: line, Applied: true, Detail: detail})
		result.Succeeded++
	}

	h.renderAdvancementAdmin(w, r, unit.ID, "", result)
}
