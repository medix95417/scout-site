package prospect

import (
	"context"
	"strings"
	"testing"
)

// Pasting a list is somebody's spreadsheet arriving by clipboard, so
// the parser's job is to be forgiving about shape and unforgiving about
// what it will store — and to say, per line, which it decided.

func TestParsePaste(t *testing.T) {
	e := newEnv(t, "Paste Family")
	ctx := context.Background()

	raw := strings.Join([]string{
		// A header row, pasted along with the data.
		"Parent name\tParent email\tPhone\tChild name\tAge\tGrade\tSchool",
		"Jamie Rivera\tjamie@example.com\t555-0100\tSam Rivera\t9\t4th\tRiverside Elementary",
		// Short rows are fine — only the first four columns matter.
		"Alex Chen\talex@example.com\t\tRobin Chen",
		"",
		// Commas work too, quoted where a field contains one.
		`Morgan Patel,morgan@example.com,555-0111,"Patel, Kit",10`,
	}, "\n")

	rows, err := ParsePaste(ctx, e.pool, e.unitID, raw)
	if err != nil {
		t.Fatalf("ParsePaste: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (the header and the blank line are not families): %+v", len(rows), rows)
	}
	for _, r := range rows {
		if !r.Ready() {
			t.Errorf("line %d was skipped: %s", r.Line, r.Skip)
		}
	}
	if rows[0].ParentName != "Jamie Rivera" || rows[0].ParentEmail != "jamie@example.com" {
		t.Errorf("first row parsed as %+v", rows[0].New)
	}
	if rows[0].ChildAge == nil || *rows[0].ChildAge != 9 {
		t.Errorf("age didn't parse: %v", rows[0].ChildAge)
	}
	if rows[1].ChildName != "Robin Chen" {
		t.Errorf("a short row lost its child's name: %+v", rows[1].New)
	}
	if rows[1].ChildAge != nil {
		t.Error("a row with no age column came back with one")
	}
	// The comma inside a quoted field must not have split the row.
	if rows[2].ChildName != "Patel, Kit" {
		t.Errorf("quoted comma mishandled, child name = %q", rows[2].ChildName)
	}

	// Every row is marked as hand-added, which is what keeps the
	// automatic reply away from a list that never asked for one.
	for _, r := range rows {
		if r.Source != SourceLeader {
			t.Errorf("line %d has source %q, want %q", r.Line, r.Source, SourceLeader)
		}
	}
}

// A tab-separated line must not be split on the commas inside a name,
// which is the failure a spreadsheet paste would hit constantly.
func TestParsePastePrefersTabsWhenThereAreAny(t *testing.T) {
	e := newEnv(t, "Tab Family")
	rows, err := ParsePaste(context.Background(), e.pool, e.unitID,
		"Rivera, Jamie\tjamie@example.com\t\tRivera, Sam")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].ParentName != "Rivera, Jamie" || rows[0].ChildName != "Rivera, Sam" {
		t.Errorf("a tabbed line was split on its commas: %+v", rows[0].New)
	}
}

