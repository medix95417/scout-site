package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 is just enough of the S3 API for the minio client to open and read
// one object: a bucket-location answer, HEAD and GET of the object with Range
// support, and the "no such key" error. It records every object request, so a
// test can say what the app asked the bucket for rather than only what came
// back.
type fakeS3 struct {
	srv     *httptest.Server
	data    []byte
	modTime time.Time

	mu       sync.Mutex
	requests []string // "HEAD", "GET bytes=100-", ...
}

const fakeBucket = "scout-test"

func newFakeS3(t *testing.T, data []byte) (*fakeS3, *Store) {
	t.Helper()
	f := &fakeS3{data: data, modTime: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)

	store, err := New(context.Background(), Config{
		Endpoint:  f.srv.URL, // "http://127.0.0.1:port" — normalizeEndpoint strips the scheme
		AccessKey: "test",
		SecretKey: "test",
		Bucket:    fakeBucket,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f, store
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("location") {
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
		return
	}
	if r.URL.Path != "/"+fakeBucket+"/video.mp4" {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
		return
	}

	f.mu.Lock()
	f.requests = append(f.requests, strings.TrimSpace(r.Method+" "+r.Header.Get("Range")))
	f.mu.Unlock()

	w.Header().Set("ETag", `"0123456789abcdef"`)
	http.ServeContent(w, r, "", f.modTime, bytes.NewReader(f.data))
}

func (f *fakeS3) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func sampleBytes(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(1)).Read(b)
	return b
}

func TestOpenReportsWhatARangeResponseNeeds(t *testing.T) {
	data := sampleBytes(2 << 20)
	f, store := newFakeS3(t, data)

	r, err := store.Open(context.Background(), "video.mp4")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	if r.Size != int64(len(data)) {
		t.Errorf("Size = %d, want %d", r.Size, len(data))
	}
	if !r.ModTime.Equal(f.modTime) {
		t.Errorf("ModTime = %v, want %v", r.ModTime, f.modTime)
	}
	if r.ETag != "0123456789abcdef" {
		t.Errorf("ETag = %q, want the bucket's own, unquoted", r.ETag)
	}
}

// The point of a Reader over a plain ReadCloser: serving bytes from the
// middle of a file asks the bucket for the middle of the file. A seek that
// quietly read from the start would make every "skip ahead in the video" cost
// the whole file all over again, which is the problem this exists to solve.
func TestSeekThenReadAsksTheBucketOnlyForThatRange(t *testing.T) {
	data := sampleBytes(2 << 20)
	f, store := newFakeS3(t, data)

	r, err := store.Open(context.Background(), "video.mp4")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	const start = 1_000_000
	if _, err := r.Seek(0, io.SeekEnd); err != nil { // what http.ServeContent does first, to learn the size
		t.Fatalf("Seek end: %v", err)
	}
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	got := make([]byte, 1000)
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, data[start:start+1000]) {
		t.Fatal("bytes read after seeking are not the bytes at that position")
	}

	t.Logf("the bucket was asked: %v", f.recorded())
	for _, req := range f.recorded() {
		if req == "GET" || strings.HasPrefix(req, "GET bytes=0-") {
			t.Errorf("the bucket was asked for the file from its start (%q) although only the middle was wanted; requests: %v", req, f.recorded())
		}
	}
}

func TestOpenAMissingObjectIsAnError(t *testing.T) {
	_, store := newFakeS3(t, sampleBytes(10))
	if r, err := store.Open(context.Background(), "nope.mp4"); err == nil {
		r.Close()
		t.Fatal("Open of a key that is not there succeeded")
	}
	if rc, err := store.Get(context.Background(), "nope.mp4"); err == nil {
		rc.Close()
		t.Fatal("Get of a key that is not there succeeded")
	}
}

func TestGetStillReturnsTheWholeObject(t *testing.T) {
	data := sampleBytes(300_000)
	_, store := newFakeS3(t, data)

	rc, err := store.Get(context.Background(), "video.mp4")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("Get did not return the object's bytes")
	}
}
