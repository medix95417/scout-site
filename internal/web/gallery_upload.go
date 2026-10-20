package web

// Uploading photos and videos straight from the album editor.
//
// Adding a photo to an album used to be two trips: upload it on the Files
// page, then come back and pick it from the library. This is the one-step
// version, on the page where the album is being built.
//
// BATCHING IS THE POINT. A leader dropping in a campout's worth of photos
// does not want one request carrying all of them: one dropped connection
// loses the lot, and the page shows nothing until the very end. The page
// (see _gallery-upload.html) sends them one batch at a time — up to
// galleryUploadMaxPhotos photos, or a single video, never both together
// and never two requests at once — and adds each batch to the album as it
// lands. A failure costs one batch, and says which files it was.
//
// The limits below are the SERVER's ceiling on one request, and the page
// reads the same numbers from the markup rather than repeating them, so
// the two cannot drift. The ceiling is looser than the page's plan (it
// allows a video beside the photos) so a client that misjudges a file's
// type by its extension still gets its batch through rather than losing it
// to a rule the person never saw.
//
// WHERE THE FILES GO. Into the same library the Files page uses, filed as
// event photos/video, so nothing about them is special afterwards: they
// can be linked to an event, renamed or deleted there like any other file.
//
// WHETHER THEY ARE PUBLIC follows the album's visibility as it stands when
// the batch is sent. A public album's photos have to be public or a signed-
// out visitor sees an empty album (see filterViewableGalleryPhotos); a
// members-only album's photos must NOT be, or anyone holding a link could
// open pictures of other people's children. Neither is a choice to make
// per photo, so the page sends the radio's value and says what it did.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/thumbnail"
	"github.com/47-yonkers/scout-site/internal/units"
)

const (
	// galleryUploadMaxPhotos and galleryUploadMaxVideos bound one request.
	// Five photos is a few tens of megabytes on a phone; a video is the
	// thing most likely to be large and slow, so it travels alone.
	galleryUploadMaxPhotos = 5
	galleryUploadMaxVideos = 1

	// galleryUploadMaxParts is the most files one request may carry, read
	// before anything is opened so a hand-made request cannot ask for
	// hundreds of storage writes.
	galleryUploadMaxParts = galleryUploadMaxPhotos + galleryUploadMaxVideos

	// galleryUploadWriteWindow is how long the handler gets to finish and
	// answer once it starts. See extendWriteDeadline for why this exists.
	galleryUploadWriteWindow = 5 * time.Minute
)

// galleryUploadView is what the album form needs to draw its uploader. A
// type of its own, built by newGalleryUploadView, so the page takes its
// limits from the same constants the handler enforces.
type galleryUploadView struct {
	// Enabled is false when no file storage is configured. The uploader is
	// then replaced by a plain note — the optional-integration rule (see
	// CLAUDE.md): the rest of the editor keeps working.
	Enabled   bool
	MaxPhotos int
	MaxVideos int
	MaxBytes  int64
	MaxMB     int
}

func newGalleryUploadView(storageConfigured bool) galleryUploadView {
	return galleryUploadView{
		Enabled:   storageConfigured,
		MaxPhotos: galleryUploadMaxPhotos,
		MaxVideos: galleryUploadMaxVideos,
		MaxBytes:  maxUploadFileSize,
		MaxMB:     maxUploadFileSize >> 20,
	}
}

// galleryUploadResult is one file's outcome, in the order the files were
// sent. Exactly one of URL or Error is set.
type galleryUploadResult struct {
	Name  string `json:"name"`
	ID    string `json:"id,omitempty"`
	URL   string `json:"url,omitempty"`
	Video bool   `json:"video,omitempty"`
	Error string `json:"error,omitempty"`
	// Retry says whether sending the same file again could work. True for a
	// storage hiccup or a file that arrived in a batch that was too full;
	// false for a file that is the wrong kind or too big, which would fail
	// the same way every time. The page offers a retry only for the first.
	Retry bool `json:"retry,omitempty"`
}

// galleryUploadResponse is the body of a request that was understood. A
// request can succeed overall with some files refused — each says why.
type galleryUploadResponse struct {
	Results []galleryUploadResult `json:"results"`
	// Public says how the files were saved, so the page can tell the leader
	// and notice if the album's visibility changes afterwards.
	Public bool `json:"public"`
}