// What the preview says it will skip, and why.
func TestParsePasteSkipsWhatItCannotStore(t *testing.T) {
	e := newEnv(t, "Skip Family")
	ctx := context.Background()

	// Someone already on the list, to prove a re-paste reports rather
	// than duplicates.
	if _, err := Create(ctx, e.pool, New{
		UnitID: e.unitID, ParentName: "Existing Parent",
		ParentEmail: "existing@example.com", ChildName: "Existing Child",
	}); err != nil {
		t.Fatal(err)
	}

	raw := strings.Join([]string{
		"No Email Here\t\t555-0100\tChild One",
		"Bad Address\tnot-an-email\t\tChild Two",
		"\tnoname@example.com\t\tChild Three",
		"No Child\tnochild@example.com",
		"Bad Age\tbadage@example.com\t\tChild Four\tnine",
		"Existing Parent\texisting@example.com\t\tExisting Child",
		"Twice Over\ttwice@example.com\t\tChild Five",
		"Twice Again\tTWICE@example.com\t\tChild Six",
		"Fine Parent\tfine@example.com\t\tChild Seven",
	}, "\n")

	rows, err := ParsePaste(ctx, e.pool, e.unitID, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 9 {
		t.Fatalf("got %d rows, want 9 — every line comes back, skipped or not", len(rows))
	}

	ready := map[int]bool{7: true, 9: true}
	for _, r := range rows {
		if got := r.Ready(); got != ready[r.Line] {
			t.Errorf("line %d: ready = %v (%s), want %v", r.Line, got, r.Skip, ready[r.Line])
		}
	}

	// The two kinds of skip read differently, because pasting a list
	// twice is normal and a malformed row is not.
	if !rows[5].Duplicate {
		t.Error("someone already on the list wasn't marked as a duplicate")
	}
	if !rows[7].Duplicate {
		t.Error("the same address twice in one paste wasn't marked as a duplicate")
	}
	if rows[0].Duplicate || rows[4].Duplicate {
		t.Error("a malformed row was reported as a duplicate")
	}
	// Case doesn't make a new family.
	if !strings.Contains(rows[7].Skip, "earlier in this paste") {
		t.Errorf("TWICE@ vs twice@ wasn't caught: %q", rows[7].Skip)
	}
}

// Parsing stores nothing. The preview exists precisely so a leader can
// look before anything happens.
func TestParsePasteStoresNothing(t *testing.T) {
	e := newEnv(t, "Dry Run Family")
	ctx := context.Background()

	if _, err := ParsePaste(ctx, e.pool, e.unitID,
		"Jamie Rivera\tjamie@example.com\t\tSam Rivera"); err != nil {
		t.Fatal(err)
	}
	list, err := ListForUnit(ctx, e.pool, e.unitID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("parsing a paste stored %d prospects", len(list))
	}
}

// Saving stores the ready rows and only those.
func TestSavePaste(t *testing.T) {
	e := newEnv(t, "Save Family")
	ctx := context.Background()

	rows, err := ParsePaste(ctx, e.pool, e.unitID, strings.Join([]string{
		"Jamie Rivera\tjamie@example.com\t\tSam Rivera",
		"Broken Row\tnot-an-email\t\tChild",
		"Alex Chen\talex@example.com\t\tRobin Chen",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}

	n, err := SavePaste(ctx, e.pool, rows, e.actor)
	if err != nil {
		t.Fatalf("SavePaste: %v", err)
	}
	if n != 2 {
		t.Errorf("stored %d, want 2", n)
	}

	list, err := ListForUnit(ctx, e.pool, e.unitID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("the unit has %d prospects, want 2", len(list))
	}
	for _, p := range list {
		if p.Source != SourceLeader {
			t.Errorf("%s was stored with source %q, want %q", p.ParentEmail, p.Source, SourceLeader)
		}
		if p.Status != StatusNew {
			t.Errorf("%s was stored at status %q, want a new enquiry", p.ParentEmail, p.Status)
		}
	}

	// And a second run of the same list adds nobody.
	again, err := ParsePaste(ctx, e.pool, e.unitID, "Jamie Rivera\tjamie@example.com\t\tSam Rivera")
	if err != nil {
		t.Fatal(err)
	}
	n2, err := SavePaste(ctx, e.pool, again, e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("re-pasting the same list added %d", n2)
	}
}

// One paste is capped. A longer list is a data import, and doing it in
// a browser request — where a timeout leaves half of it stored — is the
// wrong shape for one.
func TestParsePasteCapsOnePaste(t *testing.T) {
	e := newEnv(t, "Long Family")
	var b strings.Builder
	for i := 0; i < maxPasteRows+10; i++ {
		b.WriteString("Parent\tp")
		b.WriteString(strings.Repeat("x", 1))
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString("@example.com\t\tChild\n")
	}
	rows, err := ParsePaste(context.Background(), e.pool, e.unitID, b.String())
	if err != nil {
		t.Fatal(err)
	}
	last := rows[len(rows)-1]
	if last.Ready() || !strings.Contains(last.Skip, "split the list") {
		t.Errorf("an over-long paste wasn't stopped: last row = %+v", last)
	}
	ready := 0
	for _, r := range rows {
		if r.Ready() {
			ready++
		}
	}
	if ready > maxPasteRows {
		t.Errorf("%d rows would be stored, over the cap of %d", ready, maxPasteRows)
	}
}

// A hand-added prospect is audited against whoever added it, where a
// form submission has no actor to audit. "Who put this family on our
// list" is the first question when a name turns out not to want to be
// there.
func TestAddByLeaderIsAudited(t *testing.T) {
	e := newEnv(t, "Audit Family")
	ctx := context.Background()

	p, err := AddByLeader(ctx, e.pool, New{
		UnitID: e.unitID, ParentName: "Jamie Rivera",
		ParentEmail: "jamie@example.com", ChildName: "Sam Rivera",
	}, e.actor)
	if err != nil {
		t.Fatalf("AddByLeader: %v", err)
	}
	if p.Source != SourceLeader {
		t.Errorf("source = %q, want %q", p.Source, SourceLeader)
	}

	var n int
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'prospect' AND entity_id = $1 AND action = 'create'`,
		p.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d audit entries for a hand-added prospect, want 1", n)
	}

	// The form's own path stays unaudited — there is no member to
	// attribute it to.
	f, err := Create(ctx, e.pool, New{
		UnitID: e.unitID, ParentName: "Form Parent",
		ParentEmail: "form@example.com", ChildName: "Form Child",
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Source != SourceForm {
		t.Errorf("a form submission has source %q, want %q", f.Source, SourceForm)
	}
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'prospect' AND entity_id = $1`,
		f.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("a form submission produced %d audit entries", n)
	}
}
