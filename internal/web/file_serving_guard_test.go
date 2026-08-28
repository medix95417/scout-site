package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryStoredByteResponseUsesTheSafeHeaders is a structural test, and
// it exists because reviewing by hand already failed once.
//
// When the stored-XSS hole was closed, two routes served user-uploaded
// bytes and both were fixed. A third — the resources page's download,
// which is the only one an anonymous visitor can reach — was missed
// entirely, and kept serving the stored content type inline with no
// sandbox for another four pull requests.
//
// So rather than trusting the next reader to remember, this walks the
// package: any function that pulls an object out of storage must also
// call writeUserFileHeaders. Adding a fourth route without it fails here.
func TestEveryStoredByteResponseUsesTheSafeHeaders(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("globbing package files: %v", err)
	}

	fset := token.NewFileSet()
	checked := 0

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}

			var fetchesStoredBytes, setsSafeHeaders bool
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := call.Fun.(type) {
				case *ast.SelectorExpr:
					// h.Storage.Get(...) — pulling an uploaded object out
					// of object storage to write to a response.
					if f.Sel.Name == "Get" {
						if inner, ok := f.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "Storage" {
							fetchesStoredBytes = true
						}
					}
				case *ast.Ident:
					if f.Name == "writeUserFileHeaders" {
						setsSafeHeaders = true
					}
				}
				return true
			})

			if !fetchesStoredBytes {
				continue
			}
			checked++
			if !setsSafeHeaders {
				t.Errorf("%s: %s reads bytes from storage but never calls writeUserFileHeaders — "+
					"user-uploaded content must not be served with its stored Content-Type inline "+
					"(see file_serving.go)", path, fn.Name.Name)
			}
		}
	}

	// If this drops to zero the test has quietly stopped testing anything,
	// which is worse than failing.
	if checked == 0 {
		t.Fatal("found no handlers reading from storage — this guard is no longer checking anything")
	}
	t.Logf("checked %d handler(s) that serve stored bytes", checked)
}
