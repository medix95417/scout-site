package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryTemplateParses parses every page template exactly the way
// web.New does, and fails naming the file when one doesn't.
//
// Worth having because until this existed, a template was only ever
// parsed at server startup: a typo'd {{end}} or a call to a function not
// in templateFuncs compiled fine, passed the whole test suite, passed
// CI, and only surfaced as a failure to boot. The unit tests around it
// test Go helpers, not the templates those helpers feed.
//
// This checks parsing, not rendering — a field that doesn't exist on a
// page's data struct is an execution-time error and won't be caught here
// (see TestForgotPasswordTemplate_RendersEveryDataShape for that shape of
// test, done per-page where the data matters).
func TestEveryTemplateParses(t *testing.T) {
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		t.Fatalf("reading embedded templates: %v", err)
	}

	// base.html and the _-prefixed partials are parsed as part of every
	// page rather than on their own, so they're covered by each page
	// below rather than named here.
	var pages []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".html") {
			continue
		}
		if name == "base.html" || strings.HasPrefix(name, "_") {
			continue
		}
		pages = append(pages, name)
	}
	if len(pages) < 40 {
		t.Fatalf("only found %d page templates — this test is not looking where it thinks it is", len(pages))
	}

	// parsePageTemplate is the same function New itself uses, so a page
	// that parses here is one that parses at startup — no second copy of
	// the parsing rules to drift.
	for _, page := range pages {
		if _, err := parsePageTemplate(page); err != nil {
			t.Errorf("template %s does not parse: %v", page, err)
		}
	}
}

// TestEveryTemplateIsWiredUp catches the other half: a template file that
// exists but no handler ever parses is dead weight, and more importantly a
// page a developer believes is live but never rendered.
func TestEveryTemplateIsWiredUp(t *testing.T) {
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		t.Fatalf("reading embedded templates: %v", err)
	}
	webSrc := packageSource(t)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".html") {
			continue
		}
		if name == "base.html" || strings.HasPrefix(name, "_") {
			continue
		}
		if !strings.Contains(webSrc, `"`+name+`"`) {
			t.Errorf("template %s is never parsed by any handler — either wire it up or delete it", name)
		}
	}
}

// packageSource is every non-test .go file in this package concatenated,
// for the "is this template referenced anywhere" check above.
func packageSource(t *testing.T) string {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("globbing package files: %v", err)
	}
	var b strings.Builder
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		b.Write(src)
	}
	return b.String()
}
