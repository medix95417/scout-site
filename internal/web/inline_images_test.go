package web

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/files"
)

// fakeLibrary stands in for the file table and the bucket, so what this
// tests is the policy — the caps, the sniffing, the reuse — and not
// pgx or minio.
type fakeLibrary struct {
	rows    map[string]files.File // storage key -> row
	objects map[string][]byte     // storage key -> bytes
	created []files.File
	thumbs  []string

	lookupErr error
	putErr    error
	createErr error
	nextID    int
}

func newFakeLibrary() *fakeLibrary {
	return &fakeLibrary{rows: map[string]files.File{}, objects: map[string][]byte{}}
}

func (l *fakeLibrary) host(unitID string) *inlineImageHost {
	actor := "actor-1"
	return &inlineImageHost{
		unitID:  unitID,
		siteURL: "https://pack47.example.org",
		actorID: &actor,
		lookup: func(_ context.Context, unitID, key string) (files.File, bool, error) {
			if l.lookupErr != nil {
				return files.File{}, false, l.lookupErr
			}
			f, ok := l.rows[key]
			if !ok || f.UnitID != unitID {
				return files.File{}, false, nil
			}
			return f, true, nil
		},
		put: func(_ context.Context, key string, data []byte, _ string) error {
			if l.putErr != nil {
				return l.putErr
			}
			l.objects[key] = data
			return nil
		},
		create: func(_ context.Context, f files.File) (files.File, error) {
			if l.createErr != nil {
				return files.File{}, l.createErr
			}
			l.nextID++
			f.ID = "file-" + string(rune('a'+l.nextID-1))
			l.rows[f.StorageKey] = f
			l.created = append(l.created, f)
			return f, nil
		},
		cacheThumb: func(_ context.Context, key string, _ []byte) { l.thumbs = append(l.thumbs, key) },
	}
}

// pngBytes builds a real PNG of the given size, so sniffContentType and
// thumbnail generation are exercised on something genuine.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 0, G: 63, B: 135, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestInlineImageIsStoredAndLinked(t *testing.T) {
	lib := newFakeLibrary()
	h := lib.host("unit-1")
	data := pngBytes(t, 4, 4)

	url := h.hostOne(context.Background(), data, "image/png")

	if url != "https://pack47.example.org/files/file-a/download" {
		t.Fatalf("url = %q", url)
	}
	if len(lib.created) != 1 {
		t.Fatalf("created %d rows, want 1", len(lib.created))
	}
	f := lib.created[0]
	if !f.Public {
		t.Error("the file is not public, so no recipient's mail client will be able to fetch it")
	}
	if f.UnitID != "unit-1" {
		t.Errorf("UnitID = %q", f.UnitID)
	}
	if f.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want image/png (sniffed from the bytes)", f.ContentType)
	}
	if f.SizeBytes != int64(len(data)) {
		t.Errorf("SizeBytes = %d, want %d", f.SizeBytes, len(data))
	}
	if f.UploadedBy == nil || *f.UploadedBy != "actor-1" {
		t.Error("the file is not attributed to the leader who saved the draft")
	}
	if !strings.HasPrefix(f.StorageKey, "unit-1/") {
		t.Errorf("StorageKey %q is not under the unit's own prefix", f.StorageKey)
	}
	if !bytes.Equal(lib.objects[f.StorageKey], data) {
		t.Error("the bytes written to storage are not the bytes handed over")
	}
	if len(lib.thumbs) != 1 {
		t.Errorf("cached %d thumbnails, want 1", len(lib.thumbs))
	}
}

