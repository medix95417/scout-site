package web

import (
	"bytes"
	"compress/zlib"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/go-pdf/fpdf"

	"github.com/47-yonkers/scout-site/internal/family"
)

// A roster printed to PDF used to put every row on one fixed-height
// line, so a member with three roles — which the web page stacks — came
// out as one long string drawn straight across the neighbouring columns
// and their borders. These cover the wrapping that replaced it.

// pdfStrings returns every literal string the PDF draws, in order.
// fpdf compresses its content streams, so they are inflated first; the
// text-showing operator it emits is "(...) Tj".
func pdfStrings(t *testing.T, data []byte) []string {
	t.Helper()

	var text strings.Builder
	const begin, end = "stream", "endstream"
	for rest := data; ; {
		i := bytes.Index(rest, []byte(begin))
		if i == -1 {
			break
		}
		body := rest[i+len(begin):]
		j := bytes.Index(body, []byte(end))
		if j == -1 {
			break
		}
		chunk := bytes.TrimLeft(body[:j], "\r\n")
		if zr, err := zlib.NewReader(bytes.NewReader(chunk)); err == nil {
			if inflated, err := io.ReadAll(zr); err == nil {
				text.Write(inflated)
			}
			zr.Close()
		} else {
			text.Write(chunk)
		}
		rest = body[j+len(end):]
	}

	var out []string
	for _, m := range regexp.MustCompile(`\(([^)]*)\)\s*Tj`).FindAllStringSubmatch(text.String(), -1) {
		out = append(out, m[1])
	}
	return out
}

func TestWrapCellHonoursExplicitNewlines(t *testing.T) {
	pdf := fpdf.New("P", "mm", "Letter", "")
	pdf.AddPage()
	pdf.SetFont("Helvetica", "", 9)
	tr := pdf.UnicodeTranslatorFromDescriptor("")

	lines := wrapCell(pdf, tr, "Scoutmaster\nTreasurer\nParent", 28)
	if len(lines) != 3 {
		t.Fatalf("three roles on three lines produced %d lines: %q", len(lines), lines)
	}
	for i, want := range []string{"Scoutmaster", "Treasurer", "Parent"} {
		if lines[i] != want {
			t.Errorf("line %d = %q, want %q", i, lines[i], want)
		}
	}
}

func TestWrapCellWrapsWhatIsTooWide(t *testing.T) {
	pdf := fpdf.New("P", "mm", "Letter", "")
	pdf.AddPage()
	pdf.SetFont("Helvetica", "", 9)
	tr := pdf.UnicodeTranslatorFromDescriptor("")

	long := "Assistant Senior Patrol Leader and Quartermaster for the whole troop"
	lines := wrapCell(pdf, tr, long, 28)
	if len(lines) < 2 {
		t.Fatalf("a line far wider than its column came back as %d line(s): %q", len(lines), lines)
	}
	// Nothing is dropped on the way.
	joined := strings.Join(lines, " ")
	for _, word := range strings.Fields(long) {
		if !strings.Contains(joined, word) {
			t.Errorf("wrapping lost %q: %q", word, lines)
		}
	}

	// An empty cell is still one line, so a row keeps its height.
	if got := wrapCell(pdf, tr, "", 28); len(got) != 1 || got[0] != "" {
		t.Errorf("an empty cell produced %q, want one empty line", got)
	}
	if got := wrapCell(pdf, tr, "   ", 28); len(got) != 1 {
		t.Errorf("a blank cell produced %q, want one line", got)
	}
}

func TestTablePDFDrawsEachLineSeparately(t *testing.T) {
	data, err := simpleTablePDF("Troop 47 — Roster", "",
		[]string{"Name", "Roles"},
		[]float64{40, 28}, []string{"L", "L"},
		[][]string{{"Sam Kowalski", "Scoutmaster\nTreasurer\nParent"}})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}

	drawn := pdfStrings(t, data)
	for _, want := range []string{"Sam Kowalski", "Scoutmaster", "Treasurer", "Parent"} {
		found := false
		for _, s := range drawn {
			if s == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q was never drawn as its own string; drawn: %q", want, drawn)
		}
	}
	// The whole point: three roles are three strings, not one joined one.
	for _, joined := range []string{"Scoutmaster, Treasurer, Parent", "ScoutmasterTreasurerParent"} {
		for _, s := range drawn {
			if s == joined {
				t.Errorf("the roles were drawn as one line %q instead of one per line", joined)
			}
		}
	}
}

// A taller row has to push the next one further down, or the borders and
// the text disagree — which is what overflowing looked like.
func TestTallRowsPushTheNextRowDown(t *testing.T) {
	oneLine, err := simpleTablePDF("t", "", []string{"Name", "Roles"}, []float64{40, 28}, []string{"L", "L"},
		[][]string{{"A", "Parent"}, {"B", "Parent"}})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	threeLines, err := simpleTablePDF("t", "", []string{"Name", "Roles"}, []float64{40, 28}, []string{"L", "L"},
		[][]string{{"A", "Scoutmaster\nTreasurer\nParent"}, {"B", "Parent"}})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if len(threeLines) <= len(oneLine) {
		t.Errorf("a table with a three-line row (%d bytes) is no larger than the same table with one-line rows (%d bytes), so the extra lines went nowhere",
			len(threeLines), len(oneLine))
	}
}

// A roster long enough to spill onto a second page repeats its column
// headings there. Page two of a printed roster without them is a puzzle.
func TestALongTableRepeatsItsHeader(t *testing.T) {
	rows := make([][]string, 80)
	for i := range rows {
		rows[i] = []string{"Member", "Parent"}
	}
	data, err := simpleTablePDF("t", "", []string{"Name", "Roles"}, []float64{40, 28}, []string{"L", "L"}, rows)
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}

	headings := 0
	for _, s := range pdfStrings(t, data) {
		if s == "Name" {
			headings++
		}
	}
	if headings < 2 {
		t.Errorf("the column heading was drawn %d time(s) across a multi-page table, want one per page", headings)
	}
}

func TestRosterContactParts(t *testing.T) {
	full := family.RosterEntry{}
	full.Email = "sam@example.com"
	full.HomePhone = "555-0100"
	full.CellPhone = "555-0101"
	got := rosterContactParts(full)
	want := []string{"sam@example.com", "home 555-0100", "cell 555-0101"}
	if len(got) != len(want) {
		t.Fatalf("rosterContactParts = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("part %d = %q, want %q", i, got[i], want[i])
		}
	}

	// Nothing released means nothing listed — not a row of empty labels.
	if got := rosterContactParts(family.RosterEntry{}); len(got) != 0 {
		t.Errorf("a member who has released nothing produced %q", got)
	}
}
