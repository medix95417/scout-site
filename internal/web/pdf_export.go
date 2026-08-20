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
