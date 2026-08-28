package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/storage"
	"github.com/47-yonkers/scout-site/internal/thumbnail"
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
// under csrf.maxRequestBodySize (500 MB, the TOTAL size of one submission —
// see that constant's own comment), so the error a leader sees for one
// oversized file names the actual limit that tripped rather than a generic
// "request too large."
const maxUploadFileSize = 50 << 20 // 50 MB

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

	events, err := calendar.ListAllForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing events for file library: %v", err)
	}

	decorate := func(f files.File) fileRow {
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
		return row
	}

	// Every file is grouped by event, accordion-style, whether or not a
	// leader has narrowed things down with the filter below — a flat,
	// unpaginated list of every file in the unit got hard to use once
	// there were more than a handful, the same problem the photo pickers
	// solved with eventAccordionPicker (see _image-picker.html). Checking
	// specific events in the filter just narrows which groups show, via
	// ListForUnitGroupedByEvents instead of the auto-grouped
	// ListForUnitGroupedByEventAuto — files with no event link have
	// nowhere to go in that filtered view, so there's no "ungrouped"
	// group there the way there is by default.
	selectedEventIDs := r.URL.Query()["event_id"]
	var groups []eventFileGroupView
	if len(selectedEventIDs) > 0 {
		fileGroups, err := files.ListForUnitGroupedByEvents(r.Context(), h.Pool, unit.ID, selectedEventIDs)
		if err != nil {
			log.Printf("web: listing files grouped by event: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		groups = make([]eventFileGroupView, 0, len(fileGroups))
		for _, g := range fileGroups {
			groupRows := make([]fileRow, 0, len(g.Files))
			for _, f := range g.Files {
				groupRows = append(groupRows, decorate(f))
			}
			groups = append(groups, eventFileGroupView{EventID: g.EventID, EventTitle: g.EventTitle, Files: groupRows})
		}
	} else {
		fileGroups, ungrouped, err := files.ListForUnitGroupedByEventAuto(r.Context(), h.Pool, unit.ID)
		if err != nil {
			log.Printf("web: listing files grouped by event: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		groups = make([]eventFileGroupView, 0, len(fileGroups)+1)
		for _, g := range fileGroups {
			groupRows := make([]fileRow, 0, len(g.Files))
			for _, f := range g.Files {
				groupRows = append(groupRows, decorate(f))
			}
			groups = append(groups, eventFileGroupView{EventID: g.EventID, EventTitle: g.EventTitle, Files: groupRows})
		}
		if len(ungrouped) > 0 {
			ungroupedRows := make([]fileRow, 0, len(ungrouped))
			for _, f := range ungrouped {
				ungroupedRows = append(ungroupedRows, decorate(f))
			}
			groups = append(groups, eventFileGroupView{EventTitle: "Not linked to an event", Files: ungroupedRows})
		}
	}

	selectedEventSet := make(map[string]bool, len(selectedEventIDs))
	for _, id := range selectedEventIDs {
		selectedEventSet[id] = true
	}

	data := struct {
		baseData
		EventGroups       []eventFileGroupView
		GroupedByEvent    bool
		Events            []calendar.Event
		SelectedEventIDs  map[string]bool
		CanManage         bool
		StorageConfigured bool
	}{
		baseData:          h.base(r, "Files"),
		EventGroups:       groups,
		GroupedByEvent:    len(selectedEventIDs) > 0,
		Events:            events,
		SelectedEventIDs:  selectedEventSet,
		CanManage:         canManage,
		StorageConfigured: h.Storage != nil,
	}
	// Set by FileUpload's redirect when a batch upload had to skip one or
	// more files over the per-file size cap (see maxUploadFileSize) — the
	// rest of the batch still succeeded, so this is a warning, not an error
	// page.
	if skipped := r.URL.Query()["skipped"]; len(skipped) > 0 {
		data.Flash = strconv.Itoa(len(skipped)) + " file(s) were too large (50 MB max each) and were skipped: " + strings.Join(skipped, ", ")
	}
	h.render(w, h.fileLibrary, data)
}

// eventFileGroupView is one event's files, decorated for the template —
// see files.EventFileGroup.
type eventFileGroupView struct {
	EventID    string
	EventTitle string
	Files      []fileRow
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

	// A given name is shared by the whole batch. Uploading just one file
	// gets that name exactly; uploading several gets it with " 1", " 2", ...
	// appended (in upload order) so a leader dropping in a dozen campout
	// photos at once gets "Campout 2026 1", "Campout 2026 2", etc. instead
	// of one name silently overwriting the others or being dropped
	// entirely. Left blank, every file just keeps its own filename as
	// before — this only kicks in when a name was actually given.
	baseName := strings.TrimSpace(r.FormValue("display_name"))

	var created []files.File
	var skipped []string
	nextNumber := 1
	for _, fh := range uploaded {
		// Skip (rather than abort the whole batch on) one oversized file —
		// a leader uploading a whole library of photos at once shouldn't
		// lose every other valid file in the batch, including ones already
		// written to storage earlier in this same loop, just because one
		// file was too big. Reported back via the redirect's "skipped"
		// query params (see FileLibrary), not a hard failure.
		if fh.Size > maxUploadFileSize {
			skipped = append(skipped, fh.Filename)
			continue
		}
		src, err := fh.Open()
		if err != nil {
			log.Printf("web: opening uploaded file: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// Read fully into memory (capped at maxUploadFileSize, so bounded)
		// rather than streaming straight from src to storage — an image
		// needs its bytes a second time right below, to generate its
		// thumbnail eagerly, and re-reading from storage right after
		// writing to it would be a wasted round trip for no reason.
		data, err := io.ReadAll(src)
		src.Close()
		if err != nil {
			log.Printf("web: reading uploaded file: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Derived from the bytes, not from what the uploader claimed — see
		// sniffContentType (file_serving.go) for why the multipart part
		// header can't be trusted here.
		contentType := sniffContentType(data, fh.Header.Get("Content-Type"))
		key := files.NewStorageKey(unit.ID, fh.Filename)
		if err := h.Storage.Put(r.Context(), key, bytes.NewReader(data), int64(len(data)), contentType); err != nil {
			log.Printf("web: uploading file to storage: %v", err)
			http.Error(w, "internal error saving the file", http.StatusInternalServerError)
			return
		}

		// Generated now, while the leader is already waiting on the
		// upload to finish, rather than the first time anyone views this
		// photo's thumbnail — see FileThumbnail's doc comment for why
		// that used to mean a visitor's page load could trigger a
		// real-time image resize. Not fatal on its own: a failure here
		// just leaves FileThumbnail's on-demand fallback to cover it
		// later, the same as it does for every photo uploaded before
		// this existed.
		if strings.HasPrefix(contentType, "image/") {
			if thumb, err := thumbnail.Generate(data); err != nil {
				log.Printf("web: generating thumbnail for uploaded file %q: %v", fh.Filename, err)
			} else if err := h.Storage.Put(r.Context(), key+thumbStorageSuffix, bytes.NewReader(thumb), int64(len(thumb)), "image/jpeg"); err != nil {
				log.Printf("web: caching thumbnail for uploaded file %q: %v", fh.Filename, err)
			}
		}

		displayName := baseName
		if baseName != "" && len(uploaded) > 1 {
			displayName = baseName + " " + strconv.Itoa(nextNumber)
		}
		nextNumber++

		f, err := files.Create(r.Context(), h.Pool, files.File{
			UnitID:      unit.ID,
			Filename:    fh.Filename,
			DisplayName: displayName,
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
	if len(skipped) > 0 {
		q := url.Values{}
		for _, name := range skipped {
			q.Add("skipped", name)
		}
		redirectTo += "?" + q.Encode()
	}
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

// requiresLoginToDownload is the access check behind FileDownload, pulled
// out as a pure function so it's unit-testable without a real storage
// backend — the only file a logged-out visitor may ever fetch is one a
// leader has explicitly marked public (see migration 0016).
func requiresLoginToDownload(f files.File, loggedIn bool) bool {
	return !f.Public && !loggedIn
}

func (h *Handlers) FileDownload(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
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

	// Every file is members-only EXCEPT one a leader has explicitly marked
	// public (see migration 0016) — that's how a homepage image slot (see
	// content-admin.html's "choose from library" picker) can point at a
	// library photo and still render for a logged-out visitor.
	_, loggedIn := auth.UserFromContext(r.Context())
	if requiresLoginToDownload(f, loggedIn) {
		http.Redirect(w, r, "/login?next=/files", http.StatusSeeOther)
		return
	}

	obj, err := h.Storage.Get(r.Context(), f.StorageKey)
	if err != nil {
		log.Printf("web: fetching file from storage: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer obj.Close()

	writeUserFileHeaders(w, f.ContentType, f.Filename)
	if _, err := io.Copy(w, obj); err != nil {
		log.Printf("web: streaming file download: %v", err)
	}
}

// thumbStorageSuffix is appended to a file's own storage key to derive
// the key its generated thumbnail is cached under (see FileThumbnail) —
// derived rather than stored in its own DB column so every existing
// file, uploaded before this feature existed, gets a thumbnail the
// first time one is requested with no backfill step needed.
//
// The ".v2" marks the fix for thumbnails generated before
// thumbnail.Generate corrected for EXIF orientation — those came out
// sideways for any photo taken with the phone held rotated, since only
// the pixel grid was resized, not the rotation a phone camera records
// separately. Bumping this suffix makes every already-cached (sideways)
// thumbnail simply orphaned rather than served: the next request for
// it finds nothing at the new key and regenerates correctly. The old
// objects are never explicitly cleaned up — same tradeoff as the
// orphaned original in Handlers.FileDelete below.
const thumbStorageSuffix = ".thumb.v2.jpg"

// FileThumbnail serves a small, resized JPEG preview of an image file —
// generated once on first request and cached back into storage under a
// derived key so replaying the same photo (a homepage carousel cycling
// through it again, another visitor) doesn't pay the resize cost twice.
// Falls back to streaming the original bytes unchanged for anything
// thumbnail.Generate can't decode (a non-image file that ended up
// pointed at this URL, or an image format it doesn't handle) — a
// full-size fallback beats a broken image.
//
// Same access check as FileDownload (public flag / login) since this is
// the same underlying file's content, just resized.
func (h *Handlers) FileThumbnail(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	if h.Storage == nil {
		http.Error(w, storageUnavailableMsg, http.StatusServiceUnavailable)
		return
	}

	f, found, err := files.Get(r.Context(), h.Pool, r.PathValue("id"), unit.ID)
	if err != nil {
		log.Printf("web: loading file for thumbnail: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	_, loggedIn := auth.UserFromContext(r.Context())
	if requiresLoginToDownload(f, loggedIn) {
		http.Redirect(w, r, "/login?next=/files", http.StatusSeeOther)
		return
	}

	thumbKey := f.StorageKey + thumbStorageSuffix
	if cached, err := h.Storage.Get(r.Context(), thumbKey); err == nil {
		defer cached.Close()
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "private, max-age=604800")
		if _, err := io.Copy(w, cached); err != nil {
			log.Printf("web: streaming cached thumbnail: %v", err)
		}
		return
	}

	// A cache miss here means this photo predates eager generation at
	// upload time (see FileUpload) — an older upload, or one from before
	// this feature existed at all. Falls back to serving the original
	// bytes for anything thumbnail.Generate can't decode, so the request
	// still returns something usable instead of a broken image.
	thumb, src, err := fetchAndCacheThumbnail(r.Context(), h.Storage, f.StorageKey)
	if err != nil {
		// ErrTooLarge joins ErrNotAnImage here: both mean "no thumbnail
		// for this one", and serving the original bytes is the right
		// fallback for either. It is NOT a reason to fail the request —
		// the original still downloads fine, under the safe headers
		// writeUserFileHeaders sets.
		if errors.Is(err, thumbnail.ErrNotAnImage) || errors.Is(err, thumbnail.ErrTooLarge) {
			// Falls back to the original bytes, so this is a second path
			// serving user-uploaded content and needs the same headers the
			// download route gets — not just a bare Content-Type.
			writeUserFileHeaders(w, f.ContentType, f.Filename)
			w.Write(src)
			return
		}
		log.Printf("web: generating thumbnail on demand for %s: %v", f.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=604800")
	w.Write(thumb)
}

// fetchAndCacheThumbnail fetches storageKey's bytes from store,
// generates its thumbnail, and caches the result under its derived key
// (thumbStorageSuffix) — the shared "no local copy of the bytes already
// in hand" path used by both FileThumbnail's on-demand fallback and
// BackfillThumbnails' explicit one-time catch-up run (FileUpload's own
// eager generation skips this entirely, since it already has the
// uploaded bytes in memory and would otherwise be paying for a pointless
// round trip back to storage for what it just wrote).
//
// On a thumbnail.ErrNotAnImage decode failure, src is still returned
// (the original bytes, already fetched) so a caller like FileThumbnail
// can fall back to serving them without a second fetch; on any other
// error src is nil, since fetching or caching failed outright.
func fetchAndCacheThumbnail(ctx context.Context, store *storage.Store, storageKey string) (thumb, src []byte, err error) {
	orig, err := store.Get(ctx, storageKey)
	if err != nil {
		return nil, nil, err
	}
	src, err = io.ReadAll(orig)
	orig.Close()
	if err != nil {
		return nil, nil, err
	}

	thumb, err = thumbnail.Generate(src)
	if err != nil {
		return nil, src, err
	}

	if err := store.Put(ctx, storageKey+thumbStorageSuffix, bytes.NewReader(thumb), int64(len(thumb)), "image/jpeg"); err != nil {
		return nil, src, err
	}
	return thumb, src, nil
}

// BackfillResult tallies what BackfillThumbnails did across every image
// file it looked at.
type BackfillResult struct {
	Generated int // no cached thumbnail existed yet — generated and cached one now
	Skipped   int // already had a cached thumbnail — left untouched
	Failed    int // couldn't fetch, decode, or cache — logged and moved on
}

// BackfillThumbnails walks every image file across every unit and
// generates its cached thumbnail if one doesn't already exist — the
// one-time, explicitly-run counterpart to FileThumbnail's on-demand
// fallback, for an operator who wants every already-uploaded photo's
// thumbnail ready ahead of time rather than leaving each one to be
// generated whenever it's first viewed. Run via
// `server -backfill-thumbnails` (see cmd/server/main.go); safe to
// re-run, since an already-cached thumbnail is left untouched. A single
// file's failure (a missing/corrupt original, a storage hiccup) is
// logged and counted, not fatal to the rest of the run.
func BackfillThumbnails(ctx context.Context, pool *pgxpool.Pool, store *storage.Store) (BackfillResult, error) {
	var result BackfillResult

	all, err := files.ListAllImageFiles(ctx, pool)
	if err != nil {
		return result, err
	}

	for _, f := range all {
		if cached, err := store.Get(ctx, f.StorageKey+thumbStorageSuffix); err == nil {
			cached.Close()
			result.Skipped++
			continue
		}
		if _, _, err := fetchAndCacheThumbnail(ctx, store, f.StorageKey); err != nil {
			log.Printf("web: backfill: %s (%s): %v", f.ID, f.Filename, err)
			result.Failed++
			continue
		}
		result.Generated++
	}
	return result, nil
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
		// Best-effort: only exists if a thumbnail was ever generated for
		// this file (see FileThumbnail) — Delete on a missing key is a
		// no-op, not an error (see storage.Store.Delete).
		if err := h.Storage.Delete(r.Context(), f.StorageKey+thumbStorageSuffix); err != nil {
			log.Printf("web: deleting cached thumbnail: %v", err)
		}
	}

	http.Redirect(w, r, "/files", http.StatusSeeOther)
}

// FileSetPublic toggles whether a file may be served without login (see
// migration 0016) — the same CanEditUnitContent gate as delete/link, since
// making a file public is a content-level decision, not just a viewing one.
func (h *Handlers) FileSetPublic(w http.ResponseWriter, r *http.Request) {
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

	if err := files.SetPublic(r.Context(), h.Pool, fileID, unit.ID, !f.Public); err != nil {
		log.Printf("web: setting file public flag: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/files", http.StatusSeeOther)
}

// FileSetDisplayName sets a friendlier label for a file — see
// files.File.DisplayLabel — shown in place of the raw uploaded filename
// wherever the file is listed or picked. Same CanEditUnitContent gate as
// the file library's other management actions.
func (h *Handlers) FileSetDisplayName(w http.ResponseWriter, r *http.Request) {
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

	if err := files.SetDisplayName(r.Context(), h.Pool, fileID, unit.ID, strings.TrimSpace(r.FormValue("display_name"))); err != nil {
		log.Printf("web: setting file display name: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/files", http.StatusSeeOther)
}

// FileSetCategory reclassifies a file between "General document" and
// "Event photo" after the fact — chosen once at upload time, but a leader
// sometimes only realizes it was categorized wrong later. Same
// CanEditUnitContent gate as the file library's other management actions.
func (h *Handlers) FileSetCategory(w http.ResponseWriter, r *http.Request) {
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

	category := files.CategoryGeneral
	if r.FormValue("category") == files.CategoryEventPhoto {
		category = files.CategoryEventPhoto
	}

	if err := files.SetCategory(r.Context(), h.Pool, fileID, unit.ID, category); err != nil {
		log.Printf("web: setting file category: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
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