// errGalleryUploadTooMany is the one whole-request refusal: more files than
// one request may carry. Nothing has been stored when it is returned.
var errGalleryUploadTooMany = fmt.Errorf("too many files at once — send at most %d photos and %d video per upload",
	galleryUploadMaxPhotos, galleryUploadMaxVideos)

// galleryUploader holds the policy and the dependencies of storing ONE
// request's files. The function fields are the only things that touch a
// bucket or a database, which is what lets the rules above them — what
// counts as media, the size and count limits, what an error says — be
// tested for real instead of by inspection (the shape inline_images.go
// uses for the same reason).
type galleryUploader struct {
	unitID  string
	actorID string
	folder  string
	public  bool
	// maxBytes is one file's size cap. A field rather than the constant so
	// a test can exercise the over-limit path without building a 50 MB body.
	maxBytes int64

	put    func(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	create func(ctx context.Context, f files.File) (files.File, error)
	// thumb caches an image's thumbnail, best-effort: FileThumbnail
	// regenerates on demand when this fails, as it does for every file
	// uploaded before thumbnails were cached.
	thumb func(ctx context.Context, key string, data []byte)
}

// run stores a request's files and reports each one's outcome. The error
// is a whole-request refusal only (errGalleryUploadTooMany).
//
// One file failing never stops the others: a leader sending five photos
// should not lose four good ones to one bad one, and the response says
// exactly which was which.
func (g galleryUploader) run(ctx context.Context, parts []*multipart.FileHeader) ([]galleryUploadResult, error) {
	if len(parts) > galleryUploadMaxParts {
		return nil, errGalleryUploadTooMany
	}

	results := make([]galleryUploadResult, 0, len(parts))
	photos, videos := 0, 0
	for _, fh := range parts {
		res := galleryUploadResult{Name: fh.Filename}

		switch {
		case fh.Size == 0:
			res.Error = "That file is empty."
		case fh.Size > g.maxBytes:
			res.Error = fmt.Sprintf("Too large: %s, and the limit is %d MB for one file.", displaySize(fh.Size), g.maxBytes>>20)
		default:
			res = g.one(ctx, fh, &photos, &videos)
		}
		results = append(results, res)
	}
	return results, nil
}

// one classifies, counts and stores a single file.
func (g galleryUploader) one(ctx context.Context, fh *multipart.FileHeader, photos, videos *int) galleryUploadResult {
	res := galleryUploadResult{Name: fh.Filename}

	src, err := fh.Open()
	if err != nil {
		log.Printf("web: opening an uploaded album file: %v", err)
		res.Error = "That file couldn't be read. Try adding it again."
		res.Retry = true
		return res
	}
	defer src.Close()

	// Only the first bytes decide what a file is, so a document or an
	// unsupported format is refused before the rest of it is read.
	head := make([]byte, 512)
	n, err := io.ReadFull(src, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		log.Printf("web: reading an uploaded album file: %v", err)
		res.Error = "That file couldn't be read. Try adding it again."
		res.Retry = true
		return res
	}
	head = head[:n]

	contentType, video, problem := classifyGalleryMedia(head, fh.Header.Get("Content-Type"))
	if problem != "" {
		res.Error = problem
		return res
	}

	// The ceiling on one request. The page never exceeds it; this is what
	// holds for a client that is not the page.
	if video && *videos >= galleryUploadMaxVideos {
		res.Error = fmt.Sprintf("Only %d video goes in one upload — send this one again on its own.", galleryUploadMaxVideos)
		res.Retry = true
		return res
	}
	if !video && *photos >= galleryUploadMaxPhotos {
		res.Error = fmt.Sprintf("Only %d photos go in one upload — send this one in the next batch.", galleryUploadMaxPhotos)
		res.Retry = true
		return res
	}

	if _, err := src.Seek(0, io.SeekStart); err != nil {
		log.Printf("web: rewinding an uploaded album file: %v", err)
		res.Error = "That file couldn't be read. Try adding it again."
		res.Retry = true
		return res
	}

	key := files.NewStorageKey(g.unitID, g.folder, fh.Filename)
	if video {
		// Streamed from the request's own temp file, not read into memory:
		// a video is the big one, and there is nothing to derive from its
		// bytes the way a photo's thumbnail is.
		if err := g.put(ctx, key, src, fh.Size, contentType); err != nil {
			log.Printf("web: storing an album video: %v", err)
			res.Error = "Couldn't save that one. Try adding it again."
			res.Retry = true
			return res
		}
	} else {
		// Read in full because the thumbnail needs the bytes a second time,
		// and re-reading from storage right after writing to it would be a
		// wasted round trip. Bounded by maxBytes, checked above.
		data, err := io.ReadAll(src)
		if err != nil {
			log.Printf("web: reading an uploaded album photo: %v", err)
			res.Error = "That file couldn't be read. Try adding it again."
			res.Retry = true
			return res
		}
		if err := g.put(ctx, key, bytes.NewReader(data), int64(len(data)), contentType); err != nil {
			log.Printf("web: storing an album photo: %v", err)
			res.Error = "Couldn't save that one. Try adding it again."
			res.Retry = true
			return res
		}
		if g.thumb != nil {
			g.thumb(ctx, key, data)
		}
	}

	// The object is stored before its row exists, the order files.Create
	// documents: an object with no row is harmless, a row with no object is
	// a broken tile.
	f, err := g.create(ctx, files.File{
		UnitID:      g.unitID,
		Filename:    fh.Filename,
		ContentType: contentType,
		SizeBytes:   fh.Size,
		StorageKey:  key,
		Category:    files.CategoryEventPhoto,
		UploadedBy:  &g.actorID,
		Public:      g.public,
	})
	if err != nil {
		log.Printf("web: recording an album upload: %v", err)
		res.Error = "Couldn't save that one. Try adding it again."
		res.Retry = true
		return res
	}

	if video {
		*videos++
	} else {
		*photos++
	}
	res.ID = f.ID
	res.URL = "/files/" + f.ID + "/download"
	res.Video = video
	return res
}

// classifyGalleryMedia decides whether an upload is a photo or video an
// album can show, from its first bytes.
//
// It reuses sniffContentType, so the uploader's claim about a file is never
// what decides — see file_serving.go for why. A photo has to be one of the
// formats a browser renders in place (inlineRenderableTypes): anything else
// would be a broken tile in the album. A video needs only a video/ type,
// because a <video> element plays what it can and an album of a few
// unusual containers is still better than refusing them.
//
// problem is a sentence for the leader, empty when the file is fine.
func classifyGalleryMedia(head []byte, declared string) (contentType string, video bool, problem string) {
	contentType = sniffContentType(head, declared)

	// Go's sniffer calls a file an MP4 only if one of its brands starts with
	// "mp4". The container is wider than that — an MP4 whose brands are
	// "isom"/"avc1" carries no "mp4" brand and sniffs as plain octets — and
	// because video/mp4 is itself on the renderable list, the uploader's
	// claim is (rightly) never used to upgrade to it. The result was a real
	// video refused as "not a photo or video".
	//
	// So where the bytes open with an ISO base-media box AND the browser
	// says it is a video, that is accepted. It is no weaker than the sniffer's
	// own check, which also reads a brand out of the same box, and the file
	// is still served with the sandbox policy and nosniff (see
	// writeUserFileHeaders). Anything that sniffed as something else — an
	// AVIF or HEIC photo uses this same container — is left alone.
	if contentType == "application/octet-stream" && isISOBaseMedia(head) {
		if d := normalizeMediaType(declared); strings.HasPrefix(d, "video/") {
			contentType = d
		}
	}

	switch {
	case strings.HasPrefix(contentType, "video/"):
		return contentType, true, ""
	case strings.HasPrefix(contentType, "image/") && inlineRenderableTypes[contentType]:
		return contentType, false, ""
	case strings.HasPrefix(contentType, "image/"):
		format := strings.ToUpper(strings.TrimPrefix(contentType, "image/"))
		return contentType, false, "That's a " + format + " photo, which browsers can't show. " +
			"Save it as a JPEG or PNG and add it again."
	default:
		return contentType, false, "That isn't a photo or video, so it wasn't added. Documents go on the Files page."
	}
}

// isISOBaseMedia reports whether data opens with an ISO base-media "ftyp"
// box — the container MP4, M4V, 3GP and QuickTime files all share.
func isISOBaseMedia(data []byte) bool {
	return len(data) >= 12 && string(data[4:8]) == "ftyp"
}

// extendWriteDeadline gives the handler a fresh window to answer in.
//
// http.Server.WriteTimeout counts from the moment the request HEADERS are
// read, and it covers the handler's whole run — and by the time this
// handler starts, the middleware has already read the entire upload body.
// A video sent over an ordinary home connection takes longer than the
// server's 60 seconds to arrive, so without this the files would be stored,
// the response write would then fail, and the page would report a failure
// for an upload that had worked — inviting a retry that stores everything a
// second time.
//
// An error from SetWriteDeadline is deliberately ignored: a writer that
// cannot extend its deadline (a test recorder) is also one that has no
// deadline to extend.
func extendWriteDeadline(w http.ResponseWriter, window time.Duration) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(window))
}

