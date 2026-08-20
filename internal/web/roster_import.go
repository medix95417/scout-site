package web

// This file is the Scoutbook CSV import — scout-website-requirements.md's
// Phase 3 "Scoutbook CSV import job" candidate (see README.md's "Not in
// Phase 1" section). Real Scoutbook member-roster exports vary by export
// type and council, so rather than parsing one exact byte format, this
// accepts a header-driven paste of rows (see splitBulkRow in treasury.go
// for the same tab/comma paste-box approach the fundraiser bulk import
// already established, and its comment for why a paste box rather than a
// real file upload) with a handful of recognized column names — a leader
// exports from Scoutbook, opens it in a spreadsheet, keeps/renames the
// columns this expects, and pastes the result in.
//
// Every row goes through the exact same requireRosterEditor scope and
// roster.IsAllowedRole checks the manual "Add a New Family"/"Add a Member"
// forms already enforce — bulk doesn't mean less-checked.

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/roster"
)

// importRow is one parsed, not-yet-applied CSV row.
type importRow struct {
	Line       int
	FirstName  string
	LastName   string
	MemberType string // "adult" | "youth"
	Email      string // "" for youth rows
	Role       string
	FamilyName string
	SubGroup   string // raw, as given — resolved against the unit's actual sub-groups at apply time
}

// importOutcome is one row's result after being applied — success or a
// skip reason — for the results list, same shape as the fundraiser bulk
// import's per-row reporting (see bulkImportRow in treasury.go).
type importOutcome struct {
	Line     int
	Input    string
	Applied  bool
	Detail   string // what happened, on success
	Reason   string // why it was skipped, on failure
	Email    string // populated only when a brand-new family login was created
	TempPass string // populated only when a brand-new family login was created — shown once
}

// normalizeHeader lowercases and strips spaces/underscores/hyphens so
// "First Name", "first_name", and "firstname" all resolve the same way —
// exports and the spreadsheets people reshape them in are inconsistent
// about this.
func normalizeHeader(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	return strings.NewReplacer(" ", "", "_", "", "-", "").Replace(h)
}

// normalizeMemberType maps common Scoutbook/roster export values to this
// app's "adult"/"youth" — returns "" for anything unrecognized.
func normalizeMemberType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "adult", "parent", "leader":
		return "adult"
	case "youth", "scout", "child":
		return "youth"
	default:
		return ""
	}
}

// headerAliases maps each logical field to the header names recognized
// for it (already run through normalizeHeader).
var headerAliases = map[string][]string{
	"firstname":  {"firstname"},
	"lastname":   {"lastname"},
	"membertype": {"membertype", "type"},
	"email":      {"email"},
	"role":       {"role", "position"},
	"familyname": {"familyname", "family", "household"},
	"subgroup":   {"subgroup", "den", "patrol"},
}

// parseImportRows parses a pasted header+data block into rows, or returns
// an error describing which required column is missing. Unlike the
// fundraiser bulk import, a header row is required here (not
// auto-detected) — there are too many optional/reorderable columns for
// position-based parsing to be reliable.
func parseImportRows(raw string) ([]importRow, error) {
	var lines []string
	for _, l := range strings.Split(raw, "\n") {
		l = strings.TrimSpace(strings.TrimSuffix(l, "\r"))
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("paste a header row and at least one data row")
	}

	headerFields := splitBulkRow(lines[0])
	colIndex := map[string]int{}
	for field, aliases := range headerAliases {
		for i, h := range headerFields {
			nh := normalizeHeader(h)
			for _, alias := range aliases {
				if nh == alias {
					colIndex[field] = i
				}
			}
		}
	}
	for _, required := range []string{"firstname", "lastname", "membertype", "role"} {
		if _, ok := colIndex[required]; !ok {
			return nil, fmt.Errorf("missing required column %q (recognized headers: %v)", required, headerAliases[required])
		}
	}

	get := func(fields []string, key string) string {
		i, ok := colIndex[key]
		if !ok || i >= len(fields) {
			return ""
		}
		return strings.TrimSpace(fields[i])
	}

	var rows []importRow
	for lineNum, line := range lines[1:] {
		fields := splitBulkRow(line)
		rows = append(rows, importRow{
			Line:       lineNum + 2, // +1 for the header row, +1 for 1-indexing
			FirstName:  get(fields, "firstname"),
			LastName:   get(fields, "lastname"),
			MemberType: normalizeMemberType(get(fields, "membertype")),
			Email:      get(fields, "email"),
			Role:       strings.ToLower(get(fields, "role")),
			FamilyName: get(fields, "familyname"),
			SubGroup:   get(fields, "subgroup"),
		})
	}
	return rows, nil
}

