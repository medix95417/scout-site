package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// An input the library picker fills must not be type="url".
//
// The picker writes this site's own files in as a path —
// /files/{id}/download — and an <input type="url"> requires an absolute
// URL with a scheme, so the browser refuses the form before it is ever
// submitted and reports "enter a URL". The field looks broken and the
// photo cannot be chosen at all.
//
// This is a source check rather than a rendered-page one because it has
// to hold for every such field on every admin page at once, and because
// the way it was missed is instructive: every live check of these pages
// was made with curl, which never runs client-side validation. Only a
// real browser sees this, so only a rule about the markup catches it.
//
// The two remaining type="url" inputs on the site — the social media
// link and the resources page's direct link — are external links with no
// picker attached, and are right to keep the browser's validation.

var inputTagPattern = regexp.MustCompile(`(?s)<input\b[^>]*>`)

func TestPickerBackedInputsAreNotURLTyped(t *testing.T) {
	dir := "templates"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading templates: %v", err)
	}

	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		for _, tag := range inputTagPattern.FindAllString(string(body), -1) {
			if !strings.Contains(tag, "data-image-url-input") {
				continue
			}
			checked++
			if strings.Contains(tag, `type="url"`) {
				t.Errorf("%s: an input the library picker fills is type=\"url\", so choosing a photo from the library will be refused by the browser:\n%s", e.Name(), tag)
			}
		}
	}

	// If the attribute is ever renamed, this test would silently pass
	// while checking nothing.
	if checked == 0 {
		t.Error("found no picker-backed inputs at all — has data-image-url-input been renamed?")
	}
}

// The picker has to keep writing a path rather than an absolute URL:
// thumbURL and bannerURL both match on the "/files/" prefix, so a picker
// that started writing https://host/files/... would quietly serve every
// visitor the full-size original again.
func TestPickerWritesAPathNotAnAbsoluteURL(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("templates", "_image-picker.html"))
	if err != nil {
		t.Fatalf("reading the picker: %v", err)
	}
	src := string(body)
	if !strings.Contains(src, `printf "/files/%s/download" .ID`) {
		t.Error("the picker no longer offers a /files/{id}/download path; thumbURL and bannerURL match on that prefix")
	}
	if strings.Contains(src, "window.location.origin") {
		t.Error("the picker builds an absolute URL, which would bypass the resized-variant rewriting")
	}
}
