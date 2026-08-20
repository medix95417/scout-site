package web

import (
	"testing"

	"github.com/47-yonkers/scout-site/internal/files"
)

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
