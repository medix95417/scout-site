package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// What a browser needs of a video, and what the server used to answer instead:
// a Range header got the whole file back as a plain 200, with no length and no
// Accept-Ranges, so a viewer could neither skip ahead nor resume.

var storedModTime = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func storedBytes() []byte {
	b := make([]byte, 5000)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

// serveVideo runs one request through exactly what FileDownload does once it
// has the object: the safe headers, then the ranged body.
func serveVideo(t *testing.T, method string, hdr map[string]string, content io.ReadSeeker, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/files/abc/download", nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	writeUserFileHeaders(w, contentType, "camp.mp4")
	serveStoredContent(w, r, content, storedModTime, "abc123")
	return w
}

func TestAWholeDownloadStatesItsLengthAndAdvertisesRanges(t *testing.T) {
	data := storedBytes()
	w := serveVideo(t, "GET", nil, bytes.NewReader(data), "video/mp4")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Length"); got != strconv.Itoa(len(data)) {
		t.Errorf("Content-Length = %q, want %d — a browser cannot size or seek a video without it", got, len(data))
	}
	if got := w.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", got)
	}
	if got := w.Header().Get("ETag"); got != `"abc123"` {
		t.Errorf("ETag = %q, want the stored object's, quoted", got)
	}
	if w.Header().Get("Last-Modified") == "" {
		t.Error("no Last-Modified")
	}
	if !bytes.Equal(w.Body.Bytes(), data) {
		t.Error("body is not the stored bytes")
	}
}

func TestARangeRequestGetsOnlyThoseBytes(t *testing.T) {
	data := storedBytes()
	cases := []struct {
		name, header string
		wantRange    string
		want         []byte
	}{
		{"a middle slice", "bytes=100-199", "bytes 100-199/5000", data[100:200]},
		// What Chrome sends first for a <video>: everything, as a range.
		{"open-ended from the start", "bytes=0-", "bytes 0-4999/5000", data},
		{"open-ended from the middle (a seek)", "bytes=4000-", "bytes 4000-4999/5000", data[4000:]},
		{"the last bytes (where an MP4's index often is)", "bytes=-50", "bytes 4950-4999/5000", data[4950:]},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := serveVideo(t, "GET", map[string]string{"Range": c.header}, bytes.NewReader(data), "video/mp4")
			if w.Code != http.StatusPartialContent {
				t.Fatalf("status = %d, want 206", w.Code)
			}
			if got := w.Header().Get("Content-Range"); got != c.wantRange {
				t.Errorf("Content-Range = %q, want %q", got, c.wantRange)
			}
			if got := w.Header().Get("Content-Length"); got != strconv.Itoa(len(c.want)) {
				t.Errorf("Content-Length = %q, want %d", got, len(c.want))
			}
			if !bytes.Equal(w.Body.Bytes(), c.want) {
				t.Error("body is not the requested slice")
			}
		})
	}
}

func TestAnImpossibleRangeIsRefusedNotAnsweredWithTheWholeFile(t *testing.T) {
	w := serveVideo(t, "GET", map[string]string{"Range": "bytes=9000-"}, bytes.NewReader(storedBytes()), "video/mp4")
	if w.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416", w.Code)
	}
	if got := w.Header().Get("Content-Range"); got != "bytes */5000" {
		t.Errorf("Content-Range = %q, want bytes */5000", got)
	}
}

// If-Range is what makes resuming safe: the client says "give me the rest, but
// only if it is still the file I started". A file replaced since must come back
// whole rather than as a splice of two different files.
func TestResumingOnlyContinuesTheSameFile(t *testing.T) {
	data := storedBytes()

	same := serveVideo(t, "GET", map[string]string{"Range": "bytes=1000-", "If-Range": `"abc123"`}, bytes.NewReader(data), "video/mp4")
	if same.Code != http.StatusPartialContent {
		t.Errorf("If-Range with the current ETag: status = %d, want 206", same.Code)
	}

	changed := serveVideo(t, "GET", map[string]string{"Range": "bytes=1000-", "If-Range": `"an-older-version"`}, bytes.NewReader(data), "video/mp4")
	if changed.Code != http.StatusOK || !bytes.Equal(changed.Body.Bytes(), data) {
		t.Errorf("If-Range with a stale ETag: status = %d, body %d bytes; want 200 and the whole file", changed.Code, changed.Body.Len())
	}
}

