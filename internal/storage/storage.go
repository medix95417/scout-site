// Package storage puts and gets file bytes in S3-compatible object storage
// (works against MinIO, AWS S3, Cloudflare R2, or anything else speaking
// the S3 API — see Config). The database (internal/files) only ever stores
// a key into this store, never the bytes themselves.
//
// Downloads are streamed back through the app server rather than handed
// out as presigned URLs straight to the bucket: every other access-controlled
// resource in this app (roster data, ledger entries, permission slips) is
// gated by a server-side auth check on each request, and files are no
// different — a presigned URL would leak access to anyone who later gets
// hold of the link, bypassing that check entirely.
package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Config points at an S3-compatible endpoint and bucket — a self-hosted
// MinIO you run separately, AWS S3, Cloudflare R2, or anything else
// speaking the S3 API. Endpoint empty means storage is unconfigured (see
// New).
type Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

// Store is a thin wrapper around a minio client scoped to one bucket.
type Store struct {
	client *minio.Client
	bucket string
}

// New builds a client for the configured S3-compatible endpoint. Returns a
// nil Store (and no error) when cfg.Endpoint is empty — file storage is
// optional, same as internal/mailer degrades gracefully when SMTP isn't
// configured: the rest of the site should keep working even if an operator
// hasn't set up (or has temporarily lost) their S3 bucket. Callers must
// check for a nil Store the same way they already check mailer.Enabled.
//
// Deliberately does NOT verify connectivity or that the bucket exists —
// unlike the bundled-MinIO setup this once had, the bucket now lives in a
// service this app doesn't manage, so auto-creating it isn't appropriate
// (and often isn't even permitted by a real cloud provider's IAM policy).
// Create the bucket yourself before expecting uploads to work; a
// misconfigured or unreachable endpoint surfaces as a clear per-request
// error from Put/Get/Delete, not a startup crash.
func New(_ context.Context, cfg Config) (*Store, error) {
	if cfg.Endpoint == "" {
		return nil, nil
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: configuring client for %s: %w", cfg.Endpoint, err)
	}
	return &Store{client: client, bucket: cfg.Bucket}, nil
}

// Put uploads size bytes from r under key, replacing any existing object at
// that key.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("storage: uploading %q: %w", key, err)
	}
	return nil
}

// Get opens the object at key for reading. Callers must Close the result.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("storage: opening %q: %w", key, err)
	}
	// GetObject doesn't itself contact the server or return a 404 for a
	// missing key — that only surfaces on the first read, so Stat here
	// turns a missing object into an error at the call site that expects
	// one, instead of a confusing empty/zero-length download.
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, fmt.Errorf("storage: %q not found: %w", key, err)
	}
	return obj, nil
}

// Delete removes the object at key. Deleting a key that doesn't exist is
// not an error — the caller's goal ("this key is gone") is already true.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("storage: deleting %q: %w", key, err)
	}
	return nil
}
