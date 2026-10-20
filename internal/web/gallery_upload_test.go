package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/help"
)

// Uploading photos and videos from the album editor.
//
// Every rule here fails quietly in production: a photo goes missing from an
// album, a members-only photo ends up public, or the page reports a failure
// for an upload that worked. None of them show up in a render test, so each
// is checked against the real policy with a fake bucket and database.

// --- fixtures ----------------------------------------------------------------

// Real file signatures, because the server decides what a file is from its
// first bytes and not from what the browser called it.
var (
	jpegBytes = append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), bytes.Repeat([]byte{0x11}, 64)...)
	samplePNG = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x22}, 64)...)
	// ISO base media: a 24-byte ftyp box whose brands are "isom"/"mp41".
	mp4Bytes = append([]byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isommp41"), bytes.Repeat([]byte{0x33}, 256)...)
	// A real MP4 whose brands ("isom", "iso2", "avc1") carry no "mp4", which is
	// all Go's sniffer looks for — so it sniffs as plain octets.
	unsniffableMP4 = append([]byte("\x00\x00\x00\x18ftypavc1\x00\x00\x00\x00isomiso2"), bytes.Repeat([]byte{0x77}, 256)...)
	// An iPhone .mov: "qt  " brand, which Go's sniffer does not recognise.
	movBytes   = append([]byte("\x00\x00\x00\x14ftypqt  \x00\x00\x00\x00qt  "), bytes.Repeat([]byte{0x44}, 256)...)
	webmBytes  = append([]byte("\x1a\x45\xdf\xa3"), bytes.Repeat([]byte{0x55}, 64)...)
	sampleHTML = []byte("<html><body><script>alert(1)</script></body></html>")
	pdfBytes   = []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n")
	heicBytes  = append([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), bytes.Repeat([]byte{0x66}, 64)...)
)

type upload struct {
	name     string
	declared string // the Content-Type the browser claimed, which must never decide anything
	data     []byte
}

// headers builds real multipart file headers the way the request parser
// would hand them to the handler.
func headers(t *testing.T, uploads ...upload) []*multipart.FileHeader {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, u := range uploads {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, u.name))
		if u.declared != "" {
			h.Set("Content-Type", u.declared)
		}
		part, err := w.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(u.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	form, err := multipart.NewReader(&buf, w.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = form.RemoveAll() })
	return form.File["file"]
}

type storedObject struct {
	key         string
	size        int64
	contentType string
	body        []byte
}

// library stands in for the bucket and the files table.
type library struct {
	objects []storedObject
	created []files.File
	thumbs  []string

	failPut    func(key string) bool
	failCreate func(f files.File) bool
}

func (l *library) uploader(public bool) galleryUploader {
	return galleryUploader{
		unitID:   "unit-1",
		actorID:  "member-1",
		folder:   files.PhotosFolder,
		public:   public,
		maxBytes: 1 << 20,
		put: func(_ context.Context, key string, r io.Reader, size int64, contentType string) error {
			if l.failPut != nil && l.failPut(key) {
				return errors.New("bucket unreachable")
			}
			body, err := io.ReadAll(r)
			if err != nil {
				return err
			}
			l.objects = append(l.objects, storedObject{key: key, size: size, contentType: contentType, body: body})
			return nil
		},
		create: func(_ context.Context, f files.File) (files.File, error) {
			if l.failCreate != nil && l.failCreate(f) {
				return files.File{}, errors.New("database unreachable")
			}
			f.ID = "file-" + strconv.Itoa(len(l.created)+1)
			l.created = append(l.created, f)
			return f, nil
		},
		thumb: func(_ context.Context, key string, _ []byte) { l.thumbs = append(l.thumbs, key) },
	}
}

func run(t *testing.T, g galleryUploader, uploads ...upload) []galleryUploadResult {
	t.Helper()
	results, err := g.run(context.Background(), headers(t, uploads...))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return results
}

func albumPhoto(name string) upload {
	return upload{name: name, declared: "image/jpeg", data: jpegBytes}
}
func albumVideo(name string) upload { return upload{name: name, declared: "video/mp4", data: mp4Bytes} }

// --- what counts as a photo or a video ----------------------------------------

