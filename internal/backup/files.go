// Package backup packs the site's stored photos and documents into a tar
// archive, and unpacks one back again.
//
// Only the object store is handled here. The database is dumped by the
// real pg_dump inside the postgres container (see scripts/backup.sh) —
// hand-rolling a replacement for pg_dump would put a bespoke, rarely
// exercised code path on the one road you only ever travel in an
// emergency, which is the worst possible place for it. The photos are
// different: they live in S3-compatible storage that no tool on the host
// necessarily knows how to reach, while this binary already holds the
// credentials and a client for it.
//
// Tar rather than zip because it streams: Export writes to an io.Writer
// as it walks the bucket and Import reads from an io.Reader, so neither
// ever holds more than one object in memory and a backup of a library
// larger than RAM still works.
package backup

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/47-yonkers/scout-site/internal/storage"
)

// Store is the slice of internal/storage.Store this package needs. An
// interface rather than the concrete type so the pack/unpack logic can
// be tested without a live S3 endpoint — the part worth testing is the
// archive handling, not minio's client.
// Uses storage.Object rather than redeclaring it: the struct is plain
// data, so a fake still needs no S3 endpoint, and one definition can't
// drift from the other.
type Store interface {
	List(ctx context.Context) ([]storage.Object, error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
}

// contentTypePrefix labels each entry's content type in its tar header's
// PAX records, so Import can restore an object with the same type it was
// stored under rather than guessing from the extension.
const contentTypePrefix = "SCOUTSITE.contenttype"

// Result reports what an Export or Import moved, for the operator
// running it to sanity-check against what they expected.
type Result struct {
	Objects int
	Bytes   int64
}

// Export writes every object in the store to w as a tar archive.
//
// Returns an error rather than skipping on the first object it can't
// read. A backup that quietly omits files is worse than one that fails
// loudly: the failure gets noticed and fixed today, the omission gets
// noticed the day you need it.
func Export(ctx context.Context, s Store, w io.Writer) (Result, error) {
	objects, err := s.List(ctx)
	if err != nil {
		return Result{}, err
	}

	tw := tar.NewWriter(w)
	var res Result
	for _, obj := range objects {
		if err := exportOne(ctx, s, tw, obj, &res); err != nil {
			return res, err
		}
	}
	if err := tw.Close(); err != nil {
		return res, fmt.Errorf("backup: finishing archive: %w", err)
	}
	return res, nil
}

// exportOne is its own function so the object's reader is closed as soon
// as it's written, rather than every reader staying open until the whole
// walk finishes.
func exportOne(ctx context.Context, s Store, tw *tar.Writer, obj storage.Object, res *Result) error {
	r, err := s.Get(ctx, obj.Key)
	if err != nil {
		return fmt.Errorf("backup: reading %q: %w", obj.Key, err)
	}
	defer r.Close()

	hdr := &tar.Header{
		Name:    obj.Key,
		Size:    obj.Size,
		Mode:    0o600,
		ModTime: time.Now(),
		Format:  tar.FormatPAX,
	}
	if obj.ContentType != "" {
		hdr.PAXRecords = map[string]string{contentTypePrefix: obj.ContentType}
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("backup: writing header for %q: %w", obj.Key, err)
	}

	// Copy exactly the declared size. tar refuses a body that doesn't
	// match its header, so an object that changed length between the
	// listing and the read fails here instead of producing an archive
	// that unpacks into subtly wrong files.
	n, err := io.Copy(tw, io.LimitReader(r, obj.Size))
	if err != nil {
		return fmt.Errorf("backup: copying %q: %w", obj.Key, err)
	}
	if n != obj.Size {
		return fmt.Errorf("backup: %q is %d bytes but the listing said %d — it changed mid-backup, so this archive would be corrupt", obj.Key, n, obj.Size)
	}

	res.Objects++
	res.Bytes += n
	return nil
}

// Import reads a tar archive from r and writes every entry back into the
// store, replacing anything already at the same key.
//
// Deliberately additive: it never deletes objects the archive doesn't
// mention. Restoring into a store that already holds files should not be
// able to destroy something the archive simply predates.
func Import(ctx context.Context, s Store, r io.Reader) (Result, error) {
	tr := tar.NewReader(r)
	var res Result
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return res, nil
		}
		if err != nil {
			return res, fmt.Errorf("backup: reading archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // directories and anything exotic: nothing to store
		}
		if err := safeKey(hdr.Name); err != nil {
			return res, err
		}

		contentType := hdr.PAXRecords[contentTypePrefix]
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		if err := s.Put(ctx, hdr.Name, tr, hdr.Size, contentType); err != nil {
			return res, fmt.Errorf("backup: restoring %q: %w", hdr.Name, err)
		}
		res.Objects++
		res.Bytes += hdr.Size
	}
}

// safeKey rejects archive entry names that shouldn't become object keys.
//
// Object storage has no directory traversal in the filesystem sense, so
// "../../etc/passwd" is just an odd key rather than a path escape. It's
// still refused: a tar being unpacked by this code today could be
// unpacked to disk by a person with tar tomorrow, and an archive whose
// entries can't escape is one fewer thing that has to stay true.
func safeKey(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("backup: archive contains an entry with no name")
	case strings.HasPrefix(name, "/"):
		return fmt.Errorf("backup: archive entry %q is an absolute path", name)
	case strings.Contains(name, ".."):
		return fmt.Errorf("backup: archive entry %q contains \"..\"", name)
	}
	return nil
}