func TestAHeadRequestGetsTheHeadersAndNoBody(t *testing.T) {
	w := serveVideo(t, "HEAD", nil, bytes.NewReader(storedBytes()), "video/mp4")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Header().Get("Content-Length") != "5000" {
		t.Errorf("Content-Length = %q, want 5000", w.Header().Get("Content-Length"))
	}
	if w.Body.Len() != 0 {
		t.Errorf("HEAD response carried %d body bytes", w.Body.Len())
	}
}

// The reason ranges are worth having: a request for 100 bytes reads 100 bytes,
// not the file up to them, and a response with a type already set is never
// sniffed (which would read the head of the file).
type countingSeeker struct {
	r    *bytes.Reader
	read int
}

func (c *countingSeeker) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}
func (c *countingSeeker) Seek(off int64, whence int) (int64, error) { return c.r.Seek(off, whence) }

func TestARangeRequestReadsOnlyTheRange(t *testing.T) {
	src := &countingSeeker{r: bytes.NewReader(storedBytes())}
	w := serveVideo(t, "GET", map[string]string{"Range": "bytes=2000-2099"}, src, "video/mp4")
	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", w.Code)
	}
	if src.read != 100 {
		t.Errorf("read %d bytes of the source to answer a 100-byte range", src.read)
	}
}

// Ranges must not be a way around the rules in file_serving.go. A file that is
// served as a download, or in a sandbox, is served that way whether the client
// asked for all of it or a slice.
func TestRangesDoNotLoosenTheSafeHeaders(t *testing.T) {
	for _, c := range []struct {
		name, contentType, wantDisposition string
	}{
		{"a video, inline", "video/mp4", "inline"},
		{"a web page, forced to download", "text/html", "attachment"},
	} {
		for _, hdr := range []map[string]string{nil, {"Range": "bytes=0-99"}} {
			w := serveVideo(t, "GET", hdr, bytes.NewReader(storedBytes()), c.contentType)
			label := c.name
			if hdr != nil {
				label += " (ranged)"
			}
			if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Errorf("%s: Cache-Control = %q", label, got)
			}
			if got := w.Header().Get("Content-Security-Policy"); got != "sandbox" {
				t.Errorf("%s: Content-Security-Policy = %q", label, got)
			}
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("%s: X-Content-Type-Options = %q", label, got)
			}
			if got := w.Header().Get("Content-Disposition"); got == "" || got[:len(c.wantDisposition)] != c.wantDisposition {
				t.Errorf("%s: Content-Disposition = %q, want %s", label, got, c.wantDisposition)
			}
			if got := w.Header().Get("Content-Type"); got != c.contentType {
				t.Errorf("%s: Content-Type = %q, want %q", label, got, c.contentType)
			}
		}
	}
}

// Bytes made on the spot — a thumbnail generated on first view — are ranged
// and sized too, though they have no stored validators.
func TestGeneratedBytesAreSizedToo(t *testing.T) {
	data := storedBytes()
	r := httptest.NewRequest("GET", "/files/abc/thumb", nil)
	w := httptest.NewRecorder()
	w.Header().Set("Content-Type", "image/jpeg")
	serveStoredContent(w, r, bytes.NewReader(data), time.Time{}, "")

	if got := w.Header().Get("Content-Length"); got != "5000" {
		t.Errorf("Content-Length = %q, want 5000", got)
	}
	if w.Header().Get("ETag") != "" || w.Header().Get("Last-Modified") != "" {
		t.Errorf("bytes with no stored object were given validators: ETag %q, Last-Modified %q",
			w.Header().Get("ETag"), w.Header().Get("Last-Modified"))
	}
}

// --- the deadline ---------------------------------------------------------

// deadlineRecorder is a ResponseWriter whose connection can take a write
// deadline, and remembers each one it was given.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.deadlines = append(d.deadlines, t)
	return nil
}