func TestClassifyGalleryMedia(t *testing.T) {
	for _, c := range []struct {
		name     string
		data     []byte
		declared string
		wantType string
		video    bool
		problem  string // substring; "" means the file is fine
	}{
		{"a jpeg", jpegBytes, "image/jpeg", "image/jpeg", false, ""},
		{"a png", samplePNG, "image/png", "image/png", false, ""},
		{"an mp4", mp4Bytes, "video/mp4", "video/mp4", true, ""},
		{"a webm", webmBytes, "video/webm", "video/webm", true, ""},
		// Go's sniffer cannot tell a QuickTime file from raw octets, so the
		// browser's claim is what keeps an iPhone video from being refused.
		{"an iPhone mov", movBytes, "video/quicktime", "video/quicktime", true, ""},
		// Accepted on the strength of the container plus the browser saying "video".
		{"an mp4 with no mp4 brand", unsniffableMP4, "video/mp4", "video/mp4", true, ""},
		{"the same file claiming to be a photo", unsniffableMP4, "image/jpeg", "", false, "isn't a photo or video"},
		{"the same file with no claim at all", unsniffableMP4, "", "", false, "isn't a photo or video"},
		// The claim never wins against the bytes.
		{"a jpeg that claims to be html", jpegBytes, "text/html", "image/jpeg", false, ""},
		{"html that claims to be a jpeg", sampleHTML, "image/jpeg", "", false, "isn't a photo or video"},
		{"a pdf", pdfBytes, "application/pdf", "", false, "Files page"},
		{"a heic photo", heicBytes, "image/heic", "", false, "HEIC"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ct, video, problem := classifyGalleryMedia(c.data, c.declared)
			if c.problem != "" {
				if !strings.Contains(problem, c.problem) {
					t.Fatalf("problem = %q, want it to mention %q (type %q)", problem, c.problem, ct)
				}
				return
			}
			if problem != "" {
				t.Fatalf("refused a file that should be fine: %s", problem)
			}
			if ct != c.wantType || video != c.video {
				t.Errorf("classified as (%q, video=%v), want (%q, video=%v)", ct, video, c.wantType, c.video)
			}
		})
	}
}

// A file whose first bytes say "html" is never stored under an image type,
// whatever it was called and whatever the browser said.
func TestAnAlbumUploadIsNeverStoredUnderTheTypeItClaimed(t *testing.T) {
	var lib library
	res := run(t, lib.uploader(false), upload{name: "totally-a-photo.jpg", declared: "image/jpeg", data: sampleHTML})
	if res[0].Error == "" {
		t.Fatal("an html file was accepted as a photo")
	}
	if len(lib.objects) != 0 || len(lib.created) != 0 {
		t.Errorf("an html file was stored: %d objects, %d rows", len(lib.objects), len(lib.created))
	}
}

// --- a batch ---------------------------------------------------------------------

func TestAlbumUploadStoresEachFileInOrder(t *testing.T) {
	var lib library
	res := run(t, lib.uploader(false), albumPhoto("a.jpg"), upload{name: "b.png", declared: "image/png", data: samplePNG}, albumPhoto("c.jpg"))

	if len(res) != 3 {
		t.Fatalf("got %d results, want 3", len(res))
	}
	// Order is the album's order: the page maps results back to the files it
	// sent by position, so this is what keeps a caption on the right photo.
	for i, name := range []string{"a.jpg", "b.png", "c.jpg"} {
		if res[i].Name != name {
			t.Errorf("result %d is %q, want %q", i, res[i].Name, name)
		}
		if res[i].Error != "" {
			t.Errorf("%s was refused: %s", name, res[i].Error)
		}
		if want := "/files/file-" + strconv.Itoa(i+1) + "/download"; res[i].URL != want {
			t.Errorf("%s links to %q, want %q", name, res[i].URL, want)
		}
		if res[i].Video {
			t.Errorf("%s was reported as a video", name)
		}
	}

	// Stored as what the bytes are, in the unit's own photos folder.
	if lib.created[1].ContentType != "image/png" {
		t.Errorf("the png was stored as %q", lib.created[1].ContentType)
	}
	for _, f := range lib.created {
		if f.Category != files.CategoryEventPhoto {
			t.Errorf("%s filed under %q, want the event photo category", f.Filename, f.Category)
		}
		if !strings.HasPrefix(f.StorageKey, "unit-1/photos/") {
			t.Errorf("%s stored at %q, want it under the unit's photos folder", f.Filename, f.StorageKey)
		}
		if f.UnitID != "unit-1" || f.UploadedBy == nil || *f.UploadedBy != "member-1" {
			t.Errorf("%s recorded for unit %q by %v", f.Filename, f.UnitID, f.UploadedBy)
		}
	}
}