// writeGalleryUploadJSON answers in JSON, which is all the page reads. The
// uploader is script-driven, so a redirect or an error page in HTML would
// only reach it as something it cannot parse.
func writeGalleryUploadJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("web: writing an album upload response: %v", err)
	}
}

func writeGalleryUploadError(w http.ResponseWriter, status int, msg string) {
	writeGalleryUploadJSON(w, status, map[string]string{"error": msg})
}

// requireGalleryUploader is requireContentEditor for a script-driven
// request: the same gate (units.CanEditUnitContent, which every album and
// news page already uses), but refusing in JSON instead of redirecting to a
// login page the uploader would try to parse as a result.
func (h *Handlers) requireGalleryUploader(w http.ResponseWriter, r *http.Request) (units.Unit, family.Member, bool) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		writeGalleryUploadError(w, http.StatusUnauthorized,
			"Your sign-in has ended. Sign in again, then add these again — nothing from this batch was added to the album.")
		return unit, family.Member{}, false
	}

	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanEditUnitContent(caps) {
		writeGalleryUploadError(w, http.StatusForbidden, "You don't have permission to add photos to this site.")
		return unit, family.Member{}, false
	}
	if h.Storage == nil {
		writeGalleryUploadError(w, http.StatusServiceUnavailable, storageUnavailableMsg)
		return unit, family.Member{}, false
	}

	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		writeGalleryUploadError(w, http.StatusBadRequest,
			"Couldn't tell who is uploading — has your family been added to the roster yet?")
		return unit, family.Member{}, false
	}
	return unit, actor, true
}

