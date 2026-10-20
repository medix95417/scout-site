package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/csrf"
)

// The limits on an upload are set in two packages — the per-file cap here,
// the per-submission cap in internal/csrf — and a third number, the server's
// write timeout, decides whether a slow upload gets its answer. Nothing but
// these tests notices one being moved without the others.

// A file the page accepts has to fit in a request the server accepts, with
// room for the multipart framing and the form's own fields. Otherwise the
// limit on screen is one nobody can reach, and the person sees a generic
// "too large" for a file the page said was fine.
func TestAFileAtTheLimitFitsInOneRequest(t *testing.T) {
	const framing = 1 << 20 // headers, boundaries and the form's other fields
	if cap := csrf.MaxRequestBodySize(); maxUploadFileSize+framing > cap {
		t.Fatalf("a file of %d MB cannot be sent: one request is capped at %d MB, so the per-file limit is unreachable",
			maxUploadFileSize>>20, cap>>20)
	}
}

func TestTheFilesPageSkipMessageStatesTheRealLimit(t *testing.T) {
	msg := skippedTooLargeMessage([]string{"a.mp4", "b.mp4"})
	if want := strconv.Itoa(maxUploadFileSize>>20) + " MB"; !strings.Contains(msg, want) {
		t.Errorf("the skip message doesn't state the %s limit the server enforces: %q", want, msg)
	}
	if !strings.Contains(msg, "2 file(s)") || !strings.Contains(msg, "a.mp4, b.mp4") {
		t.Errorf("the skip message lost the count or the names: %q", msg)
	}
}

// http.Server.WriteTimeout runs from the end of the request headers, and the
// middleware reads the whole upload body before any handler runs — so a
// large file over an ordinary connection outlasts it, and the handler's
// answer is lost after the work is done. The browser then re-sends the lot
// and the file is stored twice (measured, on a throttled link, against the
// Files page before it extended its deadline).
//
// Every handler that takes an upload has to extend its own deadline, and
// each new one is a place to forget. This finds them by what they do — read
// r.MultipartForm — instead of by a list that would need remembering.
func TestEveryUploadHandlerExtendsItsWriteDeadline(t *testing.T) {
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
			if !strings.Contains(body, "r.MultipartForm") {
				continue
			}
			found++
			if !strings.Contains(body, "extendWriteDeadline(w,") {
				t.Errorf("%s (in %s) takes a file upload but never extends its write deadline — a slow upload "+
					"would be stored and its answer lost, and the browser would send it again", fn.Name.Name, name)
			}
		}
	}
	// The scan has to still be looking at something. Two handlers take files
	// today; finding none means the pattern above no longer matches how they
	// read one, and the test would pass for no reason.
	if found < 2 {
		t.Fatalf("found %d upload handlers, expected at least 2 — the scan has lost its subject", found)
	}
}
