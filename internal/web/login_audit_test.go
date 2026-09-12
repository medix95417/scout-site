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

// A session that nothing recorded is a sign-in that never happened as
// far as the activity log is concerned, and the log is the first place
// anyone looks after something goes wrong with an account.
//
// There are three ways into this site — a password, a password plus an
// authenticator code, a password plus a security key — and each used to
// call auth.CreateSession itself. All three now go through
// startSession, which is also where the entry is written; this guard
// fails a fourth route that issues its own.
func TestOnlyStartSessionIssuesASession(t *testing.T) {
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("globbing: %v", err)
	}

	var offenders []string
	for _, file := range matches {
		if strings.HasSuffix(file, "_test.go") || file == "login_audit.go" {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		if strings.Contains(string(src), "auth.CreateSession(") {
			offenders = append(offenders, file)
		}
	}
	if len(offenders) > 0 {
		t.Errorf("auth.CreateSession is called outside login_audit.go, in %v — "+
			"a session issued anywhere else is a sign-in the activity log never sees. "+
			"Route it through h.startSession instead.", offenders)
	}
}

// And startSession has to actually do both halves.
func TestStartSessionCreatesAndRecords(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "login_audit.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing login_audit.go: %v", err)
	}

	calls := func(name string) map[string]bool {
		var decl *ast.FuncDecl
		ast.Inspect(f, func(n ast.Node) bool {
			if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == name {
				decl = d
				return false
			}
			return true
		})
		if decl == nil {
			t.Fatalf("%s is gone from login_audit.go; if it moved, move this guard with it", name)
		}
		seen := map[string]bool{}
		ast.Inspect(decl, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					// Both "CreateSession" and "auth.CreateSession", so a
					// method call and a package call are both nameable.
					seen[fun.Sel.Name] = true
					if pkg, ok := fun.X.(*ast.Ident); ok {
						seen[pkg.Name+"."+fun.Sel.Name] = true
					}
				case *ast.Ident:
					// A plain function in this package, like clientIP.
					seen[fun.Name] = true
				}
			}
			return true
		})
		return seen
	}

	c := calls("startSession")
	for _, want := range []string{"auth.CreateSession", "auth.SetSessionCookie", "logSignIn"} {
		if !c[want] {
			t.Errorf("startSession no longer calls %s", want)
		}
	}

	c = calls("logSignIn")
	for _, want := range []string{"audit.Log", "clientIP"} {
		if !c[want] {
			t.Errorf("logSignIn no longer calls %s — %s", want,
				map[string]string{
					"audit.Log": "nothing reaches the activity log",
					"clientIP":  "the address would come from a header the visitor can write",
				}[want])
		}
	}
}

// The activity log reads the address back out of after_state's "ip" key
// (see audit.LogEntry.IPAddress). Writer and reader agreeing on that key
// is the whole mechanism, and nothing else would fail if they stopped.
func TestSignInRecordsTheAddressUnderTheKeyTheLogReads(t *testing.T) {
	src, err := os.ReadFile("login_audit.go")
	if err != nil {
		t.Fatalf("reading login_audit.go: %v", err)
	}
	if !strings.Contains(string(src), `"ip":`) {
		t.Error(`the sign-in entry no longer writes an "ip" key, so the activity log's From column will be blank`)
	}

	auditSrc, err := os.ReadFile("../audit/audit.go")
	if err != nil {
		t.Fatalf("reading internal/audit/audit.go: %v", err)
	}
	if !strings.Contains(string(auditSrc), `after_state->>'ip'`) {
		t.Error(`the activity log no longer reads after_state->>'ip', so recorded addresses will never be shown`)
	}
}

// readSource / functionBody are small source-reading helpers shared by
// the guards in this package that care about what a handler does rather
// than what it returns. Source-level because the alternative — standing
// a whole server, database and mail server up to observe one
// best-effort call — tests the harness more than the code.
func readSource(file string) (string, error) {
	b, err := os.ReadFile(file)
	return string(b), err
}

// functionBody returns the text of one top-level func, from its
// signature to the closing brace at column 0.
func functionBody(src, name string) (string, bool) {
	start := strings.Index(src, "func "+name+"(")
	if start == -1 {
		// A method: "func (h *Handlers) Name(".
		marker := ") " + name + "("
		i := strings.Index(src, marker)
		if i == -1 {
			return "", false
		}
		start = strings.LastIndex(src[:i], "\nfunc ") + 1
	}
	end := strings.Index(src[start:], "\n}\n")
	if end == -1 {
		return src[start:], true
	}
	return src[start : start+end+3], true
}
