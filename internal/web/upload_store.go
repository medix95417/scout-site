package web

// How an uploaded file's bytes get from the request to storage.
//
// Two handlers take files — FileUpload for the library and AdminGalleryUpload
// for the album editor — and each used to carry its own copy of "decide what
// this is, then store it". The Files page's copy read every file whole into
// memory first: a 150 MB video raised the server's memory by about 376 MB, and
// two people uploading 130 MB videos at the same moment took it to about
// 600 MB. The album uploader's copy streamed a video instead and added a
// fraction of that. One place now, so the two cannot drift apart again; the
// Files page measures about 40 MB for the 150 MB video and about 66 MB for the
// pair.
//
// THE RULE: read an upload whole only if something needs its bytes twice.
// That is an image, whose thumbnail is made from the same bytes that are
// being stored; re-reading from storage right after writing to it would be a
// wasted round trip. Everything else — a video, a PDF, a document — has
// nothing to derive, so it goes from the request's own temp file straight to
// storage, and the server holds a few megabytes at a time rather than the
// file.
//
// Even an image avoids the growth io.ReadAll does. The size is known — the
// multipart parser counted it — so the buffer is allocated once at that size.
// ReadAll starts small and doubles-ish as it reads, and for a large file the
// old copies it leaves behind are part of why the cost was several times the
// file.
//
// What an image still costs is its decode. A 69 MB, 24-megapixel PNG raised
// memory by about 273 MB — down from about 341 MB, but not small — because the
// thumbnail is made by decoding the whole picture. That is bounded by
// thumbnail.MaxPixels rather than by the size of the upload, and a real photo
// is a few megabytes and a fraction of that.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/47-yonkers/scout-site/internal/thumbnail"
)

// sniffWindow is how much of a file http.DetectContentType looks at. It reads
// at most this many bytes and ignores the rest, which is what makes sniffing
// just the head the same decision as sniffing the whole file.
const sniffWindow = 512

// uploadPutFunc is how bytes reach storage. The same shape as
// storage.Store.Put, so a handler passes h.Storage.Put directly, and a test
// passes a fake.
type uploadPutFunc func(ctx context.Context, key string, r io.Reader, size int64, contentType string) error

// uploadThumbFunc caches an image's thumbnail, best-effort: FileThumbnail
// regenerates one on demand when this fails, as it does for every file
// uploaded before thumbnails were cached. May be nil.
type uploadThumbFunc func(ctx context.Context, key string, data []byte)

// errReadingUpload marks a failure to read the uploaded bytes, as opposed to
// a failure to store them. The two read differently to the person: one says
// the file couldn't be read, the other that it couldn't be saved.
var errReadingUpload = errors.New("reading the uploaded file")

// readUploadHead returns the first bytes of an upload — enough to decide what
// it is — and leaves src rewound to the start, so the caller can then read or
// stream the whole file. A file shorter than the window, or empty, is not an
// error.
func readUploadHead(src io.ReadSeeker) ([]byte, error) {
	head := make([]byte, sniffWindow)
	n, err := io.ReadFull(src, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return head[:n], nil
}

// storeUpload writes one upload's bytes to storage under key.
//
// size is the file's real size, as the multipart parser counted it. Callers
// have already refused anything over their own limit; the check on an image
// below is defence in depth, since that is the one path that allocates a
// buffer of this size and a caller that forgot its check must not turn a
// number into a giant allocation.
//
// A failure reading the upload is wrapped in errReadingUpload; anything else
// came from storage.
func storeUpload(ctx context.Context, put uploadPutFunc, thumb uploadThumbFunc, src io.Reader, size int64, key, contentType string) error {
	if !strings.HasPrefix(contentType, "image/") {
		// Streamed. The reader is handed to storage as it is, so what the
		// server holds is storage's own buffering, not the file.
		return put(ctx, key, src, size, contentType)
	}

	if size < 0 || size > maxUploadFileSize {
		return fmt.Errorf("%w: %d bytes is outside what an image upload may be", errReadingUpload, size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(src, data); err != nil {
		return fmt.Errorf("%w: %v", errReadingUpload, err)
	}
	if err := put(ctx, key, bytes.NewReader(data), size, contentType); err != nil {
		return err
	}
	if thumb != nil {
		thumb(ctx, key, data)
	}
	return nil
}

// cacheUploadThumbnail is the uploadThumbFunc every handler passes: it makes
// the thumbnail at upload time, while the leader is already waiting, rather
// than the first time anyone views the photo — see FileThumbnail's doc comment
// for why that used to mean a visitor's page load could trigger a real-time
// resize. Not fatal on its own: a failure here just leaves FileThumbnail's
// on-demand fallback to cover it later.
func (h *Handlers) cacheUploadThumbnail(ctx context.Context, key string, data []byte) {
	thumb, err := thumbnail.Generate(data)
	if err != nil {
		log.Printf("web: generating thumbnail for an uploaded image: %v", err)
		return
	}
	if err := h.Storage.Put(ctx, key+thumbStorageSuffix, bytes.NewReader(thumb), int64(len(thumb)), "image/jpeg"); err != nil {
		log.Printf("web: caching thumbnail for an uploaded image: %v", err)
	}
}
