package web

import (
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/units"
)

// This file holds the file library and event-photo handlers — general
// document storage plus per-event photo/document attachments (see
// migration 0012 and internal/files/internal/storage). Viewing is open to
// any logged-in member, matching how the roster and calendar already work;
// uploading, deleting, and managing which events a file links to requires
// CanEditUnitContent, the same gate /admin/home and the news/gallery admin
// pages already use.

// maxUploadFileSize caps a single uploaded file's size. Kept comfortably
// under csrf.maxRequestBodySize (25 MB), which bounds the whole request —
// this bounds just the one file field, so the error a leader sees names the
// actual limit that tripped rather than a generic "request too large."
const maxUploadFileSize = 20 << 20 // 20 MB

// fileRow is a files.File decorated with what the template needs to render
// it and its "link to events" checkboxes.
type fileRow struct {
	files.File
	SizeDisplay    string
	LinkedEventIDs map[string]bool
}

func displaySize(n int64) string {
	const kb = 1024
	const mb = kb * 1024
	switch {
	case n >= mb:
		return strconv.FormatFloat(float64(n)/mb, 'f', 1, 64) + " MB"
	case n >= kb:
		return strconv.FormatFloat(float64(n)/kb, 'f', 1, 64) + " KB"
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}

func (h *Handlers) FileLibrary(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next=/files", http.StatusSeeOther)
		return
	}

	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil {
		log.Printf("web: loading capabilities: %v", err)
	}
	canManage := units.CanEditUnitContent(caps)

	all, err := files.ListForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing files: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	events, err := calendar.ListAllForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing events for file library: %v", err)
	}

	rows := make([]fileRow, 0, len(all))
	for _, f := range all {
		row := fileRow{File: f, SizeDisplay: displaySize(f.SizeBytes)}
		if canManage {
			linkedIDs, err := files.EventIDsForFile(r.Context(), h.Pool, f.ID)
			if err != nil {
				log.Printf("web: loading linked events for file %s: %v", f.ID, err)
			}
			row.LinkedEventIDs = make(map[string]bool, len(linkedIDs))
			for _, id := range linkedIDs {
				row.LinkedEventIDs[id] = true
			}
		}
		rows = append(rows, row)
	}

	data := struct {
		baseData
		Files             []fileRow
		Events            []calendar.Event
		CanManage         bool
		StorageConfigured bool
	}{
		baseData:          h.base(r, "Files"),
		Files:             rows,
		Events:            events,
		CanManage:         canManage,
		StorageConfigured: h.Storage != nil,
	}
	h.render(w, h.fileLibrary, data)
}

// storageUnavailableMsg is shown instead of doing any actual storage I/O
// when h.Storage is nil (no S3_ENDPOINT configured) — the rest of the site
// keeps working (see internal/storage.New's doc comment); only these
// upload/download/delete actions need to fail clearly instead of crashing.
const storageUnavailableMsg = "File storage isn't configured for this site yet — an admin needs to set S3_ENDPOINT/S3_ACCESS_KEY/S3_SECRET_KEY (see DEPLOY.md)."

