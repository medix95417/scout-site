package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/storage"
)

// fakeStore is an in-memory Store. The logic worth testing here is the
// archive handling, not minio's client, so a live S3 endpoint would only
// make these tests slower and flakier without checking anything more.
type fakeStore struct {
	objects map[string][]byte
	types   map[string]string
	getErr  error
}

func newFake() *fakeStore {
	return &fakeStore{objects: map[string][]byte{}, types: map[string]string{}}
}

func (f *fakeStore) add(key, body, contentType string) {
	f.objects[key] = []byte(body)
	f.types[key] = contentType
}

func (f *fakeStore) List(context.Context) ([]storage.Object, error) {
	var out []storage.Object
	for k, v := range f.objects {
		out = append(out, storage.Object{Key: k, Size: int64(len(v)), ContentType: f.types[k]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	b, ok := f.objects[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *fakeStore) Put(_ context.Context, key string, r io.Reader, size int64, contentType string) error {
	b, err := io.ReadAll(io.LimitReader(r, size))
	if err != nil {
		return err
	}
	f.objects[key] = b
	f.types[key] = contentType
	return nil
}

// TestExportImport_RoundTrips is the whole point of the package: what
// comes back has to be byte-for-byte what went in, under the same keys
// and content types. A backup that restores *nearly* the right thing is
// the failure nobody notices until it matters.
func TestExportImport_RoundTrips(t *testing.T) {
	ctx := context.Background()
	src := newFake()
	src.add("photos/campout.jpg", "\xff\xd8\xff\xe0 not really a jpeg but binary", "image/jpeg")
	src.add("docs/handbook.pdf", "%PDF-1.4 ...", "application/pdf")
	src.add("photos/nested/deep/file with spaces.png", strings.Repeat("x", 5000), "image/png")
	src.add("empty.txt", "", "text/plain")

	var archive bytes.Buffer
	res, err := Export(ctx, src, &archive)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if res.Objects != 4 {
		t.Errorf("exported %d objects, want 4", res.Objects)
	}

	dst := newFake()
	imported, err := Import(ctx, dst, &archive)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported.Objects != res.Objects || imported.Bytes != res.Bytes {
		t.Errorf("imported %+v, exported %+v — they should match", imported, res)
	}

	for key, want := range src.objects {
		got, ok := dst.objects[key]
		if !ok {
			t.Errorf("%q didn't survive the round trip", key)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%q came back different: %d bytes vs %d", key, len(got), len(want))
		}
		if dst.types[key] != src.types[key] {
			t.Errorf("%q content type = %q, want %q", key, dst.types[key], src.types[key])
		}
	}
}

// TestExport_FailsLoudlyOnAnUnreadableObject pins the choice not to skip.
// A backup that quietly omits a file is worse than one that fails: the
// failure gets fixed today, the omission gets discovered the day it's
// needed.
func TestExport_FailsLoudlyOnAnUnreadableObject(t *testing.T) {
	src := newFake()
	src.add("photos/one.jpg", "data", "image/jpeg")
	src.getErr = errors.New("storage is having a bad day")

	var archive bytes.Buffer
	if _, err := Export(context.Background(), src, &archive); err == nil {
		t.Fatal("expected Export to fail rather than produce an archive missing the object")
	}
}

// TestImport_RejectsUnsafeKeys covers the archive-entry names that
// shouldn't become object keys. Object storage has no path traversal in
// the filesystem sense, so this is defence in depth — but an archive
// this code unpacks today may be unpacked with tar by a person tomorrow.
func TestImport_RejectsUnsafeKeys(t *testing.T) {
	for _, name := range []string{"../../etc/passwd", "/etc/passwd", "photos/../../../secret", ""} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		body := "x"
		// An empty name can't go through WriteHeader, so that case is
		// checked against safeKey directly below instead.
		if name != "" {
			if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(body)), Mode: 0o600, Typeflag: tar.TypeReg}); err != nil {
				t.Fatalf("building a test archive for %q: %v", name, err)
			}
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatalf("writing test body: %v", err)
			}
			tw.Close()

			dst := newFake()
			if _, err := Import(context.Background(), dst, &buf); err == nil {
				t.Errorf("Import accepted unsafe entry name %q", name)
			}
			if len(dst.objects) != 0 {
				t.Errorf("Import stored something for unsafe name %q", name)
			}
			continue
		}
		if err := safeKey(name); err == nil {
			t.Errorf("safeKey accepted an empty name")
		}
	}
}

// TestImport_IsAdditive checks that restoring doesn't delete anything the
// archive predates — a store holding files newer than the backup should
// keep them.
func TestImport_IsAdditive(t *testing.T) {
	ctx := context.Background()
	src := newFake()
	src.add("old.jpg", "from the backup", "image/jpeg")

	var archive bytes.Buffer
	if _, err := Export(ctx, src, &archive); err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst := newFake()
	dst.add("newer.jpg", "uploaded after the backup was taken", "image/jpeg")
	dst.add("old.jpg", "a stale copy that should be replaced", "image/jpeg")

	if _, err := Import(ctx, dst, &archive); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if _, ok := dst.objects["newer.jpg"]; !ok {
		t.Error("Import deleted a file the archive didn't mention")
	}
	if string(dst.objects["old.jpg"]) != "from the backup" {
		t.Errorf("Import should replace a matching key, got %q", dst.objects["old.jpg"])
	}
}

// TestExport_EmptyStoreProducesAValidArchive — a site with no photos yet
// should still back up cleanly rather than erroring or writing garbage.
func TestExport_EmptyStoreProducesAValidArchive(t *testing.T) {
	ctx := context.Background()
	var archive bytes.Buffer
	res, err := Export(ctx, newFake(), &archive)
	if err != nil {
		t.Fatalf("Export of an empty store: %v", err)
	}
	if res.Objects != 0 {
		t.Errorf("expected 0 objects, got %d", res.Objects)
	}
	if _, err := Import(ctx, newFake(), &archive); err != nil {
		t.Fatalf("the empty archive should still import cleanly: %v", err)
	}
}