// TestTheSameImageIsStoredOnce is why the key is a content hash. Editing
// a draft five times must not leave five copies of the logo behind.
func TestTheSameImageIsStoredOnce(t *testing.T) {
	lib := newFakeLibrary()
	data := pngBytes(t, 4, 4)

	// Twice within one body...
	h := lib.host("unit-1")
	first := h.hostOne(context.Background(), data, "image/png")
	second := h.hostOne(context.Background(), data, "image/png")
	// ...and again on a later save, which starts a fresh host.
	third := lib.host("unit-1").hostOne(context.Background(), data, "image/png")

	if first != second || second != third {
		t.Errorf("the same image produced different URLs: %q, %q, %q", first, second, third)
	}
	if len(lib.created) != 1 {
		t.Errorf("created %d rows for one image, want 1", len(lib.created))
	}
}

// TestUnitsDoNotShareStoredImages: content-addressing must not reach
// across the tenant boundary. A file belongs to the unit that saved it.
func TestUnitsDoNotShareStoredImages(t *testing.T) {
	lib := newFakeLibrary()
	data := pngBytes(t, 4, 4)

	lib.host("unit-1").hostOne(context.Background(), data, "image/png")
	lib.host("unit-2").hostOne(context.Background(), data, "image/png")

	if len(lib.created) != 2 {
		t.Fatalf("created %d rows, want one per unit", len(lib.created))
	}
	if lib.created[0].UnitID == lib.created[1].UnitID {
		t.Error("both rows belong to the same unit")
	}
	if lib.created[0].StorageKey == lib.created[1].StorageKey {
		t.Errorf("both units share storage key %q", lib.created[0].StorageKey)
	}
}

// TestOnlyRealImagesAreStored. What comes back out of /files/{id}/download
// is served from this site's own origin, so the thing going in has to be
// an image in fact and not merely by label.
func TestOnlyRealImagesAreStored(t *testing.T) {
	cases := map[string][]byte{
		// An icon IS an image and still must not be hosted: the file
		// server won't render image/x-icon in place (it isn't in
		// inlineRenderableTypes), so hosting one would swap an image
		// that displays for a link that downloads. Storing only what we
		// will actually serve inline is the point of that second check.
		"an icon": {0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x10, 0x10, 0x00, 0x00,
			0x01, 0x00, 0x20, 0x00, 0x68, 0x04, 0x00, 0x00, 0x16, 0x00, 0x00, 0x00},
		"html mislabeled as png": []byte("<html><body><script>alert(1)</script></body></html>"),
		"svg mislabeled as png":  []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"plain text":             []byte("just some text, at length, so the sniffer has plenty to go on"),
		"a zip":                  {0x50, 0x4b, 0x03, 0x04, 0x00, 0x00, 0x00, 0x00},
		"empty":                  {},
		"truncated png header":   {0x89, 0x50, 0x4e},
	}
	for name, data := range cases {
		lib := newFakeLibrary()
		if url := lib.host("unit-1").hostOne(context.Background(), data, "image/png"); url != "" {
			t.Errorf("%s: was hosted as %q, want left embedded", name, url)
		}
		if len(lib.created) != 0 {
			t.Errorf("%s: %d rows created", name, len(lib.created))
		}
	}
}

func TestOversizedImagesStayEmbedded(t *testing.T) {
	lib := newFakeLibrary()
	// Valid PNG bytes with a length past the cap — the cap must be on the
	// size, not on whether it decodes.
	data := append(pngBytes(t, 4, 4), bytes.Repeat([]byte{0}, maxInlineImageBytes)...)

	if url := lib.host("unit-1").hostOne(context.Background(), data, "image/png"); url != "" {
		t.Errorf("an oversized image was hosted as %q", url)
	}
	if len(lib.objects) != 0 {
		t.Error("an oversized image reached storage")
	}
}

// TestOneBodyCannotStoreUnboundedImages bounds the work a single form
// post can cause.
func TestOneBodyCannotStoreUnboundedImages(t *testing.T) {
	lib := newFakeLibrary()
	h := lib.host("unit-1")

	// Distinct images, so nothing is deduplicated away.
	for i := 0; i < maxInlineImagesPerBody+5; i++ {
		h.hostOne(context.Background(), pngBytes(t, 2+i, 2), "image/png")
	}
	if len(lib.created) != maxInlineImagesPerBody {
		t.Errorf("stored %d images, want the cap of %d", len(lib.created), maxInlineImagesPerBody)
	}
}

