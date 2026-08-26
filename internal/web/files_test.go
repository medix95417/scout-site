package web

import (
	"testing"

	"github.com/47-yonkers/scout-site/internal/files"
)

func TestThumbURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"own file download link", "/files/abc-123/download", "/files/abc-123/thumb"},
		{"external URL passes through unchanged", "https://example.com/photo.jpg", "https://example.com/photo.jpg"},
		{"empty string passes through unchanged", "", ""},
		{"missing download suffix passes through unchanged", "/files/abc-123", "/files/abc-123"},
		{"already a thumb link passes through unchanged", "/files/abc-123/thumb", "/files/abc-123/thumb"},
		{"id containing a slash is rejected", "/files/abc/123/download", "/files/abc/123/download"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := thumbURL(c.url); got != c.want {
				t.Errorf("thumbURL(%q) = %q, want %q", c.url, got, c.want)
			}
		})
	}
}

func TestRequiresLoginToDownload(t *testing.T) {
	cases := []struct {
		name     string
		public   bool
		loggedIn bool
		want     bool
	}{
		{"private file, logged out", false, false, true},
		{"private file, logged in", false, true, false},
		{"public file, logged out", true, false, false},
		{"public file, logged in", true, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := requiresLoginToDownload(files.File{Public: c.public}, c.loggedIn)
			if got != c.want {
				t.Errorf("requiresLoginToDownload(Public=%v, loggedIn=%v) = %v, want %v", c.public, c.loggedIn, got, c.want)
			}
		})
	}
}
