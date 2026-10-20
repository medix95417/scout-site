package web

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/47-yonkers/scout-site/internal/storage"
)

// memBucket is an in-memory stand-in for an S3 bucket, spoken to over HTTP so
// the real minio client — and therefore the real storage.Store the handlers
// use — runs against it. Enough of the API for a handler test: put, get and
// head with ranges, delete, and the bucket-location probe.
type memBucket struct {
	mu      sync.Mutex
	objects map[string][]byte
}

const memBucketName = "scout-test"

func newMemBucket(t *testing.T) (*memBucket, *storage.Store) {
	t.Helper()
	b := &memBucket{objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(srv.Close)

	store, err := storage.New(context.Background(), storage.Config{
		Endpoint:  srv.URL,
		AccessKey: "test",
		SecretKey: "test",
		Bucket:    memBucketName,
	})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	return b, store
}

func (b *memBucket) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("location") {
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/"+memBucketName+"/")

	b.mu.Lock()
	defer b.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		data, err := readPutBody(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b.objects[key] = data
		w.Header().Set("ETag", `"fake"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		delete(b.objects, key)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet, http.MethodHead:
		data, ok := b.objects[key]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
			return
		}
		w.Header().Set("ETag", `"fake"`)
		http.ServeContent(w, r, "", time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), bytes.NewReader(data))
	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

// readPutBody returns the object a PUT carried. Over plain HTTP the minio
// client signs each chunk of the body ("aws-chunked": a hex length and a
// signature, a line break, the bytes, a line break, and a zero-length chunk to
// end), which a real S3 unwraps before storing; storing the framing along with
// the bytes would make every object look corrupt.
func readPutBody(r *http.Request) ([]byte, error) {
	if !strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
		return io.ReadAll(r.Body)
	}
	br := bufio.NewReader(r.Body)
	var out []byte
	for {
		header, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.ParseInt(strings.TrimSpace(strings.SplitN(header, ";", 2)[0]), 16, 64)
		if err != nil {
			return nil, err
		}
		if size == 0 {
			return out, nil
		}
		chunk := make([]byte, size)
		if _, err := io.ReadFull(br, chunk); err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		if _, err := br.Discard(2); err != nil { // the line break after the bytes
			return nil, err
		}
	}
}

func (b *memBucket) has(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.objects[key]
	return ok
}

func (b *memBucket) get(key string) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.objects[key]
}

func (b *memBucket) keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for k := range b.objects {
		out = append(out, k)
	}
	return out
}