// A public album's photos have to be public or a signed-out visitor sees an
// empty album; a members-only album's must not be, or anyone with the link
// can open them. The flag follows the album, and nothing else.
func TestAlbumUploadFollowsTheAlbumsVisibility(t *testing.T) {
	for _, public := range []bool{true, false} {
		var lib library
		run(t, lib.uploader(public), albumPhoto("a.jpg"), albumVideo("b.mp4"))
		for _, f := range lib.created {
			if f.Public != public {
				t.Errorf("album public=%v but %s was saved with Public=%v", public, f.Filename, f.Public)
			}
		}
		if len(lib.created) != 2 {
			t.Errorf("public=%v: stored %d files, want 2", public, len(lib.created))
		}
	}
}

// --- the limits ---------------------------------------------------------------------

func TestAlbumUploadCeilingIsFivePhotosAndOneVideo(t *testing.T) {
	var lib library
	res := run(t, lib.uploader(false),
		albumPhoto("1.jpg"), albumPhoto("2.jpg"), albumPhoto("3.jpg"), albumPhoto("4.jpg"), albumPhoto("5.jpg"), albumVideo("clip.mp4"))
	for _, r := range res {
		if r.Error != "" {
			t.Errorf("%s was refused inside the ceiling: %s", r.Name, r.Error)
		}
	}
	if len(lib.created) != 6 {
		t.Errorf("stored %d files, want 6", len(lib.created))
	}
}

func TestAnExtraPhotoOrVideoIsTurnedAwayButTheRestStay(t *testing.T) {
	t.Run("a sixth photo", func(t *testing.T) {
		var lib library
		res := run(t, lib.uploader(false),
			albumPhoto("1.jpg"), albumPhoto("2.jpg"), albumPhoto("3.jpg"), albumPhoto("4.jpg"), albumPhoto("5.jpg"), albumPhoto("6.jpg"))
		if len(lib.created) != galleryUploadMaxPhotos {
			t.Fatalf("stored %d photos, want %d", len(lib.created), galleryUploadMaxPhotos)
		}
		last := res[5]
		if last.Error == "" || !last.Retry {
			t.Errorf("the sixth photo: error=%q retry=%v — it should say to send it in the next batch", last.Error, last.Retry)
		}
	})
	t.Run("a second video", func(t *testing.T) {
		var lib library
		res := run(t, lib.uploader(false), albumVideo("one.mp4"), albumVideo("two.mp4"))
		if len(lib.created) != 1 {
			t.Fatalf("stored %d videos, want 1", len(lib.created))
		}
		if res[1].Error == "" || !res[1].Retry {
			t.Errorf("the second video: error=%q retry=%v", res[1].Error, res[1].Retry)
		}
	})
}

// More files than one request may carry is refused whole, before anything
// is opened — so a hand-made request cannot ask for hundreds of writes.
func TestTooManyFilesAreRefusedBeforeAnythingIsStored(t *testing.T) {
	var lib library
	ups := make([]upload, galleryUploadMaxParts+1)
	for i := range ups {
		ups[i] = albumPhoto("p" + strconv.Itoa(i) + ".jpg")
	}
	_, err := lib.uploader(false).run(context.Background(), headers(t, ups...))
	if !errors.Is(err, errGalleryUploadTooMany) {
		t.Fatalf("got %v, want errGalleryUploadTooMany", err)
	}
	if len(lib.objects) != 0 || len(lib.created) != 0 {
		t.Errorf("a refused request still stored %d objects and %d rows", len(lib.objects), len(lib.created))
	}
}

func TestAnOversizeFileIsRefusedAndTheRestStay(t *testing.T) {
	var lib library
	g := lib.uploader(false)
	g.maxBytes = 1024
	big := upload{name: "huge.jpg", declared: "image/jpeg", data: append(append([]byte{}, jpegBytes...), bytes.Repeat([]byte{1}, 2048)...)}

	res := run(t, g, albumPhoto("small.jpg"), big, albumPhoto("also-small.jpg"))
	if res[1].Error == "" || !strings.Contains(res[1].Error, "Too large") {
		t.Fatalf("the oversize file: %q", res[1].Error)
	}
	if res[1].Retry {
		t.Error("an oversize file is marked retryable — sending it again would fail the same way")
	}
	if res[0].Error != "" || res[2].Error != "" {
		t.Errorf("a good file was lost to the big one: %q / %q", res[0].Error, res[2].Error)
	}
	if len(lib.created) != 2 {
		t.Errorf("stored %d files, want the two that fit", len(lib.created))
	}
}

