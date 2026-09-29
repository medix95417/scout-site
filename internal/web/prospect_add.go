package web

// Adding prospects by hand.
//
// Until now the only way a prospect existed was a family filling in the
// public form. A unit also comes back from a school recruiting night,
// or gets a lead list from the council, with names that ought to be
// tracked the same way — followed up, moved along, not lost in
// somebody's inbox.
//
// Neither path here sends the family anything. A form submission gets
// an automatic reply because they just asked and are probably still
// looking at the screen; someone off a list asked for nothing, and a
// "thanks for your enquiry" they do not recognise is at best confusing
// and at worst the first thing they know about a unit holding their
// address. The leaders' notification is skipped too, for the simpler
// reason that the leader is the one typing.

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/47-yonkers/scout-site/internal/prospect"
)

// ProspectAdd records one enquiry a leader typed in.
func (h *Handlers) ProspectAdd(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/prospects")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	in := prospect.New{
		UnitID:      unit.ID,
		ParentName:  r.FormValue("parent_name"),
		ParentEmail: r.FormValue("parent_email"),
		ParentPhone: r.FormValue("parent_phone"),
		ChildName:   r.FormValue("child_name"),
		ChildGrade:  r.FormValue("child_grade"),
		ChildSchool: r.FormValue("child_school"),
		Message:     r.FormValue("message"),
	}
	if raw := strings.TrimSpace(r.FormValue("child_age")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			h.backToProspects(w, r, "Enter the child's age as a number, or leave it blank.")
			return
		}
		in.ChildAge = &n
	}

	if _, err := prospect.AddByLeader(r.Context(), h.Pool, in, actor.ID); err != nil {
		if errors.Is(err, prospect.ErrInvalid) {
			h.backToProspects(w, r, sentence(strings.TrimPrefix(err.Error(), "prospect: invalid submission: ")))
			return
		}
		log.Printf("web: adding a prospect by hand: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, prospectReturnTo(r), http.StatusSeeOther)
}

// prospectImportData is what admin-prospect-import.html renders: the
// paste as understood, before anything is stored.
type prospectImportData struct {
	baseData
	// Raw is the pasted text, carried through the confirmation step so
	// the leader can go back and correct it without retyping, and so
	// the save re-parses rather than trusting a list of rows posted
	// back from a browser.
	Raw     string
	Rows    []prospect.PasteRow
	Ready   int
	Skipped int
	Columns []string
	Error   string
}

// ProspectImportPreview parses a pasted list and shows what it would
// do, without storing any of it.
//
// A separate step rather than saving straight off, because the input is
// somebody's spreadsheet: a column in the wrong order, an age in a
// grade field, half the list already on the page from last month. All
// of that is cheap to look at and tedious to undo — a prospect is
// deleted one at a time.
func (h *Handlers) ProspectImportPreview(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireContentEditor(w, r, "/admin/prospects")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	raw := r.FormValue("rows")

	rows, err := prospect.ParsePaste(r.Context(), h.Pool, unit.ID, raw)
	if err != nil {
		log.Printf("web: parsing a pasted prospect list: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := prospectImportData{
		baseData: h.base(r, "Add a list of prospects"),
		Raw:      raw,
		Rows:     rows,
		Columns:  prospect.PasteColumns,
	}
	for _, row := range rows {
		if row.Ready() {
			data.Ready++
		} else {
			data.Skipped++
		}
	}
	if len(rows) == 0 {
		data.Error = "There was nothing to read in that. Paste one family per line."
	}
	h.render(w, h.prospectImport, data)
}

// ProspectImportSave stores the rows of a confirmed paste.
//
// The text is parsed again here rather than the preview's rows being
// posted back. Two reasons, and the second is the important one: a list
// of rows round-tripping through a browser is a list a browser could
// have edited, and re-parsing means what gets stored is decided against
// the database as it is now — two leaders pasting overlapping lists a
// minute apart would otherwise both add the same family, each having
// been shown a preview taken before the other saved.
func (h *Handlers) ProspectImportSave(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/prospects")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	rows, err := prospect.ParsePaste(r.Context(), h.Pool, unit.ID, r.FormValue("rows"))
	if err != nil {
		log.Printf("web: parsing a pasted prospect list: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	n, err := prospect.SavePaste(r.Context(), h.Pool, rows, actor.ID)
	if err != nil {
		// Some may already be stored — reported as such rather than
		// pretending the whole thing failed, since the leader needs to
		// know not to paste it again wholesale.
		log.Printf("web: saving a pasted prospect list (%d stored before the failure): %v", n, err)
		h.backToProspects(w, r, "Something went wrong part-way through that list. "+
			strconv.Itoa(n)+" were added; check the list below before pasting the rest again.")
		return
	}
	h.backToProspects(w, r, "")
}

// backToProspects returns to the list, carrying a message about the add
// when there is one.
func (h *Handlers) backToProspects(w http.ResponseWriter, r *http.Request, msg string) {
	to := prospectReturnTo(r)
	if msg != "" {
		sep := "?"
		if strings.Contains(to, "?") {
			sep = "&"
		}
		to += sep + "add_error=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// sentence capitalises a validation message so it reads as one on its
// own, rather than as the tail of the sentence it was written for.
func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}
