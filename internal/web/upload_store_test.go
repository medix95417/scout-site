package web

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"strings"
	"testing"
)

// How an upload's bytes reach storage.
//
// The claim worth testing is a negative one — that the server does NOT hold a
// big file in memory — and a negative claim is easy to break without anyone
// noticing: the upload still works, still passes every functional test, and
// the only symptom is a server that is killed for memory the day two leaders
// upload a video at once. So these tests count the bytes read.

// countingFile is an upload as the multipart parser hands it over — readable
// and seekable — that counts every byte read from it.
type countingFile struct {
	*bytes.Reader
	read int
}

func newCountingFile(data []byte) *countingFile { return &countingFile{Reader: bytes.NewReader(data)} }

func (c *countingFile) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.read += n
	return n, err
}

func (c *countingFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.Reader.ReadAt(p, off)
	c.read += n
	return n, err
}

func (c *countingFile) Close() error { return nil }

// putCall is what storage was handed.
type putCall struct {
	key, contentType string
	size             int64
	reader           io.Reader
	// readBeforePut is how much of the file had been read by the moment
	// storage was called — the number that says whether it was buffered.
	readBeforePut int
	body          []byte
}

func recordingPut(src *countingFile, calls *[]putCall) uploadPutFunc {
	return func(_ context.Context, key string, r io.Reader, size int64, contentType string) error {
		call := putCall{key: key, contentType: contentType, size: size, reader: r, readBeforePut: src.read}
		body, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		call.body = body
		*calls = append(*calls, call)
		return nil
	}
}

// padded makes a fixture bigger than the sniff window, so a test cannot pass
// merely because the whole file fits inside the 512 bytes that get read.
func padded(data []byte, extra int) []byte {
	return append(append([]byte{}, data...), bytes.Repeat([]byte("."), extra)...)
}

// --- the rule: an image is read whole, anything else is streamed -----------------------

func TestAnImageIsReadWholeBecauseItsThumbnailNeedsTheBytes(t *testing.T) {
	data := padded(jpegBytes, 4000)
	src := newCountingFile(data)
	var calls []putCall
	var thumbKey string
	var thumbData []byte

	err := storeUpload(context.Background(), recordingPut(src, &calls),
		func(_ context.Context, key string, d []byte) { thumbKey, thumbData = key, d },
		src, int64(len(data)), "unit/photos/x.jpg", "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}

	if len(calls) != 1 || !bytes.Equal(calls[0].body, data) || calls[0].size != int64(len(data)) {
		t.Fatalf("storage got %+v, want the whole image once", calls)
	}
	if calls[0].readBeforePut != len(data) {
		t.Errorf("read %d of %d bytes before storing — an image is read whole, in one go", calls[0].readBeforePut, len(data))
	}
	if _, isBuffer := calls[0].reader.(*bytes.Reader); !isBuffer {
		t.Errorf("storage was handed a %T, want the buffer the thumbnail was made from", calls[0].reader)
	}
	if thumbKey != "unit/photos/x.jpg" || !bytes.Equal(thumbData, data) {
		t.Error("the thumbnail wasn't made from the bytes that were stored")
	}
}

// The point of the change. For a video or a document, nothing has been read
// of the file when storage starts receiving it: it comes from the request's
// own temp file as storage asks for it.
func TestAnythingButAnImageIsStreamedNotBuffered(t *testing.T) {
	for _, c := range []struct {
		name, contentType string
		data              []byte
	}{
		{"an mp4", "video/mp4", mp4Bytes},
		{"an iPhone mov", "video/quicktime", movBytes},
		{"a pdf", "application/pdf", pdfBytes},
		{"something unrecognised", "application/octet-stream", bytes.Repeat([]byte{0, 0x9f}, 40)},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := padded(c.data, 3<<20) // 3 MB: well past anything that could be a header
			src := newCountingFile(data)
			var calls []putCall
			thumbs := 0

			err := storeUpload(context.Background(), recordingPut(src, &calls),
				func(context.Context, string, []byte) { thumbs++ },
				src, int64(len(data)), "unit/documents/x", c.contentType)
			if err != nil {
				t.Fatal(err)
			}

			if len(calls) != 1 {
				t.Fatalf("storage was called %d times, want 1", len(calls))
			}
			call := calls[0]
			if call.readBeforePut != 0 {
				t.Errorf("%d bytes of a %d byte file were read before storage was called — it was buffered, "+
					"which is the cost this exists to avoid", call.readBeforePut, len(data))
			}
			if call.reader != io.Reader(src) {
				t.Errorf("storage was handed %T, want the upload itself so it can pull from it", call.reader)
			}
			if !bytes.Equal(call.body, data) || call.size != int64(len(data)) {
				t.Errorf("storage received %d bytes of a declared %d, want all %d unchanged", len(call.body), call.size, len(data))
			}
			if thumbs != 0 {
				t.Error("a thumbnail was requested for something that isn't an image")
			}
		})
	}
}

// --- reading just enough to decide what a file is -------------------------------------------