func TestAnEmptyFileIsRefused(t *testing.T) {
	var lib library
	res := run(t, lib.uploader(false), upload{name: "empty.jpg", declared: "image/jpeg", data: nil})
	if res[0].Error == "" || res[0].Retry {
		t.Errorf("empty file: error=%q retry=%v", res[0].Error, res[0].Retry)
	}
	if len(lib.created) != 0 {
		t.Error("an empty file was stored")
	}
}

// --- one file failing never costs the others ----------------------------------------

func TestOneBadFileDoesNotStopTheBatch(t *testing.T) {
	var lib library
	res := run(t, lib.uploader(false),
		albumPhoto("first.jpg"),
		upload{name: "form.pdf", declared: "application/pdf", data: pdfBytes},
		albumPhoto("third.jpg"))

	if res[0].Error != "" || res[2].Error != "" {
		t.Errorf("a good photo was refused: %q / %q", res[0].Error, res[2].Error)
	}
	if res[1].Error == "" || res[1].Retry {
		t.Errorf("the pdf: error=%q retry=%v — it can never be added here", res[1].Error, res[1].Retry)
	}
	if len(lib.created) != 2 {
		t.Errorf("stored %d files, want 2", len(lib.created))
	}
}

func TestAStorageFailureIsRetryableAndLeavesNoRow(t *testing.T) {
	var lib library
	lib.failPut = func(key string) bool { return strings.Contains(key, "flaky") }
	res := run(t, lib.uploader(false), albumPhoto("ok.jpg"), albumPhoto("flaky.jpg"), albumVideo("clip.mp4"))

	if res[1].Error == "" || !res[1].Retry {
		t.Errorf("a storage failure: error=%q retry=%v — it is worth sending again", res[1].Error, res[1].Retry)
	}
	if res[0].Error != "" || res[2].Error != "" {
		t.Errorf("a neighbour was lost to the failure: %q / %q", res[0].Error, res[2].Error)
	}
	// A row with no object behind it is a broken tile, so none is written.
	for _, f := range lib.created {
		if strings.Contains(f.Filename, "flaky") {
			t.Error("a row was recorded for a file that never reached storage")
		}
	}
}

func TestADatabaseFailureIsRetryable(t *testing.T) {
	var lib library
	lib.failCreate = func(f files.File) bool { return f.Filename == "unlucky.jpg" }
	res := run(t, lib.uploader(false), albumPhoto("unlucky.jpg"), albumPhoto("fine.jpg"))

	if res[0].Error == "" || !res[0].Retry {
		t.Errorf("a database failure: error=%q retry=%v", res[0].Error, res[0].Retry)
	}
	if res[1].Error != "" {
		t.Errorf("the next photo was lost: %s", res[1].Error)
	}
	// The failed photo must not count against the batch's photo ceiling, or
	// five photos with one hiccup would turn the fifth away for no reason.
	var lib2 library
	lib2.failCreate = func(f files.File) bool { return f.Filename == "1.jpg" }
	res = run(t, lib2.uploader(false), albumPhoto("1.jpg"), albumPhoto("2.jpg"), albumPhoto("3.jpg"), albumPhoto("4.jpg"), albumPhoto("5.jpg"), albumPhoto("6.jpg"))
	stored := 0
	for _, r := range res {
		if r.URL != "" {
			stored++
		}
	}
	if stored != galleryUploadMaxPhotos {
		t.Errorf("stored %d after one failed save, want a full %d", stored, galleryUploadMaxPhotos)
	}
}

// --- what is done with the bytes ----------------------------------------------------

