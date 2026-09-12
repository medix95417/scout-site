package web

// Acting on several files at once, from the file library's own list.
//
// Everything here was already possible one file at a time; what wasn't
// possible was doing it to the forty photos that came off one phone. The
// two jobs that actually come up are "these should be public" (or should
// stop being) and "these went in under the wrong event" — the second
// being the one worth having, since the alternative is opening forty
// link forms.
//
// The selection posts from checkboxes that live inside each file's card
// but belong, via HTML's form attribute, to the one bulk form at the top
// of the page. That indirection is deliberate: the cards already contain
// their own rename/delete/category forms, and a form cannot be nested
// inside another one.

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/units"
)

// maxBulkFiles bounds one submission. Comfortably more than a phone's
// camera roll, low enough that a hand-crafted post can't ask for a
// hundred thousand ids in one transaction.
const maxBulkFiles = 500

// FileBulkUpdate applies one action to every selected file.
//
// Same CanEditUnitContent gate as every other management action on this
// page, and every query it makes is scoped to the current unit in SQL
// (see files.SetPublicMany / LinkEventMany / UnlinkEventsMany) rather
// than by checking each id here — the ids arrive as a list off a form,
// so "which of these are yours" is a question for the database, and one
// id from the other unit must be silently skipped rather than quietly
// acted on.
func (h *Handlers) FileBulkUpdate(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanEditUnitContent(caps) {
		http.Error(w, "you don't have permission to manage files", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	ids := r.Form["file_ids"]
	if len(ids) == 0 {
		http.Redirect(w, r, "/files?bulk=none", http.StatusSeeOther)
		return
	}
	if len(ids) > maxBulkFiles {
		http.Error(w, "that's more files than one action can take at a time — select fewer and repeat", http.StatusBadRequest)
		return
	}

	action := r.FormValue("action")
	eventID := r.FormValue("event_id")
	var n int

	switch action {
	case "public", "private":
		changed, err := files.SetPublicMany(r.Context(), h.Pool, unit.ID, ids, action == "public")
		if err != nil {
			log.Printf("web: bulk public flag: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		n = int(changed)
	case "link", "move":
		if eventID == "" {
			http.Redirect(w, r, "/files?bulk=noevent", http.StatusSeeOther)
			return
		}
		changed, err := files.LinkEventMany(r.Context(), h.Pool, unit.ID, eventID, ids, action == "move")
		if errors.Is(err, files.ErrEventNotInUnit) {
			http.Error(w, "that event doesn't belong to this unit", http.StatusBadRequest)
			return
		}
		if err != nil {
			log.Printf("web: bulk event link: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		n = changed
	case "unlink":
		changed, err := files.UnlinkEventsMany(r.Context(), h.Pool, unit.ID, ids)
		if err != nil {
			log.Printf("web: bulk event unlink: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		n = int(changed)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/files?bulk="+action+"&n="+strconv.Itoa(n), http.StatusSeeOther)
}

// bulkResultMessage turns the redirect's own query params back into the
// sentence shown at the top of the page.
//
// The message is built here from a fixed set of action names rather than
// carried in the URL as text, so nothing a visitor types can be handed
// back to them as page content — html/template would escape it anyway,
// but a reflected message is still a message somebody else wrote.
func bulkResultMessage(action, count string) string {
	n, err := strconv.Atoi(count)
	if err != nil || n < 0 {
		n = 0
	}
	// Named "label" rather than "files" so it doesn't shadow the
	// package of that name in a file that uses it.
	label := strconv.Itoa(n) + " file"
	if n != 1 {
		label += "s"
	}

	switch action {
	case "none":
		return "Nothing was selected — tick the box on a file or two first."
	case "noevent":
		return "Choose an event first, then pick what to do with the selected files."
	case "public":
		return label + " are now public — anyone with the link can open them, signed in or not."
	case "private":
		return label + " are now members-only."
	case "link":
		if n == 0 {
			return "Those files were already linked to that event."
		}
		return label + " added to that event."
	case "move":
		return label + " moved to that event — any other event links they had were removed."
	case "unlink":
		if n == 0 {
			return "Those files weren't linked to an event."
		}
		return "Removed " + strconv.Itoa(n) + " event link(s)."
	}
	return ""
}
