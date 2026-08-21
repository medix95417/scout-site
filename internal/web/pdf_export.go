package web

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/go-pdf/fpdf"
)

// This file renders the simple, printable family-contact-list PDF shared by
// /directory/export.pdf (every family, contact fields already
// release-filtered by roster.DirectoryForUnit) and /my-family/export.pdf
// (just the caller's own family, unfiltered — same "see your own stuff
// regardless of release settings" rule /my-family's own page already
// follows). Deliberately a real, server-generated PDF rather than a
// print stylesheet alone — see CHANGELOG.md's entry for why.

// pdfMember is the minimal shape the PDF needs, decoupled from whichever
// roster type (roster.DirectoryEntry.Members or roster.MemberDetail)
// supplies it.
type pdfMember struct {
	Name      string
	Email     string
	HomePhone string
	CellPhone string
}

type pdfFamily struct {
	Name    string
	Address string
	Members []pdfMember
}

// familyDirectoryPDF renders one page: a title, then one section per
// family listing its members and whatever contact info the caller passed
// in (already filtered/unfiltered as appropriate — this function has no
// opinion on that).
func familyDirectoryPDF(title string, families []pdfFamily) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "Letter", "")
	pdf.SetMargins(15, 15, 15)
	pdf.AddPage()

	// The core Helvetica font expects text in cp1252, not UTF-8 — passing a
	// Go string's raw UTF-8 bytes straight to Cell/CellFormat renders
	// mojibake for anything beyond ASCII (an em-dash, a middle dot, or an
	// accented name like "Muñoz"). tr converts each string to the byte
	// sequence the font actually expects; cp1252 is a superset of Latin-1,
	// so this covers everything this page's data realistically contains.
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	title = tr(title)

	pdf.SetTitle(title, true)
	pdf.SetFont("Helvetica", "B", 16)
	pdf.CellFormat(0, 10, title, "", 1, "L", false, 0, "")
	pdf.Ln(4)

	for _, fam := range families {
		pdf.SetFont("Helvetica", "B", 12)
		pdf.CellFormat(0, 8, tr(fam.Name), "", 1, "L", false, 0, "")
		if fam.Address != "" {
			pdf.SetFont("Helvetica", "", 10)
			pdf.CellFormat(0, 6, tr(fam.Address), "", 1, "L", false, 0, "")
		}
		pdf.SetFont("Helvetica", "", 10)
		for _, m := range fam.Members {
			line := m.Name
			var extras []string
			if m.Email != "" {
				extras = append(extras, m.Email)
			}
			if m.HomePhone != "" {
				extras = append(extras, "home "+m.HomePhone)
			}
			if m.CellPhone != "" {
				extras = append(extras, "cell "+m.CellPhone)
			}
			if len(extras) > 0 {
				line += "  -  " + strings.Join(extras, "   |   ")
			}
			pdf.CellFormat(0, 6, tr(line), "", 1, "L", false, 0, "")
		}
		pdf.Ln(4)
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writePDF sets the response headers for a downloadable PDF and writes the
// bytes — shared by both export handlers below.
func writePDF(w http.ResponseWriter, filename string, data []byte) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Write(data) //nolint:errcheck // nothing meaningful to do if the client already disconnected
}

// This section renders a Scout ledger account's printable, date-ranged
// statement — shared by a Scout/family printing their own account(s) (see
// /treasury/accounts/{id}/export.pdf and /accounts/export.pdf in
// treasury.go/accounts.go) and, later, a Treasurer's per-account report.
// One PDF can hold several accounts' statements back to back (one per
// page) — the family print flow's "more than one Scout, sorted by
// Scout" requirement.

// ledgerStatementRow is one line of a printed statement — a transaction's
// date/description/type alongside the signed amount and running balance
// *for the one account the statement is for*, not every account the
// underlying transaction touched.
type ledgerStatementRow struct {
	Date            string
	Description     string
	TransactionType string
	AmountCents     int64
	RunningCents    int64
}

// ledgerStatementSection is one account's statement for a date range —
// the unit of repetition in a multi-Scout family printout.
type ledgerStatementSection struct {
	Heading        string // e.g. "Riley Morgan — Individual Account"
	DateRangeLabel string // e.g. "Jan 1, 2026 – Mar 31, 2026"
	StartingCents  int64
	EndingCents    int64
	Rows           []ledgerStatementRow
}

// ledgerStatementPDF renders one section per account, each starting on
// its own page so a family printing several Scouts at once gets a clean
// break between them (the "sorted by Scout" requirement — callers pass
// sections in whatever order they want that grouping to read in).
func ledgerStatementPDF(title string, sections []ledgerStatementSection) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "Letter", "")
	pdf.SetMargins(15, 15, 15)
	pdf.AddPage()
	tr := pdf.UnicodeTranslatorFromDescriptor("")

	pdf.SetTitle(tr(title), true)
	pdf.SetFont("Helvetica", "B", 16)
	pdf.CellFormat(0, 10, tr(title), "", 1, "L", false, 0, "")
	pdf.Ln(2)

	for i, sec := range sections {
		if i > 0 {
			pdf.AddPage()
		}
		pdf.SetFont("Helvetica", "B", 13)
		pdf.CellFormat(0, 8, tr(sec.Heading), "", 1, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 10)
		pdf.CellFormat(0, 6, tr(sec.DateRangeLabel), "", 1, "L", false, 0, "")
		pdf.Ln(2)

		pdf.SetFont("Helvetica", "B", 9)
		pdf.CellFormat(0, 6, tr("Starting balance: "+formatCents(sec.StartingCents)), "", 1, "L", false, 0, "")
		pdf.Ln(2)

		pdf.SetFillColor(240, 240, 240)
		pdf.SetFont("Helvetica", "B", 9)
		pdf.CellFormat(28, 7, "Date", "1", 0, "L", true, 0, "")
		pdf.CellFormat(82, 7, "Description", "1", 0, "L", true, 0, "")
		pdf.CellFormat(30, 7, "Amount", "1", 0, "R", true, 0, "")
		pdf.CellFormat(30, 7, "Balance", "1", 1, "R", true, 0, "")

		pdf.SetFont("Helvetica", "", 9)
		if len(sec.Rows) == 0 {
			pdf.CellFormat(170, 7, tr("No activity in this date range."), "1", 1, "L", false, 0, "")
		}
		for _, row := range sec.Rows {
			pdf.CellFormat(28, 7, tr(row.Date), "1", 0, "L", false, 0, "")
			pdf.CellFormat(82, 7, tr(row.Description), "1", 0, "L", false, 0, "")
			pdf.CellFormat(30, 7, tr(formatCents(row.AmountCents)), "1", 0, "R", false, 0, "")
			pdf.CellFormat(30, 7, tr(formatCents(row.RunningCents)), "1", 1, "R", false, 0, "")
		}

		pdf.Ln(2)
		pdf.SetFont("Helvetica", "B", 9)
		pdf.CellFormat(0, 6, tr("Ending balance: "+formatCents(sec.EndingCents)), "", 1, "L", false, 0, "")
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