// A photo gets a cached thumbnail; a video, which has no thumbnail, does
// not — and is handed to storage whole, at its real size.
func TestPhotosGetThumbnailsAndVideosAreStoredWhole(t *testing.T) {
	var lib library
	run(t, lib.uploader(false), albumPhoto("a.jpg"), albumVideo("b.mp4"), albumPhoto("c.jpg"))

	var vid storedObject
	for _, o := range lib.objects {
		if o.contentType == "video/mp4" {
			vid = o
		}
	}
	if vid.key == "" {
		t.Fatal("the video was never stored")
	}

	// Compared by the video's own key: a substring of a UUID-based key can
	// match anything.
	if len(lib.thumbs) != 2 {
		t.Fatalf("cached %d thumbnails, want 2 (the photos)", len(lib.thumbs))
	}
	for _, key := range lib.thumbs {
		if key == vid.key {
			t.Error("a thumbnail was requested for the video")
		}
	}

	if vid.size != int64(len(mp4Bytes)) || !bytes.Equal(vid.body, mp4Bytes) {
		t.Errorf("the video reached storage as %d bytes (declared %d), want all %d unchanged", len(vid.body), vid.size, len(mp4Bytes))
	}
}

func TestAnIPhoneVideoIsKeptAsAVideo(t *testing.T) {
	var lib library
	res := run(t, lib.uploader(false), upload{name: "IMG_0042.MOV", declared: "video/quicktime", data: movBytes})
	if res[0].Error != "" {
		t.Fatalf("an iPhone video was refused: %s", res[0].Error)
	}
	if !res[0].Video {
		t.Error("it was not reported as a video, so the album would draw it as a broken photo")
	}
}

// --- the contract the page reads ----------------------------------------------------