// AdminGalleryUpload stores one batch of photos and videos from the album
// editor and answers with what happened to each.
//
// Like FileUpload it is not written to the Activity Log: the library
// already records who uploaded each file and when.
func (h *Handlers) AdminGalleryUpload(w http.ResponseWriter, r *http.Request) {
	// First, before anything that can take time. See extendWriteDeadline.
	extendWriteDeadline(w, galleryUploadWriteWindow)

	unit, actor, ok := h.requireGalleryUploader(w, r)
	if !ok {
		return
	}

	// The CSRF middleware has already parsed the multipart body (the token
	// is a field inside it), so the files are here to read.
	if r.MultipartForm == nil || len(r.MultipartForm.File["file"]) == 0 {
		writeGalleryUploadError(w, http.StatusBadRequest, "Choose at least one photo or video.")
		return
	}

	public := r.FormValue("visibility") == "public"
	g := galleryUploader{
		unitID:   unit.ID,
		actorID:  actor.ID,
		folder:   h.uploadFolder(r.Context(), unit.ID, files.CategoryEventPhoto, nil),
		public:   public,
		maxBytes: maxUploadFileSize,
		put: func(ctx context.Context, key string, rd io.Reader, size int64, contentType string) error {
			return h.Storage.Put(ctx, key, rd, size, contentType)
		},
		create: func(ctx context.Context, f files.File) (files.File, error) {
			return files.Create(ctx, h.Pool, f)
		},
		thumb: func(ctx context.Context, key string, data []byte) {
			thumb, err := thumbnail.Generate(data)
			if err != nil {
				log.Printf("web: generating thumbnail for an album photo: %v", err)
				return
			}
			if err := h.Storage.Put(ctx, key+thumbStorageSuffix, bytes.NewReader(thumb), int64(len(thumb)), "image/jpeg"); err != nil {
				log.Printf("web: caching thumbnail for an album photo: %v", err)
			}
		},
	}

	results, err := g.run(r.Context(), r.MultipartForm.File["file"])
	if err != nil {
		writeGalleryUploadError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeGalleryUploadJSON(w, http.StatusOK, galleryUploadResponse{Results: results, Public: public})
}
