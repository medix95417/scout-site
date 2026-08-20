package web

import (
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/resources"
	"github.com/47-yonkers/scout-site/internal/units"
)

// This file holds the resources-page handlers — a curated list of
// documents and links, each markable public or members-only by an admin/
// cubmaster/pack master (units.CanEditUnitContent, the same gate the
// homepage and news/gallery admin surfaces already use). One route serves
// both the public and members-only view, plus inline admin controls for
// those with permission — the same "one page, admin controls appear inline"
// pattern internal/web/files.go already uses for the file library, rather
// than a separate /admin/resources page.

// resourceRow is a resources.Resource decorated with what the template
// needs to render it.
type resourceRow struct {
	resources.Resource
	SizeDisplay string
}

func (h *Handlers) ResourcesList(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())

	var list []resources.Resource
	var err error
	if loggedIn {
		list, err = resources.ListForUnit(r.Context(), h.Pool, unit.ID)
	} else {
		list, err = resources.ListPublicForUnit(r.Context(), h.Pool, unit.ID)
	}
	if err != nil {
		log.Printf("web: listing resources: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rows := make([]resourceRow, 0, len(list))
	for _, res := range list {
		row := resourceRow{Resource: res}
		if res.FileID != nil {
			row.SizeDisplay = displaySize(res.FileSizeBytes)
		}
		rows = append(rows, row)
	}

	var canManage bool
	var libraryFiles []files.File
	if loggedIn {
		caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
		if err != nil {
			log.Printf("web: loading capabilities: %v", err)
		}
		canManage = units.CanEditUnitContent(caps)
		if canManage {
			libraryFiles, err = files.ListForUnit(r.Context(), h.Pool, unit.ID)
			if err != nil {
				log.Printf("web: listing files for resource picker: %v", err)
			}
		}
	}

	data := struct {
		baseData
		Resources    []resourceRow
		CanManage    bool
		LibraryFiles []files.File
	}{
		baseData:     h.base(r, "Resources"),
		Resources:    rows,
		CanManage:    canManage,
		LibraryFiles: libraryFiles,
	}
	h.render(w, h.resourcesList, data)
}

func (h *Handlers) ResourceCreate(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanEditUnitContent(caps) {
		http.Error(w, "you don't have permission to manage resources", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "a title is required", http.StatusBadRequest)
		return
	}
	description := r.FormValue("description")
	isPublic := r.FormValue("is_public") == "1"

	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member — has your family been added to the roster yet?", http.StatusBadRequest)
		return
	}

	switch r.FormValue("kind") {
	case "link":
		url := strings.TrimSpace(r.FormValue("url"))
		if url == "" {
			http.Error(w, "a URL is required for a link resource", http.StatusBadRequest)
			return
		}
		_, err = resources.CreateLink(r.Context(), h.Pool, resources.CreateLinkInput{
			UnitID: unit.ID, Title: title, Description: description, URL: url, IsPublic: isPublic, CreatedBy: actor.ID,
		})
	default:
		fileID := r.FormValue("file_id")
		if fileID == "" {
			http.Error(w, "choose a file from the library", http.StatusBadRequest)
			return
		}
		if _, found, ferr := files.Get(r.Context(), h.Pool, fileID, unit.ID); ferr != nil {
			log.Printf("web: loading file for resource: %v", ferr)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		} else if !found {
			http.Error(w, "that file doesn't exist in this unit's library", http.StatusBadRequest)
			return
		}
		_, err = resources.CreateFile(r.Context(), h.Pool, resources.CreateFileInput{
			UnitID: unit.ID, Title: title, Description: description, FileID: fileID, IsPublic: isPublic, CreatedBy: actor.ID,
		})
	}
	if err != nil {
		log.Printf("web: creating resource: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/resources", http.StatusSeeOther)
}

// ResourceSetPublic toggles whether a resource is visible to logged-out
// visitors — same CanEditUnitContent gate as create/delete, since this is a
// content-level decision.
func (h *Handlers) ResourceSetPublic(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanEditUnitContent(caps) {
		http.Error(w, "you don't have permission to manage resources", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	res, found, err := resources.Get(r.Context(), h.Pool, id, unit.ID)
	if err != nil {
		log.Printf("web: loading resource: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	if err := resources.SetPublic(r.Context(), h.Pool, id, unit.ID, !res.IsPublic); err != nil {
		log.Printf("web: setting resource public flag: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/resources", http.StatusSeeOther)
}

func (h *Handlers) ResourceDelete(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanEditUnitContent(caps) {
		http.Error(w, "you don't have permission to manage resources", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	if _, found, err := resources.Get(r.Context(), h.Pool, id, unit.ID); err != nil {
		log.Printf("web: loading resource: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !found {
		http.NotFound(w, r)
		return
	}

	if err := resources.Delete(r.Context(), h.Pool, id, unit.ID); err != nil {
		log.Printf("web: deleting resource: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/resources", http.StatusSeeOther)
}

// ResourceDownload streams a document resource's underlying file. Deliberately
// separate from /files/{id}/download: that route gates on the file's own
// is_public flag, but a resource has its own independent is_public flag (see
// migration 0019's doc comment), so a members-only-by-default library file
// curated here as a public resource needs its own access check.
func (h *Handlers) ResourceDownload(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	if h.Storage == nil {
		http.Error(w, storageUnavailableMsg, http.StatusServiceUnavailable)
		return
	}

	res, found, err := resources.Get(r.Context(), h.Pool, r.PathValue("id"), unit.ID)
	if err != nil {
		log.Printf("web: loading resource: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found || res.FileID == nil {
		http.NotFound(w, r)
		return
	}

	_, loggedIn := auth.UserFromContext(r.Context())
	if !res.IsPublic && !loggedIn {
		http.Redirect(w, r, "/login?next=/resources", http.StatusSeeOther)
		return
	}

	f, found, err := files.Get(r.Context(), h.Pool, *res.FileID, unit.ID)
	if err != nil {
		log.Printf("web: loading file for resource download: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	obj, err := h.Storage.Get(r.Context(), f.StorageKey)
	if err != nil {
		log.Printf("web: fetching resource file from storage: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer obj.Close()

	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Disposition", `inline; filename="`+strings.ReplaceAll(f.Filename, `"`, "")+`"`)
	if _, err := io.Copy(w, obj); err != nil {
		log.Printf("web: streaming resource download: %v", err)
	}
}
