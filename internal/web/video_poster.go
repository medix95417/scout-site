package web

// A video's poster: the still picture shown where it would otherwise be a
// black box until someone presses play.
//
// The picture is made by the leader's browser, not by this server. A page
// that has just uploaded a video (or the Files page, for videos uploaded
// before this existed) draws a frame onto a canvas and POSTs it here. The
// server only checks that it is really a picture, shrinks it, and files it —
// see files.PosterKey for where, and internal/files/poster.go for why a
// browser rather than ffmpeg.
//
// GET serves it under exactly the rule that serves the video itself, since a
// frame of a members-only video is the video's content.

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/thumbnail"
	"github.com/47-yonkers/scout-site/internal/units"
)

// maxPosterUploadBytes bounds what a browser may send as a poster. A frame is
// drawn at 640 pixels on its longest side and comes to a few tens of kilobytes;
// this is generous enough for any honest page and small enough that the
// endpoint is not a way to push a large file through the image decoder.
const maxPosterUploadBytes = 4 << 20

// posterURL is thumbURL's counterpart for a video: the address of its poster.
// Empty for anything that isn't one of this app's own file links — a video a
// leader pasted in from elsewhere has no poster of ours, and an empty result
// lets a template leave the attribute off.
func posterURL(url string) string {
	if p := resizedURL(url, "poster"); p != url {
		return p
	}
	return ""
}

// FilePoster serves a video's poster, or 404 if it has none yet. Nothing is
// generated on request: a poster needs a video decoder, which only a browser
// here has, so "not yet" is an answer, and the page shows the dark box it
// always did.
func (h *Handlers) FilePoster(w http.ResponseWriter, r *http.Request) {
	f, ok := h.servableFile(w, r)
	if !ok {
		return
	}
	unit, _ := units.UnitFromContext(r.Context())

	has, err := files.HasPoster(r.Context(), h.Pool, f.ID, unit.ID)
	if err != nil {
		log.Printf("web: checking for a poster: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !has {
		http.NotFound(w, r)
		return
	}

	obj, err := h.Storage.Open(r.Context(), files.PosterKey(unit.ID, f.ID))
	if err != nil {
		// The row says there is one and the bucket says there isn't — the
		// object was lost or never landed. A picture that is missing is not
		// worth an error page, but is worth a line to find out why.
		log.Printf("web: poster recorded for %s but not in storage: %v", f.ID, err)
		http.NotFound(w, r)
		return
	}
	defer obj.Close()

	writeUserFileHeaders(w, "image/jpeg", "poster.jpg")
	serveStoredContent(w, r, obj, obj.ModTime, obj.ETag)
}

// FilePosterUpload takes the poster a browser made for one of the unit's
// videos. Whoever may edit the site's content may make one; it is the same
// gate as uploading the video was.
//
// Not written to the Activity Log, for the reason a thumbnail isn't: it is a
// picture derived from a file that is already recorded, made automatically as
// part of putting the video on the site, not a decision anyone made.
func (h *Handlers) FilePosterUpload(w http.ResponseWriter, r *http.Request) {
	// A few tens of kilobytes on a good connection, but a phone on one bar can
	// take a minute over it; see extendWriteDeadline.
	extendWriteDeadline(w, uploadWriteWindow)

	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Error(w, "sign in again", http.StatusUnauthorized)
		return
	}
	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanEditUnitContent(caps) {
		http.Error(w, "you don't have permission to do that", http.StatusForbidden)
		return
	}
	if h.Storage == nil {
		http.Error(w, storageUnavailableMsg, http.StatusServiceUnavailable)
		return
	}

	// Scoped to the unit, so an id from the other unit is simply not found.
	f, found, err := files.Get(r.Context(), h.Pool, r.PathValue("id"), unit.ID)
	if err != nil {
		log.Printf("web: loading file for a poster: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	// By the type recorded from the bytes at upload, not by anything the
	// client says now: only a video has a poster.
	if !strings.HasPrefix(f.ContentType, "video/") {
		http.Error(w, "only a video has a poster", http.StatusBadRequest)
		return
	}

	// The CSRF middleware has already read the multipart body — the token is
	// a field inside it — so the picture is here.
	if r.MultipartForm == nil || len(r.MultipartForm.File["poster"]) == 0 {
		http.Error(w, "no picture was sent", http.StatusBadRequest)
		return
	}
	fh := r.MultipartForm.File["poster"][0]
	if fh.Size > maxPosterUploadBytes {
		http.Error(w, "that picture is too large", http.StatusRequestEntityTooLarge)
		return
	}
	src, err := fh.Open()
	if err != nil {
		http.Error(w, "couldn't read that picture", http.StatusBadRequest)
		return
	}
	defer src.Close()
	raw, err := io.ReadAll(io.LimitReader(src, maxPosterUploadBytes+1))
	if err != nil || len(raw) > maxPosterUploadBytes {
		http.Error(w, "couldn't read that picture", http.StatusBadRequest)
		return
	}

	// Decoded and written out again rather than stored as sent: whatever the
	// browser says it is, what ends up in the bucket and is served from this
	// origin is a JPEG this server made, at the size a poster is shown.
	still, err := thumbnail.GenerateAt(raw, thumbnail.MaxDimension)
	if err != nil {
		if errors.Is(err, thumbnail.ErrNotAnImage) || errors.Is(err, thumbnail.ErrTooLarge) {
			http.Error(w, "that isn't a picture this site can use", http.StatusBadRequest)
			return
		}
		log.Printf("web: preparing a poster for %s: %v", f.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.Storage.Put(r.Context(), files.PosterKey(unit.ID, f.ID), bytes.NewReader(still), int64(len(still)), "image/jpeg"); err != nil {
		log.Printf("web: storing a poster for %s: %v", f.ID, err)
		http.Error(w, "couldn't store that picture", http.StatusInternalServerError)
		return
	}
	if err := files.SetPoster(r.Context(), h.Pool, f.ID, unit.ID, true); err != nil {
		log.Printf("web: recording a poster for %s: %v", f.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