func (h *Handlers) FileUpload(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanEditUnitContent(caps) {
		http.Error(w, "you don't have permission to upload files", http.StatusForbidden)
		return
	}
	if h.Storage == nil {
		http.Error(w, storageUnavailableMsg, http.StatusServiceUnavailable)
		return
	}

	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member — has your family been added to the roster yet?", http.StatusBadRequest)
		return
	}

	category := files.CategoryGeneral
	if r.FormValue("category") == files.CategoryEventPhoto {
		category = files.CategoryEventPhoto
	}

	if r.MultipartForm == nil {
		http.Error(w, "choose a file to upload", http.StatusBadRequest)
		return
	}
	uploaded := r.MultipartForm.File["file"]
	if len(uploaded) == 0 {
		http.Error(w, "choose a file to upload", http.StatusBadRequest)
		return
	}

	var created []files.File
	for _, fh := range uploaded {
		if fh.Size > maxUploadFileSize {
			http.Error(w, fh.Filename+" is too large (20 MB max per file)", http.StatusRequestEntityTooLarge)
			return
		}
		src, err := fh.Open()
		if err != nil {
			log.Printf("web: opening uploaded file: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		contentType := fh.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		key := files.NewStorageKey(unit.ID, fh.Filename)
		err = h.Storage.Put(r.Context(), key, src, fh.Size, contentType)
		src.Close()
		if err != nil {
			log.Printf("web: uploading file to storage: %v", err)
			http.Error(w, "internal error saving the file", http.StatusInternalServerError)
			return
		}

		f, err := files.Create(r.Context(), h.Pool, files.File{
			UnitID:      unit.ID,
			Filename:    fh.Filename,
			ContentType: contentType,
			SizeBytes:   fh.Size,
			StorageKey:  key,
			Category:    category,
			UploadedBy:  &actor.ID,
		})
		if err != nil {
			log.Printf("web: recording uploaded file: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		created = append(created, f)
	}

	// A photo/document upload can optionally be linked to one or more
	// events in the same step (the calendar page's "attach a file" flow
	// posts here with event_ids pre-filled), saving a second trip through
	// the file library's own link-management form.
	if eventIDs := r.Form["event_ids"]; len(eventIDs) > 0 {
		for _, f := range created {
			if err := files.SetEventLinks(r.Context(), h.Pool, f.ID, unit.ID, eventIDs); err != nil {
				log.Printf("web: linking uploaded file %s to events: %v", f.ID, err)
			}
		}
	}

	redirectTo := "/files"
	if back := r.FormValue("redirect_to"); back != "" && strings.HasPrefix(back, "/") && !strings.HasPrefix(back, "//") {
		redirectTo = back
	}
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

func (h *Handlers) FileDownload(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	if _, loggedIn := auth.UserFromContext(r.Context()); !loggedIn {
		http.Redirect(w, r, "/login?next=/files", http.StatusSeeOther)
		return
	}
	if h.Storage == nil {
		http.Error(w, storageUnavailableMsg, http.StatusServiceUnavailable)
		return
	}

	f, found, err := files.Get(r.Context(), h.Pool, r.PathValue("id"), unit.ID)
	if err != nil {
		log.Printf("web: loading file: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	obj, err := h.Storage.Get(r.Context(), f.StorageKey)
	if err != nil {
		log.Printf("web: fetching file from storage: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer obj.Close()

	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Disposition", `inline; filename="`+strings.ReplaceAll(f.Filename, `"`, "")+`"`)
	if _, err := io.Copy(w, obj); err != nil {
		log.Printf("web: streaming file download: %v", err)
	}
}

func (h *Handlers) FileDelete(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanEditUnitContent(caps) {
		http.Error(w, "you don't have permission to delete files", http.StatusForbidden)
		return
	}

	fileID := r.PathValue("id")
	f, found, err := files.Get(r.Context(), h.Pool, fileID, unit.ID)
	if err != nil {
		log.Printf("web: loading file: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	if err := files.Delete(r.Context(), h.Pool, fileID, unit.ID); err != nil {
		log.Printf("web: deleting file row: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if h.Storage != nil {
		if err := h.Storage.Delete(r.Context(), f.StorageKey); err != nil {
			// The metadata row is already gone — the file is effectively
			// deleted from the site's point of view — so this only leaves
			// an orphaned object in the bucket, logged for later cleanup
			// rather than surfaced as a failure the leader can't do
			// anything about.
			log.Printf("web: deleting file object from storage: %v", err)
		}
	}

	http.Redirect(w, r, "/files", http.StatusSeeOther)
}

func (h *Handlers) FileSetEventLinks(w http.ResponseWriter, r *http.Request) {
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

	fileID := r.PathValue("id")
	if _, found, err := files.Get(r.Context(), h.Pool, fileID, unit.ID); err != nil {
		log.Printf("web: loading file: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !found {
		http.NotFound(w, r)
		return
	}

	if err := files.SetEventLinks(r.Context(), h.Pool, fileID, unit.ID, r.Form["event_ids"]); err != nil {
		log.Printf("web: setting file event links: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/files", http.StatusSeeOther)
}