func (d *deadlineRecorder) latest() time.Time { return d.deadlines[len(d.deadlines)-1] }

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestADownloadStartsWithAFreshIdleWindow(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}

	newProgressDeadlineAt(rec, 60*time.Second, 30*time.Minute, clk.now)

	if len(rec.deadlines) != 1 || !rec.latest().Equal(clk.t.Add(60*time.Second)) {
		t.Fatalf("deadlines = %v, want one, 60s from now", rec.deadlines)
	}
}

// The failure this exists for: a deadline measured from the request's start
// cuts off a download that is moving, however slowly. Progress has to carry it
// forward — here for twenty minutes, far past the server's 60 seconds.
func TestASlowDownloadThatKeepsMovingIsNeverCutOff(t *testing.T) {
	const idle = 60 * time.Second
	clk := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	w := newProgressDeadlineAt(rec, idle, 30*time.Minute, clk.now)

	start := clk.t
	for clk.t.Sub(start) < 20*time.Minute {
		clk.t = clk.t.Add(10 * time.Second) // a chunk every ten seconds: slow, but moving
		w.Write([]byte("x"))
		if !rec.latest().After(clk.t) {
			t.Fatalf("at +%v the deadline (%v) had already passed although the client was taking bytes",
				clk.t.Sub(start), rec.latest().Sub(start))
		}
	}
}

func TestAClientThatStopsTakingBytesRunsOutOfTime(t *testing.T) {
	const idle = 60 * time.Second
	clk := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	w := newProgressDeadlineAt(rec, idle, 30*time.Minute, clk.now)

	clk.t = clk.t.Add(30 * time.Second)
	w.Write([]byte("x"))
	stalledAt := clk.t
	last := rec.latest()

	// No more writes: nothing may push the deadline out any further.
	clk.t = clk.t.Add(10 * time.Minute)
	if !rec.latest().Equal(last) {
		t.Error("the deadline moved with no bytes written")
	}
	if got := last.Sub(stalledAt); got > idle {
		t.Errorf("a stall is tolerated for %v, more than the %v idle window", got, idle)
	}
}

func TestADownloadCannotOutliveTheCeiling(t *testing.T) {
	const idle, limit = 60 * time.Second, 5 * time.Minute
	clk := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	w := newProgressDeadlineAt(rec, idle, limit, clk.now)
	hardStop := clk.t.Add(limit)

	for i := 0; i < 40; i++ { // a client that never stops, well past the ceiling
		clk.t = clk.t.Add(15 * time.Second)
		w.Write([]byte("x"))
		for _, d := range rec.deadlines {
			if d.After(hardStop) {
				t.Fatalf("deadline %v is past the ceiling %v", d.Sub(hardStop), limit)
			}
		}
	}
}

// A 130 MB file is thousands of writes; setting a deadline on each would be
// pure overhead.
func TestTheDeadlineIsNotResetOnEveryWrite(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	w := newProgressDeadlineAt(rec, 60*time.Second, 30*time.Minute, clk.now)

	for i := 0; i < 5000; i++ {
		clk.t = clk.t.Add(time.Millisecond)
		w.Write([]byte("x"))
	}
	// 5 seconds of writes against a 15-second re-arm interval: still the first.
	if len(rec.deadlines) != 1 {
		t.Errorf("set the deadline %d times for 5000 writes in 5 seconds", len(rec.deadlines))
	}
}

// http.ResponseController finds the connection by unwrapping; the wrapper has
// to let it, or the deadline would silently never be set on a real response.
func TestTheWrapperLetsAResponseControllerReachTheConnection(t *testing.T) {
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	w := newProgressDeadline(rec, time.Minute, time.Hour)
	before := len(rec.deadlines)

	want := time.Now().Add(time.Hour)
	if err := http.NewResponseController(w).SetWriteDeadline(want); err != nil {
		t.Fatalf("SetWriteDeadline through the wrapper: %v", err)
	}
	if len(rec.deadlines) != before+1 || !rec.latest().Equal(want) {
		t.Error("the deadline did not reach the underlying writer")
	}
}