// The page is script-driven and reads these field names. Renaming one in Go
// without the script is a failure no render test sees.
func TestGalleryUploadResponseShape(t *testing.T) {
	body, err := json.Marshal(galleryUploadResponse{
		Public: true,
		Results: []galleryUploadResult{
			{Name: "a.jpg", ID: "f1", URL: "/files/f1/download"},
			{Name: "b.mp4", ID: "f2", URL: "/files/f2/download", Video: true},
			{Name: "c.pdf", Error: "nope"},
			{Name: "d.jpg", Error: "later", Retry: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Public  bool `json:"public"`
		Results []map[string]any
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Public || len(got.Results) != 4 {
		t.Fatalf("decoded %+v", got)
	}
	if got.Results[0]["url"] != "/files/f1/download" || got.Results[0]["name"] != "a.jpg" {
		t.Errorf("a success reads %v", got.Results[0])
	}
	if got.Results[1]["video"] != true {
		t.Errorf("a video reads %v", got.Results[1])
	}
	if _, has := got.Results[0]["error"]; has {
		t.Error("a success carries an error field")
	}
	if got.Results[2]["error"] != "nope" {
		t.Errorf("a refusal reads %v", got.Results[2])
	}
	if _, has := got.Results[2]["retry"]; has {
		t.Error("a refusal that can never work is marked retryable")
	}
	if got.Results[3]["retry"] != true {
		t.Errorf("a retryable failure reads %v", got.Results[3])
	}
}

// --- the request itself --------------------------------------------------------------

// Refused in JSON, not by a redirect to a login page: the page would take
// that page for a result it cannot read.
func TestAnonymousAlbumUploadIsRefusedInJSON(t *testing.T) {
	h := &Handlers{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/gallery-upload", strings.NewReader(""))

	h.AdminGalleryUpload(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", ct)
	}
	var body struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
		t.Errorf("the refusal isn't a JSON error the page can show: %q (%v)", rec.Body.String(), err)
	}
}

// --- a slow upload must still get its answer ---------------------------------------

// http.Server.WriteTimeout counts from the moment the headers are read, and
// the whole upload body is read before the handler runs. A video over a
// home connection outlasts it, so without extendWriteDeadline the files
// would be stored and the response then lost — reported to the leader as a
// failure for an upload that worked, inviting a retry that stores
// everything twice.
//
// Run against a real server with a deliberately tiny timeout. The control
// case proves the test would notice the problem, which is what makes the
// passing case mean anything.
func TestAResponseWrittenAfterWriteTimeoutStillArrives(t *testing.T) {
	const writeTimeout = 150 * time.Millisecond
	const handlerTakes = 450 * time.Millisecond // well past the timeout

	serve := func(extend bool) (string, error) {
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Stands in for "the body took a long time to arrive and the
			// storage writes took a while", both of which happen before the
			// answer is written.
			time.Sleep(handlerTakes)
			if extend {
				extendWriteDeadline(w, time.Minute)
			}
			_, _ = io.WriteString(w, "uploaded")
		}))
		srv.Config.WriteTimeout = writeTimeout
		srv.Start()
		defer srv.Close()

		resp, err := http.Get(srv.URL)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return string(body), err
	}

	if body, err := serve(false); err == nil && body == "uploaded" {
		t.Fatal("the control: the response arrived even though the write deadline had passed, " +
			"so this test cannot tell whether extendWriteDeadline works")
	}
	body, err := serve(true)
	if err != nil || body != "uploaded" {
		t.Fatalf("with the deadline extended the response still didn't arrive: %q, %v", body, err)
	}
}

// A writer that cannot extend its deadline must not break the handler.
func TestExtendWriteDeadlineToleratesAWriterWithoutDeadlines(t *testing.T) {
	rec := httptest.NewRecorder() // a recorder has no connection to set a deadline on
	extendWriteDeadline(rec, time.Minute)
	rec.WriteString("still fine")
	if rec.Body.String() != "still fine" {
		t.Error("the recorder stopped working after the call")
	}
}

// --- the page --------------------------------------------------------------------

// uploaderTag is the opening tag of the uploader box, for assertions about
// the element itself rather than the page around it.
func uploaderTag(t *testing.T, out string) string {
	t.Helper()
	i := strings.Index(out, "<div")
	for i >= 0 {
		end := strings.Index(out[i:], ">")
		if end < 0 {
			break
		}
		if tag := out[i : i+end+1]; strings.Contains(tag, "data-gallery-uploader") {
			return tag
		}
		next := strings.Index(out[i+1:], "<div")
		if next < 0 {
			break
		}
		i += 1 + next
	}
	t.Fatal("no uploader box on the page")
	return ""
}

// uploaderScript is the uploader's own <script> body — found by its first
// line, so assertions about the script cannot pass on text that merely
// appears elsewhere on the page.
func uploaderScript(t *testing.T, out string) string {
	t.Helper()
	start := strings.Index(out, "document.querySelector('[data-gallery-uploader]')")
	if start < 0 {
		t.Fatal("the uploader script isn't on the page")
	}
	script := out[start:]
	end := strings.Index(script, "</script>")
	if end < 0 {
		t.Fatal("the uploader script never closes")
	}
	return script[:end]
}

func TestAlbumFormOffersTheUploader(t *testing.T) {
	out := renderPage(t, "admin-content-form.html", albumForm(nil))
	tag := uploaderTag(t, out)

	for _, want := range []string{
		`data-action="/admin/gallery-upload"`,
		`data-target="body"`,
		`data-csrf="tok"`,
	} {
		if !strings.Contains(tag, want) {
			t.Errorf("the uploader box is missing %s", want)
		}
	}
	for _, want := range []string{`multiple`, `accept="image/*,video/*"`, `data-gu-input`, "<noscript>"} {
		if !strings.Contains(out, want) {
			t.Errorf("the page is missing %s", want)
		}
	}

	// Above the list it adds to, so it is the first thing in the way of
	// adding photos rather than something found below forty thumbnails.
	if box, list := strings.Index(out, "data-gallery-uploader"), strings.Index(out, `<textarea`); box < 0 || list < 0 || box > list {
		t.Error("the uploader is not above the photo list")
	}
}

// The limits the page batches by are the server's own, written from the
// same constants — so changing one cannot leave the other behind.
func TestTheUploaderReadsItsLimitsFromTheServersConstants(t *testing.T) {
	out := renderPage(t, "admin-content-form.html", albumForm(nil))
	tag := uploaderTag(t, out)

	for attr, want := range map[string]int64{
		"data-max-photos": galleryUploadMaxPhotos,
		"data-max-videos": galleryUploadMaxVideos,
		"data-max-bytes":  maxUploadFileSize,
	} {
		if !strings.Contains(tag, fmt.Sprintf(`%s="%d"`, attr, want)) {
			t.Errorf("%s doesn't carry the server's limit of %d", attr, want)
		}
	}
	if !strings.Contains(out, fmt.Sprintf("Photos go up %d at a time", galleryUploadMaxPhotos)) {
		t.Error("the words on the page don't state the batch size the server enforces")
	}
	if !strings.Contains(out, fmt.Sprintf("up to %d MB each", maxUploadFileSize>>20)) {
		t.Error("the words on the page don't state the size limit the server enforces")
	}

	// And the script reads them rather than carrying its own copy.
	script := uploaderScript(t, out)
	for _, attr := range []string{"data-max-photos", "data-max-bytes"} {
		if !strings.Contains(script, attr) {
			t.Errorf("the script never reads %s, so it must be using a number of its own", attr)
		}
	}
}

// hidden is the browser's own display:none, and a display utility class on
// the same element silently beats it — which left a "needs JavaScript"
// control visible to people without any, on this very page. The box has
// no display utility of its own to fight it.
func TestTheUploaderBoxCanActuallyBeHidden(t *testing.T) {
	out := renderPage(t, "admin-content-form.html", albumForm(nil))
	tag := uploaderTag(t, out)
	if !strings.Contains(tag, " hidden") {
		t.Fatal("the box doesn't start hidden, so it shows without JavaScript to drive it")
	}
	for _, display := range []string{" flex", " grid", " block", " inline"} {
		if strings.Contains(tag, display) {
			t.Errorf("the box carries %q, which overrides its hidden attribute", strings.TrimSpace(display))
		}
	}
}

// A filename comes from the leader's own machine, but it ends up in a list
// on this page, and a name with markup in it must stay text.
func TestTheUploaderNeverPutsAFilenameIntoHTML(t *testing.T) {
	out := renderPage(t, "admin-content-form.html", albumForm(nil))
	script := uploaderScript(t, out)
	for _, banned := range []string{"innerHTML", "insertAdjacentHTML", "document.write", "outerHTML"} {
		if strings.Contains(script, banned) {
			t.Errorf("the uploader script uses %s, so a crafted filename could inject markup", banned)
		}
	}
	if !strings.Contains(script, "textContent") {
		t.Error("the uploader script never uses textContent, so how is the problem list written?")
	}
}

// The uploader adds finished files to the list through the picker's own
// helper, so the format of a line lives in one place. Both halves have to
// be on the page or the first finished upload throws.
func TestTheUploaderAndPickerShareOneWayOfWritingALine(t *testing.T) {
	out := renderPage(t, "admin-content-form.html", albumForm(nil))
	if !strings.Contains(out, "window.mediaListAppend = function") {
		t.Error("the picker script doesn't publish mediaListAppend")
	}
	if !strings.Contains(out, "window.mediaListAppend(target") {
		t.Error("the uploader script doesn't use it")
	}
}

// News posts have no photo list, so no box for filling one.
func TestTheNewsEditorHasNoUploader(t *testing.T) {
	out := renderPage(t, "admin-content-form.html", adminNewsForm(true))
	if strings.Contains(out, "data-gallery-uploader") || strings.Contains(out, "data-gu-input") {
		t.Error("the news editor offers the album uploader")
	}
}

// No storage configured: the rest of the editor must keep working, and the
// page says why there is no box rather than leaving a gap.
func TestWithoutStorageTheEditorSaysSoAndOffersNoUploader(t *testing.T) {
	data := albumForm(nil)
	data.Upload = newGalleryUploadView(false)
	out := renderPage(t, "admin-content-form.html", data)

	if strings.Contains(out, "data-gallery-uploader") || strings.Contains(out, "data-gu-input") {
		t.Error("an uploader was drawn with nowhere to upload to")
	}
	if !strings.Contains(out, "isn't set up yet") {
		t.Error("nothing explains why there is no uploader")
	}
	// The library picker, and the list itself, are still there.
	if !strings.Contains(out, `<textarea`) {
		t.Error("the photo list went missing")
	}
}

// The help text states the size limit and the batch size. A number written
// into prose is a copy, and a copy is what stops matching — so this holds
// the words to the constants the server actually enforces.
func TestTheHelpTextStatesTheRealLimits(t *testing.T) {
	var body string
	for _, topic := range help.Topics {
		if topic.ID == "content-editing" {
			body = strings.Join(topic.Body, "\n")
		}
	}
	if body == "" {
		t.Fatal("the content-editing help topic has gone")
	}
	if !strings.Contains(body, fmt.Sprintf("%d MB", maxUploadFileSize>>20)) {
		t.Errorf("the help text doesn't state the %d MB limit the server enforces", maxUploadFileSize>>20)
	}
	if !strings.Contains(body, "five photos at a time") || galleryUploadMaxPhotos != 5 {
		t.Errorf("the help text says five photos at a time but the server's batch is %d", galleryUploadMaxPhotos)
	}
	if !strings.Contains(body, "a video on its own") || galleryUploadMaxVideos != 1 {
		t.Errorf("the help text says a video on its own but the server allows %d per upload", galleryUploadMaxVideos)
	}
}