// TestRepeatsDoNotConsumeTheBudget — a spacer image used sixty times is
// one file, and shouldn't exhaust a cap meant to bound storage writes.
func TestRepeatsDoNotConsumeTheBudget(t *testing.T) {
	lib := newFakeLibrary()
	h := lib.host("unit-1")
	spacer := pngBytes(t, 1, 1)

	for i := 0; i < maxInlineImagesPerBody+20; i++ {
		if url := h.hostOne(context.Background(), spacer, "image/png"); url == "" {
			t.Fatalf("repeat %d was declined; a repeated image should not exhaust the cap", i)
		}
	}
	if len(lib.created) != 1 {
		t.Errorf("created %d rows for one repeated image", len(lib.created))
	}
}

// TestFailuresLeaveTheImageEmbedded. Every one of these is a reason to
// keep the draft intact, not to lose the picture.
func TestFailuresLeaveTheImageEmbedded(t *testing.T) {
	data := pngBytes(t, 4, 4)
	boom := errors.New("boom")

	for name, breakIt := range map[string]func(*fakeLibrary){
		"lookup fails":            func(l *fakeLibrary) { l.lookupErr = boom },
		"storage put fails":       func(l *fakeLibrary) { l.putErr = boom },
		"recording the row fails": func(l *fakeLibrary) { l.createErr = boom },
	} {
		lib := newFakeLibrary()
		breakIt(lib)
		if url := lib.host("unit-1").hostOne(context.Background(), data, "image/png"); url != "" {
			t.Errorf("%s: returned %q, want \"\" so the image stays embedded", name, url)
		}
	}
}

// TestHostInlineImagesWithoutStorageStillSanitizes is the optional-
// integration rule: a site with no bucket configured has a working
// composer, just without hosting.
func TestHostInlineImagesWithoutStorageStillSanitizes(t *testing.T) {
	h := &Handlers{} // Storage nil
	out := h.hostInlineImages(context.Background(), "unit-1", "https://pack47.example.org", nil,
		`<p onclick="steal()">hi</p><script>alert(1)</script>`)

	if strings.Contains(out, "script") || strings.Contains(out, "onclick") {
		t.Errorf("body was not sanitized:\n%s", out)
	}
	if !strings.Contains(out, "<p>hi</p>") {
		t.Errorf("body was not preserved:\n%s", out)
	}
}

func TestExtensionForImageCoversWhatWeServeInline(t *testing.T) {
	// Every type the file server is willing to render in place is a type
	// this can be asked to name, since those are exactly the ones hostOne
	// accepts. A missing case would store a file with no extension —
	// cosmetic, but it means the two lists have drifted.
	for contentType := range inlineRenderableTypes {
		if !strings.HasPrefix(contentType, "image/") {
			continue
		}
		if extensionForImage(contentType) == "" {
			t.Errorf("no extension for %q, which hostOne can accept", contentType)
		}
	}
}

// TestAHostedImageActuallyReachesTheReader ties the chain together. Every
// link has its own test elsewhere; this one asserts they still connect,
// because hosting an image is pointless if the file it produces turns out
// to need a login or to be served as a download.
func TestAHostedImageActuallyReachesTheReader(t *testing.T) {
	lib := newFakeLibrary()
	if url := lib.host("unit-1").hostOne(context.Background(), pngBytes(t, 8, 8), "image/png"); url == "" {
		t.Fatal("the image was not hosted")
	}
	f := lib.created[0]

	// A mail client fetching the image is not signed in and is not a
	// member of anything.
	if requiresLoginToDownload(f, false) {
		t.Error("the hosted image needs a login, so it will not load in anyone's inbox")
	}
	// And it has to render in place rather than arrive as a download.
	if !inlineRenderableTypes[f.ContentType] {
		t.Errorf("%s is not served inline, so the image would be a download link", f.ContentType)
	}
}
