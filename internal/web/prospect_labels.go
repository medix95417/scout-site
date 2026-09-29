package web

// Managing a unit's prospect statuses and categories.
//
// The five statuses used to be a Postgres enum, the same for every
// unit: a Troop wanting "Left voicemail" between "New enquiry" and
// "Contacted" needed a migration and a release. They are per-unit rows
// now (see internal/prospect/labels.go and migration 0048), edited
// here, and a category list sits alongside them for the other kind of
// sorting — which program a family asked about, how they heard of the
// unit — that overloading the status dropdown would have muddled.

import (
	"errors"
	"log"
	"net/http"
	"net/url"

	"github.com/47-yonkers/scout-site/internal/prospect"
)

// prospectLabelsPageData is what admin-prospect-labels.html renders.
type prospectLabelsPageData struct {
	baseData
	Statuses   []prospect.Label
	Categories []prospect.Label
	// Counts says how many prospects sit on each value, so a leader can
	// see what retiring one would affect before doing it.
	Counts map[string]int
	Error  string
}

// ProspectLabels is the management page for both lists.
func (h *Handlers) ProspectLabels(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireContentEditor(w, r, "/admin/prospect-labels")
	if !ok {
		return
	}
	if err := prospect.EnsureDefaults(r.Context(), h.Pool, unit.ID); err != nil {
		log.Printf("web: ensuring default prospect statuses: %v", err)
	}

	// Retired ones are shown here, and only here: this is the page for
	// deciding what the lists should be, which means seeing what has
	// been withdrawn and being able to put it back.
	statuses, err := prospect.ListLabels(r.Context(), h.Pool, unit.ID, prospect.KindStatus, true)
	if err != nil {
		log.Printf("web: listing prospect statuses: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	categories, err := prospect.ListLabels(r.Context(), h.Pool, unit.ID, prospect.KindCategory, true)
	if err != nil {
		log.Printf("web: listing prospect categories: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	counts, err := prospect.CountByLabel(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: counting prospects per label: %v", err)
	}

	h.render(w, h.prospectLabelsPage, prospectLabelsPageData{
		baseData:   h.base(r, "Prospect Statuses & Categories"),
		Statuses:   statuses,
		Categories: categories,
		Counts:     counts,
		Error:      r.URL.Query().Get("error"),
	})
}

// ProspectLabelCreate adds one to the end of a list.
func (h *Handlers) ProspectLabelCreate(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/prospect-labels")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	_, err := prospect.CreateLabel(r.Context(), h.Pool, unit.ID,
		r.FormValue("kind"), r.FormValue("label"), r.FormValue("closed") == "1", actor.ID)
	h.backToLabels(w, r, err)
}

// ProspectLabelRename changes what a label reads as. The stored value
// doesn't move, so every prospect and campaign already using it simply
// reads the new name.
func (h *Handlers) ProspectLabelRename(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/prospect-labels")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	_, err := prospect.RenameLabel(r.Context(), h.Pool, unit.ID, r.PathValue("id"), r.FormValue("label"), actor.ID)
	h.backToLabels(w, r, err)
}

// ProspectLabelClosed sets whether a status counts as dealt with.
func (h *Handlers) ProspectLabelClosed(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/prospect-labels")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	_, err := prospect.SetLabelClosed(r.Context(), h.Pool, unit.ID, r.PathValue("id"), r.FormValue("closed") == "1", actor.ID)
	h.backToLabels(w, r, err)
}

// ProspectLabelRetire withdraws a label from the pickers, or restores
// it. There is no delete: see SetLabelRetired for why.
func (h *Handlers) ProspectLabelRetire(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/prospect-labels")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	_, err := prospect.SetLabelRetired(r.Context(), h.Pool, unit.ID, r.PathValue("id"), r.FormValue("retired") == "1", actor.ID)
	h.backToLabels(w, r, err)
}

// ProspectLabelMove shifts one up or down its list.
func (h *Handlers) ProspectLabelMove(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/prospect-labels")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	err := prospect.MoveLabel(r.Context(), h.Pool, unit.ID, r.PathValue("id"), r.FormValue("dir") == "up", actor.ID)
	h.backToLabels(w, r, err)
}

// backToLabels returns to the management page, carrying a refusal back
// as a message on it rather than as a bare error page — every one of
// these is something a leader typed and can fix in the form they are
// looking at.
func (h *Handlers) backToLabels(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		http.Redirect(w, r, "/admin/prospect-labels", http.StatusSeeOther)
		return
	}
	switch {
	case errors.Is(err, prospect.ErrLabelNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, prospect.ErrInvalid),
		errors.Is(err, prospect.ErrLabelExists),
		errors.Is(err, prospect.ErrProtectedLabel):
		http.Redirect(w, r, "/admin/prospect-labels?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	log.Printf("web: editing prospect labels: %v", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
