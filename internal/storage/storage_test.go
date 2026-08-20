package storage

import "testing"

func TestNormalizeEndpoint(t *testing.T) {
	cases := []struct {
		name       string
		endpoint   string
		useSSL     bool
		wantHost   string
		wantUseSSL bool
	}{
		{
			name:       "bare host, useSSL false",
			endpoint:   "minio:9000",
			useSSL:     false,
			wantHost:   "minio:9000",
			wantUseSSL: false,
		},
		{
			name:       "DigitalOcean Spaces origin endpoint, exactly as pasted from their dashboard",
			endpoint:   "https://nyc3.digitaloceanspaces.com",
			useSSL:     false, // operator left S3_USE_SSL at its default
			wantHost:   "nyc3.digitaloceanspaces.com",
			wantUseSSL: true, // the https:// scheme wins
		},
		{
			name:       "http scheme forces useSSL false even if the flag says true",
			endpoint:   "http://localhost:9000",
			useSSL:     true,
			wantHost:   "localhost:9000",
			wantUseSSL: false,
		},
		{
			name:       "trailing slash and path stripped",
			endpoint:   "https://nyc3.digitaloceanspaces.com/some/bucket/path",
			useSSL:     false,
			wantHost:   "nyc3.digitaloceanspaces.com",
			wantUseSSL: true,
		},
		{
			name:       "no scheme, trailing slash stripped",
			endpoint:   "s3.amazonaws.com/",
			useSSL:     true,
			wantHost:   "s3.amazonaws.com",
			wantUseSSL: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotHost, gotUseSSL := normalizeEndpoint(c.endpoint, c.useSSL)
			if gotHost != c.wantHost || gotUseSSL != c.wantUseSSL {
				t.Errorf("normalizeEndpoint(%q, %v) = (%q, %v), want (%q, %v)",
					c.endpoint, c.useSSL, gotHost, gotUseSSL, c.wantHost, c.wantUseSSL)
			}
		})
	}
}

func TestNew_EmptyEndpointReturnsNilStoreNoError(t *testing.T) {
	store, err := New(nil, Config{}) //nolint:staticcheck // nil context is fine here, New never uses it
	if err != nil {
		t.Fatalf("expected no error for unconfigured storage, got %v", err)
	}
	if store != nil {
		t.Fatalf("expected a nil Store for an empty endpoint, got %+v", store)
	}
}

func TestNew_ConfiguredEndpointNeverErrors(t *testing.T) {
	// New deliberately never contacts the network or validates the
	// endpoint is reachable (see its doc comment) — this exercises the
	// exact reported crash: a real value straight from a provider's
	// dashboard must build a client without error, even though it's a
	// full URL rather than the bare host minio-go's Endpoint field wants.
	_, err := New(nil, Config{ //nolint:staticcheck
		Endpoint:  "https://nyc3.digitaloceanspaces.com",
		AccessKey: "key",
		SecretKey: "secret",
		Bucket:    "scoutsite-files",
	})
	if err != nil {
		t.Fatalf("expected New to succeed against a normalized endpoint, got %v", err)
	}
}