// rosterImportFormData is shared by AdminRosterImportForm (plain GET) and
// AdminRosterImportApply's parse-error path — html/template errors out on
// a field reference that doesn't exist on the data struct at all (unlike
// a merely empty one), so both call sites need the same shape.
type rosterImportFormData struct {
	baseData
	ParseError string
}

func (h *Handlers) AdminRosterImportForm(w http.ResponseWriter, r *http.Request) {
	_, _, _, ok := h.requireRosterEditor(w, r, "/admin/roster/import")
	if !ok {
		return
	}
	h.render(w, h.rosterImport, rosterImportFormData{baseData: h.base(r, "Import Roster")})
}

// AdminRosterImportApply parses and applies a pasted roster CSV in one
// pass. Rows sharing the same (case-insensitive) family name are grouped
// into one family: the first row in a group with an email that doesn't
// already match an existing login creates a brand-new family+login (see
// roster.CreateFamilyWithMember); every other row in the group — and any
// row whose email DOES match an existing login — just adds a member to
// whichever family that resolved to. A family group with no resolvable
// email anywhere in it (no existing match, no adult row with an email)
// can't be anchored to a family at all and every row in it is skipped
// with a reason, since this app's login model has no member-level
// identity to place an unanchored youth row against.
func (h *Handlers) AdminRosterImportApply(w http.ResponseWriter, r *http.Request) {
	unit, actor, scope, ok := h.requireRosterEditor(w, r, "/admin/roster/import")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	rows, err := parseImportRows(r.FormValue("csv_rows"))
	if err != nil {
		h.render(w, h.rosterImport, rosterImportFormData{baseData: h.base(r, "Import Roster"), ParseError: err.Error()})
		return
	}

	subGroups, err := roster.SubGroupsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading sub-groups for import: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	subGroupByName := map[string]string{}
	for _, sg := range subGroups {
		subGroupByName[strings.ToLower(sg.Name)] = sg.ID
	}

	// familyIDByGroup resolves a family-name group (case-insensitive) to
	// the family ID it maps to, once resolved — either an existing family
	// matched by email, or one created fresh during this import.
	familyIDByGroup := map[string]string{}

	var outcomes []importOutcome
	for _, row := range rows {
		input := fmt.Sprintf("%s,%s,%s,%s,%s,%s,%s", row.FirstName, row.LastName, row.MemberType, row.Email, row.Role, row.FamilyName, row.SubGroup)
		skip := func(reason string) {
			outcomes = append(outcomes, importOutcome{Line: row.Line, Input: input, Reason: reason})
		}

		if row.FirstName == "" || row.LastName == "" {
			skip("first name and last name are required")
			continue
		}
		if row.MemberType == "" {
			skip("member type must be adult/parent/leader or youth/scout/child")
			continue
		}
		if allowed, err := roster.IsAllowedRole(r.Context(), h.Pool, unit.UnitType, unit.ID, scope, row.Role); err != nil || !allowed {
			skip(fmt.Sprintf("you don't have permission to assign the role %q (or it isn't a recognized role)", row.Role))
			continue
		}

		var subGroupPtr *string
		if row.SubGroup != "" {
			id, found := subGroupByName[strings.ToLower(row.SubGroup)]
			if !found {
				skip(fmt.Sprintf("no existing %s named %q — create it first from the roster page", subGroupNoun(unit.UnitType), row.SubGroup))
				continue
			}
			if !scope.CanManageSubGroup(id) {
				skip(fmt.Sprintf("you don't have permission to assign members to %q", row.SubGroup))
				continue
			}
			subGroupPtr = &id
		} else if !scope.UnitWide {
			skip("choose a " + subGroupNoun(unit.UnitType) + " (your account can only manage a specific one)")
			continue
		}

		groupKey := strings.ToLower(strings.TrimSpace(row.FamilyName))
		if groupKey == "" {
			groupKey = "lastname:" + strings.ToLower(row.LastName)
		}

		familyID, alreadyKnown := familyIDByGroup[groupKey]

		if !alreadyKnown && row.Email != "" {
			if existingID, found, err := roster.FamilyIDForEmail(r.Context(), h.Pool, row.Email); err == nil && found {
				familyID = existingID
				alreadyKnown = true
				// Cache this resolution under the group key now, not just
				// after a fresh creation below — otherwise a sibling row in
				// the same family group with no email of its own (e.g. a
				// youth) would find nothing cached and be skipped, even
				// though this row just proved the family already exists.
				familyIDByGroup[groupKey] = familyID
			}
		}

		if !alreadyKnown {
			if row.MemberType != "adult" || row.Email == "" {
				skip("no existing family matched, and this row can't start a new one — the first row for a new family needs member type adult/parent/leader and an email")
				continue
			}
			familyName := row.FamilyName
			if familyName == "" {
				familyName = row.LastName + " Family"
			}
			newFamilyID, memberID, tempPassword, err := roster.CreateFamilyWithMember(r.Context(), h.Pool, roster.NewFamilyInput{
				FamilyName: familyName, Email: row.Email, FirstName: row.FirstName, LastName: row.LastName,
			}, actor.ID)
			if err != nil {
				skip(err.Error())
				continue
			}
			if err := roster.AssignRole(r.Context(), h.Pool, memberID, unit.ID, subGroupPtr, row.Role, actor.ID); err != nil {
				log.Printf("web: assigning role during roster import: %v", err)
			}
			familyIDByGroup[groupKey] = newFamilyID
			outcomes = append(outcomes, importOutcome{
				Line: row.Line, Input: input, Applied: true,
				Detail: fmt.Sprintf("created new family %q with %s %s as %s", familyName, row.FirstName, row.LastName, row.Role),
				Email:  row.Email, TempPass: tempPassword,
			})
			continue
		}

		// The family is already resolved (matched by email, or created
		// earlier in this same batch) — but that only identifies the
		// FAMILY, not this specific person within it. Check for an
		// existing member with the same name before creating a new one,
		// so re-importing the same rows (or importing a row for someone
		// already on the roster) assigns the role to their existing
		// member record instead of quietly duplicating them.
		existingMembers, err := family.MembersForFamily(r.Context(), h.Pool, familyID)
		if err != nil {
			skip(fmt.Sprintf("looking up existing family members: %v", err))
			continue
		}
		memberID := ""
		alreadyOnRoster := false
		for _, m := range existingMembers {
			if strings.EqualFold(m.FirstName, row.FirstName) && strings.EqualFold(m.LastName, row.LastName) {
				memberID = m.ID
				alreadyOnRoster = true
				break
			}
		}
		if memberID == "" {
			memberID, err = roster.AddMember(r.Context(), h.Pool, familyID, row.FirstName, row.LastName, row.MemberType, actor.ID)
			if err != nil {
				skip(fmt.Sprintf("adding member: %v", err))
				continue
			}
		}

		if err := roster.AssignRole(r.Context(), h.Pool, memberID, unit.ID, subGroupPtr, row.Role, actor.ID); err != nil {
			log.Printf("web: assigning role during roster import: %v", err)
		}
		detail := fmt.Sprintf("added %s %s to the existing family as %s", row.FirstName, row.LastName, row.Role)
		if alreadyOnRoster {
			detail = fmt.Sprintf("%s %s already existed in this family — ensured they hold the role %s", row.FirstName, row.LastName, row.Role)
		}
		outcomes = append(outcomes, importOutcome{Line: row.Line, Input: input, Applied: true, Detail: detail})
	}

	applied, skipped := 0, 0
	for _, o := range outcomes {
		if o.Applied {
			applied++
		} else {
			skipped++
		}
	}

	data := struct {
		baseData
		Applied  int
		Skipped  int
		Outcomes []importOutcome
	}{baseData: h.base(r, "Import Results"), Applied: applied, Skipped: skipped, Outcomes: outcomes}
	h.render(w, h.rosterImportResults, data)
}