func TestTheHeadIsJustTheSniffWindowAndTheFileIsRewound(t *testing.T) {
	data := padded(jpegBytes, 5<<20)
	src := newCountingFile(data)

	head, err := readUploadHead(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(head) != sniffWindow || !bytes.Equal(head, data[:sniffWindow]) {
		t.Errorf("head is %d bytes, want the first %d", len(head), sniffWindow)
	}
	if src.read != sniffWindow {
		t.Errorf("%d bytes of a %d byte file were read to decide what it was, want %d", src.read, len(data), sniffWindow)
	}
	// Rewound: the next reader of the file starts at its beginning.
	first := make([]byte, 4)
	if _, err := io.ReadFull(src, first); err != nil || !bytes.Equal(first, data[:4]) {
		t.Errorf("after the head read the file was not at its start: %v %x", err, first)
	}
}

func TestAShortOrEmptyFileHasAShortHead(t *testing.T) {
	for _, c := range []struct {
		name string
		data []byte
	}{{"a small file", []byte("hello")}, {"an empty file", nil}} {
		t.Run(c.name, func(t *testing.T) {
			head, err := readUploadHead(newCountingFile(c.data))
			if err != nil {
				t.Fatalf("a file shorter than the window is not an error: %v", err)
			}
			if !bytes.Equal(head, c.data) {
				t.Errorf("head = %q, want %q", head, c.data)
			}
		})
	}
}

// Files used to be sniffed from the whole file; they are now sniffed from the
// first 512 bytes. That is only the same decision if http.DetectContentType
// never looks further, which is its documented behaviour — and this holds the
// swap to it, for every kind of file this site handles and every claim a
// browser might make.
func TestSniffingTheHeadAgreesWithSniffingTheWholeFile(t *testing.T) {
	fixtures := map[string][]byte{
		"jpeg": jpegBytes, "png": samplePNG, "mp4": mp4Bytes, "mov": movBytes, "webm": webmBytes,
		"html": sampleHTML, "pdf": pdfBytes, "heic": heicBytes, "mp4 with no mp4 brand": unsniffableMP4,
		"raw octets": bytes.Repeat([]byte{0, 0x9f, 0x80}, 200),
		"plain text": []byte(strings.Repeat("A line of ordinary text.\n", 60)),
		"zip":        append([]byte("PK\x03\x04"), bytes.Repeat([]byte{7}, 100)...),
	}
	for name, data := range fixtures {
		whole := padded(data, 8000)
		head, err := readUploadHead(bytes.NewReader(whole))
		if err != nil {
			t.Fatal(err)
		}
		for _, claim := range []string{"", "application/octet-stream", "video/quicktime", "image/jpeg", "text/html", "application/pdf"} {
			if got, want := sniffContentType(head, claim), sniffContentType(whole, claim); got != want {
				t.Errorf("%s claiming %q: the head says %q, the whole file says %q", name, claim, got, want)
			}
		}
	}
}

// --- failures ---------------------------------------------------------------------------------

func TestAnImageShorterThanItsDeclaredSizeIsAReadError(t *testing.T) {
	src := newCountingFile(padded(jpegBytes, 10)) // a little over 70 bytes
	var calls []putCall
	err := storeUpload(context.Background(), recordingPut(src, &calls), nil, src, 5000, "k", "image/jpeg")
	if !errors.Is(err, errReadingUpload) {
		t.Fatalf("got %v, want a read error — the file was shorter than it said", err)
	}
	if len(calls) != 0 {
		t.Error("a truncated image reached storage")
	}
}

// The one path that allocates a buffer of the declared size must not trust a
// number a caller forgot to check.
func TestAnImageBufferIsNeverSizedBeyondTheLimit(t *testing.T) {
	for _, size := range []int64{maxUploadFileSize + 1, 1 << 40, -1} {
		src := newCountingFile([]byte("tiny"))
		var calls []putCall
		err := storeUpload(context.Background(), recordingPut(src, &calls), nil, src, size, "k", "image/png")
		if !errors.Is(err, errReadingUpload) {
			t.Errorf("size %d: got %v, want it refused before any allocation", size, err)
		}
		if len(calls) != 0 || src.read != 0 {
			t.Errorf("size %d: something was read or stored (%d bytes read)", size, src.read)
		}
	}
}

// A storage failure is not a read failure: the two tell the person different
// things, and the handlers choose the message by which one it was.
func TestAStorageFailureIsNotAReadFailure(t *testing.T) {
	boom := errors.New("bucket unreachable")
	for _, contentType := range []string{"image/jpeg", "video/mp4"} {
		src := newCountingFile(padded(jpegBytes, 100))
		thumbs := 0
		err := storeUpload(context.Background(),
			func(context.Context, string, io.Reader, int64, string) error { return boom },
			func(context.Context, string, []byte) { thumbs++ },
			src, int64(len(padded(jpegBytes, 100))), "k", contentType)
		if !errors.Is(err, boom) || errors.Is(err, errReadingUpload) {
			t.Errorf("%s: got %v, want the storage error, not a read error", contentType, err)
		}
		if thumbs != 0 {
			t.Errorf("%s: a thumbnail was cached for a file that never reached storage", contentType)
		}
	}
}

// --- nothing slips back to reading a whole upload ----------------------------------------------

// The cost this removes comes back the moment a handler reads an upload for
// itself, and nothing functional would notice. Every function that opens an
// uploaded file must hand it to storeUpload and must not slurp it with
// io.ReadAll. Found by what they do — opening a file header — rather than by a
// list that would need remembering.
func TestNoUploadHandlerReadsAWholeFileForItself(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	found := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			body := string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
			if !strings.Contains(body, "fh.Open()") {
				continue
			}
			found++
			if !strings.Contains(body, "storeUpload(") {
				t.Errorf("%s (in %s) opens an uploaded file but never hands it to storeUpload", fn.Name.Name, name)
			}
			if strings.Contains(body, "io.ReadAll(") {
				t.Errorf("%s (in %s) reads an uploaded file whole with io.ReadAll — a large video would then cost "+
					"the server several times its size in memory; see upload_store.go", fn.Name.Name, name)
			}
		}
	}
	if found < 2 {
		t.Fatalf("found %d functions that open an upload, expected at least 2 — the scan has lost its subject", found)
	}
}
