package prospect

// Adding a list of prospects at once.
//
// A unit comes back from a school recruiting night, or gets a lead list
// from the council, with twenty families on it. Typing twenty into a
// form is how a list stays in a spreadsheet instead, so this parses
// what a leader pastes — straight out of a spreadsheet, or typed with
// commas — into rows they can check before anything is stored.
//
// Nothing here writes. Parsing and saving are deliberately separate so
// the page can show what it understood and what it is skipping, and the
// leader can fix the list and paste it again, rather than discovering
// afterwards that six rows went in wrong.

import (
	"context"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PasteColumns is the order of fields in a pasted row, and what the
// page tells the leader to arrange their spreadsheet as. Only the first
// four matter; a row can stop early.
var PasteColumns = []string{
	"Parent name", "Parent email", "Phone", "Child name", "Age", "Grade", "School",
}

// maxPasteRows caps one paste. A list longer than this is a data import
// rather than a recruiting night, and doing it in a browser request —
// where a timeout leaves half of it stored — is the wrong shape for it.
const maxPasteRows = 200

// PasteRow is one line of a pasted list, as understood.
type PasteRow struct {
	Line int // 1-based, as the leader sees it in their paste
	New      // what would be stored, when Skip is empty
	// Skip says why this row will not be stored, and is shown against
	// the line. Empty means it will be.
	Skip string
	// Duplicate marks a row skipped because this unit already has an
	// enquiry from that address — worth distinguishing from a bad row,
	// since pasting the same list twice is a normal thing to do and
	// should read as "already have them", not as an error.
	Duplicate bool
}

// Ready reports whether this row would be stored.
func (r PasteRow) Ready() bool { return r.Skip == "" }

// ParsePaste turns pasted text into rows to confirm.
//
// Tab-separated and comma-separated both work, decided per line by
// whether it contains a tab: a spreadsheet paste is tabs, and a name
// like "Rivera, Jamie" would otherwise split a tab-separated row in the
// wrong place. Commas are parsed as CSV so a quoted field containing
// one survives.
//
// Every row comes back, including the ones that will be skipped and
// why. A caller that only wants the storable ones filters on Ready;
// showing the rest is the point of the preview.
func ParsePaste(ctx context.Context, pool *pgxpool.Pool, unitID, raw string) ([]PasteRow, error) {
	existing, err := emailsForUnit(ctx, pool, unitID)
	if err != nil {
		return nil, err
	}

	var out []PasteRow
	seen := map[string]bool{}
	line := 0
	for _, text := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(text) == "" {
			continue // a blank line is spacing, not a row
		}
		line++
		if len(out) >= maxPasteRows {
			out = append(out, PasteRow{
				Line: line,
				Skip: fmt.Sprintf("more than %d rows in one paste — split the list", maxPasteRows),
			})
			break
		}

		fields, err := splitPasteLine(text)
		if err != nil {
			out = append(out, PasteRow{Line: line, Skip: "couldn't read this line: " + err.Error()})
			continue
		}
		// A header row, pasted along with the data, names its columns
		// rather than a family. Recognised so it doesn't come back as a
		// puzzling failure on line 1 every time.
		if line == 1 && looksLikeHeader(fields) {
			line--
			continue
		}

		row := PasteRow{Line: line, New: New{UnitID: unitID, Source: SourceLeader}}
		row.ParentName = field(fields, 0)
		row.ParentEmail = field(fields, 1)
		row.ParentPhone = field(fields, 2)
		row.ChildName = field(fields, 3)
		row.ChildGrade = field(fields, 5)
		row.ChildSchool = field(fields, 6)

		if age := field(fields, 4); age != "" {
			n, err := strconv.Atoi(age)
			if err != nil {
				row.Skip = fmt.Sprintf("age %q isn't a number", age)
			} else {
				row.ChildAge = &n
			}
		}

		// Validated by the same function that will store it, so the
		// preview cannot promise a row the save then refuses.
		if row.Skip == "" {
			cleaned, err := Validate(row.New)
			if err != nil {
				row.Skip = strings.TrimPrefix(err.Error(), "prospect: invalid submission: ")
			} else {
				row.New = cleaned
			}
		}

		key := strings.ToLower(row.ParentEmail)
		switch {
		case row.Skip != "":
			// already skipped for a better reason
		case existing[key]:
			row.Skip, row.Duplicate = "already on this unit's list", true
		case seen[key]:
			row.Skip, row.Duplicate = "the same address appears earlier in this paste", true
		default:
			seen[key] = true
		}

		out = append(out, row)
	}
	return out, nil
}

// field returns one column, or "" past the end of a short row.
func field(fields []string, i int) string {
	if i >= len(fields) {
		return ""
	}
	return strings.TrimSpace(fields[i])
}

// splitPasteLine splits one line into columns: tabs when there are
// any, CSV otherwise.
func splitPasteLine(text string) ([]string, error) {
	if strings.Contains(text, "\t") {
		return strings.Split(text, "\t"), nil
	}
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1 // rows may be short; that is handled per field
	r.LazyQuotes = true    // a stray quote in a name is not worth refusing a row over
	return r.Read()
}

// looksLikeHeader reports whether the first pasted line names columns
// rather than a family — matched on the two fields that are always
// required, so a family genuinely called "Parent name" is the only
// false positive and is not a real one.
func looksLikeHeader(fields []string) bool {
	if len(fields) < 2 {
		return false
	}
	first := strings.ToLower(strings.TrimSpace(fields[0]))
	second := strings.ToLower(strings.TrimSpace(fields[1]))
	return strings.Contains(first, "name") && strings.Contains(second, "email")
}

// emailsForUnit is every address this unit already has an enquiry from,
// lowercased — so pasting a list twice reports the second one as
// already held rather than creating a duplicate of everybody.
func emailsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) (map[string]bool, error) {
	rows, err := pool.Query(ctx,
		`SELECT lower(parent_email) FROM prospects WHERE unit_id = $1`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out[e] = true
	}
	return out, rows.Err()
}

// SavePaste stores the rows of a parsed paste that are ready, and
// returns how many were stored.
//
// Re-parsed by the caller immediately before this rather than carried
// across the confirmation step, so what is stored is decided against
// the database as it is now — two leaders pasting overlapping lists a
// minute apart should not both add the same family because each was
// shown a preview taken before the other saved.
func SavePaste(ctx context.Context, pool *pgxpool.Pool, rows []PasteRow, actorID string) (int, error) {
	n := 0
	for _, row := range rows {
		if !row.Ready() {
			continue
		}
		if _, err := AddByLeader(ctx, pool, row.New, actorID); err != nil {
			return n, fmt.Errorf("line %d: %w", row.Line, err)
		}
		n++
	}
	return n, nil
}
